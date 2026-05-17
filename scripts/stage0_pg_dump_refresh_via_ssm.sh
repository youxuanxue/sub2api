#!/usr/bin/env bash
# Push refreshed pg_dump cadence (every 2h, ≤12 rolling files) into a running
# Stage0 EC2 via SSM Run-Command. Mirrors the user-data segment in
# deploy/aws/cloudformation/stage0-single-ec2.yaml so the live timer matches
# the template-of-record without rebuilding the EC2.

set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-ops-pg-dump-refresh}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

if [ -z "${INSTANCE_ID}" ]; then
  echo "stage0_pg_dump_refresh_via_ssm: instance id is required" >&2
  exit 1
fi

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

# Encode each payload to single-line base64 so embedding into the JSON command
# array is shell-quoting-safe. tr -d strips both GNU and BSD base64 wrapping.
PG_DUMP_SH_B64="$(cat <<'PGEOF' | base64 | tr -d '\n'
#!/bin/bash
set -euo pipefail
DUMP_DIR=/var/lib/tokenkey/pgdump
TS=$(date -u +%Y%m%dT%H%M%SZ)
OUT="${DUMP_DIR}/tokenkey-${TS}.sql.gz"
PART="${OUT}.part"
rm -f "${PART}"

# Remove bogus sub-kib dumps from failed runs (e.g. disk full).
find "${DUMP_DIR}" -maxdepth 1 -type f -name 'tokenkey-*.sql.gz' -size -2k -delete 2>/dev/null || true
# Remove legacy pre-*.dump files left by older manual pre-migration snapshots.
find "${DUMP_DIR}" -maxdepth 1 -type f -name 'pre-*.dump' -delete 2>/dev/null || true

set -o pipefail
if ! docker exec tokenkey-postgres pg_dump -U tokenkey -d tokenkey --format=plain --no-owner \
    | gzip -9 > "${PART}"; then
  rm -f "${PART}"
  exit 1
fi

SZ=$(wc -c < "${PART}")
if [ "${SZ}" -lt 2048 ]; then
  rm -f "${PART}"
  exit 1
fi

mv -f "${PART}" "${OUT}"

# Past 24h by mtime, and at most 12 rolling tokenkey-*.sql.gz (2h cadence).
find "${DUMP_DIR}" -maxdepth 1 -type f -name 'tokenkey-*.sql.gz' -mmin +1440 -delete 2>/dev/null || true
while IFS= read -r _oldf; do
  [ -z "${_oldf}" ] && continue
  rm -f "${_oldf}"
done < <(find "${DUMP_DIR}" -maxdepth 1 -type f -name 'tokenkey-*.sql.gz' -printf '%T@\t%p\n' 2>/dev/null \
  | sort -nr | tail -n +13 | cut -f2-)
PGEOF
)"

PG_SERVICE_B64="$(cat <<'PSEOF' | base64 | tr -d '\n'
[Unit]
Description=tokenkey pg_dump (every 2 hours)
After=tokenkey.service
Requires=tokenkey.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/tokenkey-pgdump.sh
PSEOF
)"

PG_TIMER_B64="$(cat <<'PTEOF' | base64 | tr -d '\n'
[Unit]
Description=Run tokenkey-pgdump every 2 hours

[Timer]
OnCalendar=*-*-* 00,02,04,06,08,10,12,14,16,18,20,22:00:00
Persistent=true
RandomizedDelaySec=2min

[Install]
WantedBy=timers.target
PTEOF
)"

# In-place sync trace: written to the SSM stdout so operators can confirm
# which stage0-single-ec2.yaml commit the live timer is now aligned to.
# Falls back to "local" when run outside a workflow.
TEMPLATE_SHA="${GITHUB_SHA:-local}"

jq -n \
  --arg sh "${PG_DUMP_SH_B64}" \
  --arg svc "${PG_SERVICE_B64}" \
  --arg tmr "${PG_TIMER_B64}" \
  --arg sha "${TEMPLATE_SHA}" \
  '{
    commands: [
      "set -euo pipefail",
      "echo === pg_dump refresh: every 2h, max 12 rolling files ===",
      ("echo " + $sh + " | base64 -d | sudo tee /usr/local/bin/tokenkey-pgdump.sh > /dev/null"),
      "sudo chmod +x /usr/local/bin/tokenkey-pgdump.sh",
      ("echo " + $svc + " | base64 -d | sudo tee /etc/systemd/system/tokenkey-pgdump.service > /dev/null"),
      ("echo " + $tmr + " | base64 -d | sudo tee /etc/systemd/system/tokenkey-pgdump.timer > /dev/null"),
      "sudo systemctl daemon-reload",
      "sudo systemctl enable --now tokenkey-pgdump.timer",
      "sudo systemctl restart tokenkey-pgdump.timer",
      "echo --- timer status ---",
      "sudo systemctl status tokenkey-pgdump.timer --no-pager | head -20 || true",
      "echo --- next firings ---",
      "sudo systemctl list-timers tokenkey-pgdump.timer --no-pager || true",
      "echo --- service unit definition ---",
      "sudo systemctl cat tokenkey-pgdump.service --no-pager || true",
      "echo --- timer unit definition ---",
      "sudo systemctl cat tokenkey-pgdump.timer --no-pager || true",
      ("echo --- in-place sync trace ---"),
      ("echo Live tokenkey-pgdump.{sh,service,timer} now match deploy/aws/cloudformation/stage0-single-ec2.yaml@" + $sha + " on $(hostname)")
    ]
  }' > "${params_file}"

cmd_id="$(aws ssm send-command \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "${COMMENT}" \
  --parameters "file://${params_file}" \
  --query 'Command.CommandId' --output text)"

echo "ssm command-id=${cmd_id}"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  echo "command_id=${cmd_id}" >> "${GITHUB_OUTPUT}"
fi

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status="InProgress"
while true; do
  status="$(aws ssm get-command-invocation \
    --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)"
  case "${status}" in
    Success|Failed|TimedOut|Cancelled) break ;;
  esac
  if [ "$(date +%s)" -ge "${deadline}" ]; then
    echo "::error::ssm timeout" >&2
    status="TimedOut"
    break
  fi
  sleep 5
done

aws ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardOutputContent' --output text > "${stdout_file}"
aws ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardErrorContent' --output text > "${stderr_file}"

echo '--- ssm stdout (last 8KB) ---'
tail -c 8192 "${stdout_file}"
echo
echo '--- ssm stderr (last 8KB) ---'
tail -c 8192 "${stderr_file}"
echo

if [ "${status}" != "Success" ]; then
  echo "::error::ssm command status=${status}" >&2
  exit 1
fi
