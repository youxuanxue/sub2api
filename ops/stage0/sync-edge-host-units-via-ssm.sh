#!/usr/bin/env bash
# Bring a running Lightsail EDGE's host-level systemd units up to prod parity via
# SSM Run-Command. Mirrors ops/stage0/pg_dump_refresh_via_ssm.sh.
#
# Why this exists: prod (deploy/aws/stage0/stage0-ec2-bootstrap.sh) installs AND
# enables two host units that the edge bootstrap (deploy/aws/lightsail/render-bootstrap.sh)
# does NOT wire — render-bootstrap is capped at 14336 bytes of Lightsail user-data
# and only `chmod +x`'s the scripts. So existing + new edges silently lacked:
#   1. on-box disk-full Feishu alert (tokenkey-disk-metrics.sh + .timer) — #778 was
#      prod-only; an edge that fills its root volume crashed Postgres with NO alert.
#   2. QA stale cleanup (tokenkey-qa-stale-cleanup.sh + .timer + retention env) — the
#      script shipped but never ran on edges, so qa_records/qa_blobs grew unbounded
#      (13+ days / multi-GB) while prod stayed pruned at 1.5 days.
# render-bootstrap can't grow (user-data cap), and deploy_via_ssm.sh does not re-run
# bootstrap, so this SSM push is the path that reaches existing edges (the same
# situation #778 solved for prod with pg_dump_refresh_via_ssm.sh). The edge deploy
# workflow calls this after provision/upgrade; run it standalone to backfill a node.
#
# Single source of record for the unit payloads:
#   - deploy/aws/lightsail/tokenkey-disk-metrics-edge.sh  (disk alert; feishu-only, df /)
#   - deploy/aws/stage0/tokenkey-qa-stale-cleanup.sh      (QA prune; shared with prod)
# Edit only those files; this script base64-pushes them verbatim.
#
# Prereq for the disk alert to actually post: TOKENKEY_FEISHU_WEBHOOK_URL/_SECRET must
# be present in /var/lib/tokenkey/.env (the alert no-ops silently otherwise). The edge
# deploy workflow's sync-feishu-config step mirrors them there.
#
# Usage:
#   bash ops/stage0/sync-edge-host-units-via-ssm.sh <instance-id|mi-...> [comment]
#   AWS_REGION=us-east-2 TK_QA_STALE_RETENTION_DAYS=1.5 \
#     bash ops/stage0/sync-edge-host-units-via-ssm.sh mi-...

set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-ops-edge-host-units-sync}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"
# Edge QA retention in days (fractional OK). Default 1.5 = prod parity (the CFN
# QaStaleRetentionDays default). edges are pure relays so QA there is low-value;
# keep it short.
QA_RETENTION_DAYS="${TK_QA_STALE_RETENTION_DAYS:-1.5}"

if [ -z "${INSTANCE_ID}" ]; then
  echo "stage0_sync_edge_host_units_via_ssm: instance id is required" >&2
  exit 1
fi
if ! printf '%s' "${QA_RETENTION_DAYS}" | grep -Eq '^(0|[1-9][0-9]*)(\.[0-9]+)?$'; then
  echo "stage0_sync_edge_host_units_via_ssm: invalid TK_QA_STALE_RETENTION_DAYS=${QA_RETENTION_DAYS}" >&2
  exit 1
fi

ssm_region_args=()
if [ -n "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" ]; then
  ssm_region_args=(--region "${AWS_REGION:-${AWS_DEFAULT_REGION}}")
fi

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DM_SRC="${SCRIPT_DIR}/../../deploy/aws/lightsail/tokenkey-disk-metrics-edge.sh"
QA_SRC="${SCRIPT_DIR}/../../deploy/aws/stage0/tokenkey-qa-stale-cleanup.sh"
[[ -f "${DM_SRC}" ]] || { echo "missing ${DM_SRC}" >&2; exit 1; }
[[ -f "${QA_SRC}" ]] || { echo "missing ${QA_SRC}" >&2; exit 1; }

# Encode each payload to single-line base64 so embedding into the JSON command
# array is shell-quoting-safe. tr -d strips both GNU and BSD base64 wrapping.
DM_SH_B64="$(base64 <"${DM_SRC}" | tr -d '\n')"
QA_SH_B64="$(base64 <"${QA_SRC}" | tr -d '\n')"

DM_SERVICE_B64="$(cat <<'DMSEOF' | base64 | tr -d '\n'
[Unit]
Description=tokenkey EDGE on-box disk-full Feishu alert
After=network-online.target tokenkey.service
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile=-/var/lib/tokenkey/.env
ExecStart=/usr/local/bin/tokenkey-disk-metrics.sh
DMSEOF
)"

DM_TIMER_B64="$(cat <<'DMTEOF' | base64 | tr -d '\n'
[Unit]
Description=Fire tokenkey EDGE disk-full alert every 5 minutes

[Timer]
OnBootSec=3min
OnUnitActiveSec=5min
RandomizedDelaySec=30
Persistent=true

[Install]
WantedBy=timers.target
DMTEOF
)"

QA_SERVICE_B64="$(cat <<'QASEOF' | base64 | tr -d '\n'
[Unit]
Description=Prune QA records and blob trees older than retention
After=network-online.target tokenkey.service
Wants=network-online.target
Requires=tokenkey.service

[Service]
Type=oneshot
EnvironmentFile=-/etc/tokenkey/qa-stale-retention.env
ExecStart=/usr/local/bin/tokenkey-qa-stale-cleanup.sh
QASEOF
)"

QA_TIMER_B64="$(cat <<'QATEOF' | base64 | tr -d '\n'
[Unit]
Description=Daily QA stale cleanup (low-traffic window)

[Timer]
OnCalendar=*-*-* 04:15:00
RandomizedDelaySec=30min
Persistent=true

[Install]
WantedBy=timers.target
QATEOF
)"

# In-place sync trace: which commit the live units are now aligned to.
TEMPLATE_SHA="${GITHUB_SHA:-local}"

jq -n \
  --arg dmsh "${DM_SH_B64}" \
  --arg dmsvc "${DM_SERVICE_B64}" \
  --arg dmtmr "${DM_TIMER_B64}" \
  --arg qash "${QA_SH_B64}" \
  --arg qasvc "${QA_SERVICE_B64}" \
  --arg qatmr "${QA_TIMER_B64}" \
  --arg qadays "${QA_RETENTION_DAYS}" \
  --arg sha "${TEMPLATE_SHA}" \
  '{
    commands: [
      "set -euo pipefail",
      "echo === edge host-units sync: disk-full alert + QA stale cleanup ===",
      "sudo install -d -m 0755 /etc/tokenkey",
      ("echo " + $dmsh + " | base64 -d | sudo tee /usr/local/bin/tokenkey-disk-metrics.sh > /dev/null"),
      "sudo chmod +x /usr/local/bin/tokenkey-disk-metrics.sh",
      ("echo " + $dmsvc + " | base64 -d | sudo tee /etc/systemd/system/tokenkey-disk-metrics.service > /dev/null"),
      ("echo " + $dmtmr + " | base64 -d | sudo tee /etc/systemd/system/tokenkey-disk-metrics.timer > /dev/null"),
      ("echo " + $qash + " | base64 -d | sudo tee /usr/local/bin/tokenkey-qa-stale-cleanup.sh > /dev/null"),
      "sudo chmod +x /usr/local/bin/tokenkey-qa-stale-cleanup.sh",
      ("echo " + $qasvc + " | base64 -d | sudo tee /etc/systemd/system/tokenkey-qa-stale-cleanup.service > /dev/null"),
      ("echo " + $qatmr + " | base64 -d | sudo tee /etc/systemd/system/tokenkey-qa-stale-cleanup.timer > /dev/null"),
      ("printf '\''TOKENKEY_QA_STALE_RETENTION_DAYS=%s\\n'\'' " + $qadays + " | sudo tee /etc/tokenkey/qa-stale-retention.env > /dev/null"),
      "sudo systemctl daemon-reload",
      "sudo systemctl enable --now tokenkey-disk-metrics.timer",
      "sudo systemctl enable --now tokenkey-qa-stale-cleanup.timer",
      "sudo systemctl restart tokenkey-disk-metrics.timer tokenkey-qa-stale-cleanup.timer",
      "echo --- timers ---",
      "sudo systemctl list-timers tokenkey-disk-metrics.timer tokenkey-qa-stale-cleanup.timer --no-pager || true",
      "echo --- retention env ---",
      "cat /etc/tokenkey/qa-stale-retention.env || true",
      "echo --- feishu webhook present in .env -- disk alert no-ops without it --",
      "grep -cE '\''^TOKENKEY_FEISHU_WEBHOOK_URL='\'' /var/lib/tokenkey/.env || true",
      ("echo Live edge host units now match deploy/aws@" + $sha + " on $(hostname)")
    ]
  }' > "${params_file}"

cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
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
  status="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
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

aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardOutputContent' --output text > "${stdout_file}"
aws "${ssm_region_args[@]}" ssm get-command-invocation \
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
