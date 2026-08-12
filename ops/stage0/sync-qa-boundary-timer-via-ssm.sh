#!/usr/bin/env bash
# Install the QA hourly boundary runtime and optionally switch from legacy cleanup.
set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-ops-qa-boundary-timer-sync}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"
DRAIN_TIMEOUT_SECONDS="${QA_SYNC_DRAIN_TIMEOUT_SECONDS:-300}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"
TIMER_STATE="${QA_BOUNDARY_TIMER_STATE:-disabled}"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"

# QA_BOUNDARY_TIMER_STATE: disabled | enabled | auto
[[ "${INSTANCE_ID}" =~ ^i-[0-9a-f]{17}$ ]] || { echo "valid instance id required" >&2; exit 1; }
[[ "${DRAIN_TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || { echo "QA_SYNC_DRAIN_TIMEOUT_SECONDS must be a positive integer" >&2; exit 1; }
case "${TIMER_STATE}" in
  disabled)
    owner_command="sudo systemctl disable --now tokenkey-qa-boundary.timer"
    boundary_active=inactive
    ;;
  enabled)
    owner_command="sudo bash -c 'set -euo pipefail; rollback() { local rc=\$?; trap - ERR; systemctl disable --now tokenkey-qa-stale-cleanup.timer || true; systemctl enable --now tokenkey-qa-boundary.timer || true; return \"\${rc}\"; }; trap rollback ERR; finalize_count=\$(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 -c \"SELECT count(*) FROM qa_lifecycle_receipts f JOIN qa_lifecycle_receipts a ON a.t0_utc=f.t0_utc AND a.phase=concat(chr(97),chr(99),chr(116),chr(105),chr(118),chr(97),chr(116),chr(101)) WHERE f.phase=concat(chr(102),chr(105),chr(110),chr(97),chr(108),chr(105),chr(122),chr(101))\" | tr -d \"[:space:]\"); test \"\${finalize_count}\" = 1; systemctl disable --now tokenkey-qa-stale-cleanup.timer; systemctl enable --now tokenkey-qa-boundary.timer; test \"\$(systemctl is-enabled tokenkey-qa-boundary.timer)\" = enabled; test \"\$(systemctl is-active tokenkey-qa-boundary.timer)\" = active; test \"\$(systemctl is-enabled tokenkey-qa-stale-cleanup.timer)\" = disabled; test \"\$(systemctl is-active tokenkey-qa-stale-cleanup.timer)\" = inactive; trap - ERR'"
    boundary_active=active
    ;;
  auto)
    owner_command="sudo bash -c 'set -euo pipefail; counts=\$(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -F : -v ON_ERROR_STOP=1 -c \"SELECT (SELECT count(*) FROM qa_lifecycle_receipts WHERE phase=concat(chr(97),chr(99),chr(116),chr(105),chr(118),chr(97),chr(116),chr(101))), (SELECT count(*) FROM qa_lifecycle_receipts f JOIN qa_lifecycle_receipts a ON a.t0_utc=f.t0_utc AND a.phase=concat(chr(97),chr(99),chr(116),chr(105),chr(118),chr(97),chr(116),chr(101)) WHERE f.phase=concat(chr(102),chr(105),chr(110),chr(97),chr(108),chr(105),chr(122),chr(101)))\" | tr -d \"[:space:]\"); case \"\${counts}\" in 0:0) systemctl disable --now tokenkey-qa-boundary.timer; test \"\$(systemctl is-enabled tokenkey-qa-boundary.timer)\" = disabled; test \"\$(systemctl is-active tokenkey-qa-boundary.timer)\" = inactive ;; 1:0) systemctl disable --now tokenkey-qa-boundary.timer; test \"\$(systemctl is-enabled tokenkey-qa-boundary.timer)\" = disabled; test \"\$(systemctl is-active tokenkey-qa-boundary.timer)\" = inactive; test \"\$(systemctl is-enabled tokenkey-qa-stale-cleanup.timer)\" = enabled; test \"\$(systemctl is-active tokenkey-qa-stale-cleanup.timer)\" = active ;; 1:1) systemctl disable --now tokenkey-qa-stale-cleanup.timer; systemctl enable --now tokenkey-qa-boundary.timer; test \"\$(systemctl is-enabled tokenkey-qa-boundary.timer)\" = enabled; test \"\$(systemctl is-active tokenkey-qa-boundary.timer)\" = active; test \"\$(systemctl is-enabled tokenkey-qa-stale-cleanup.timer)\" = disabled; test \"\$(systemctl is-active tokenkey-qa-stale-cleanup.timer)\" = inactive ;; *) echo \"invalid QA lifecycle receipt counts: \${counts}\" >&2; exit 1 ;; esac'"
    boundary_active=auto
    ;;
  *)
    echo "QA_BOUNDARY_TIMER_STATE must be disabled, enabled, or auto" >&2
    exit 1
    ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -n "${QA_HOST_ARTIFACT_ROOT:-}" ]; then
  ARTIFACT_ROOT="$(cd "${QA_HOST_ARTIFACT_ROOT}" && pwd)"
else
  ARTIFACT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
fi
BOUNDARY_SRC="${ARTIFACT_ROOT}/deploy/aws/stage0/tokenkey-qa-boundary.sh"
HELPER_SRC="${ARTIFACT_ROOT}/deploy/aws/stage0/tokenkey-qa-export-orphan.py"
RESOLVER_SRC="${ARTIFACT_ROOT}/ops/lib/resolve-app-container.sh"
for source in "${BOUNDARY_SRC}" "${HELPER_SRC}" "${RESOLVER_SRC}"; do
  [[ -f "${source}" ]] || { echo "missing ${source}" >&2; exit 1; }
done

boundary_payload="$(gzip -9n -c "${BOUNDARY_SRC}" | base64 | tr -d '\n')"
helper_payload="$(gzip -9n -c "${HELPER_SRC}" | base64 | tr -d '\n')"
resolver_payload="$(gzip -9n -c "${RESOLVER_SRC}" | base64 | tr -d '\n')"
artifact_sha="${QA_HOST_ARTIFACT_SHA:-${GITHUB_SHA:-local}}"
mkdir -p "${OUTPUT_DIR}"
params="${OUTPUT_DIR}/ssm-params.json"
stdout="${OUTPUT_DIR}/stdout.txt"
stderr="${OUTPUT_DIR}/stderr.txt"

jq -n \
  --arg boundary "${boundary_payload}" \
  --arg helper "${helper_payload}" \
  --arg resolver "${resolver_payload}" \
  --arg owner_command "${owner_command}" \
  --arg timer_state "${TIMER_STATE}" \
  --arg boundary_active "${boundary_active}" \
  --arg artifact_sha "${artifact_sha}" \
  --argjson drain_timeout "${DRAIN_TIMEOUT_SECONDS}" '
  def atomic_install($artifact; $destination; $mode):
    "sudo bash -c " + (
      (
        "set -euo pipefail; destination=" + ($destination | @sh)
        + "; directory=$(dirname \"$destination\"); install -d -m 0755 \"$directory\""
        + "; temporary=$(mktemp \"$directory/.${destination##*/}.XXXXXX\")"
        + "; trap \"rm -f \\\"$temporary\\\"\" EXIT"
        + "; printf %s " + ($artifact | @sh) + " | base64 -d | gunzip > \"$temporary\""
        + "; chmod " + $mode + " \"$temporary\"; mv -f \"$temporary\" \"$destination\""
      ) | @sh
    );
  {commands:[
    "set -euo pipefail",
    "qa_finalized=$(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 -c \"SELECT count(*) FROM qa_lifecycle_receipts f JOIN qa_lifecycle_receipts a ON a.t0_utc=f.t0_utc AND a.phase=concat(chr(97),chr(99),chr(116),chr(105),chr(118),chr(97),chr(116),chr(101)) WHERE f.phase=concat(chr(102),chr(105),chr(110),chr(97),chr(108),chr(105),chr(122),chr(101))\" | tr -d \"[:space:]\"); case \"${qa_finalized}\" in 0|1) ;; *) echo \"invalid finalized receipt count: ${qa_finalized}\" >&2; exit 1 ;; esac; if sudo systemctl is-enabled --quiet tokenkey-qa-boundary.timer; then qa_boundary_enabled=1; else qa_boundary_enabled=0; fi; if sudo systemctl is-active --quiet tokenkey-qa-boundary.timer; then qa_boundary_active=1; else qa_boundary_active=0; fi; if sudo systemctl is-enabled --quiet tokenkey-qa-stale-cleanup.timer; then qa_legacy_enabled=1; else qa_legacy_enabled=0; fi; if sudo systemctl is-active --quiet tokenkey-qa-stale-cleanup.timer; then qa_legacy_active=1; else qa_legacy_active=0; fi; qa_sync_committed=0; qa_sync_restore() { qa_sync_rc=$?; trap - EXIT; if [ \"${qa_sync_committed}\" != 1 ]; then if [ \"${qa_finalized}\" = 1 ]; then sudo systemctl enable tokenkey-qa-boundary.timer >/dev/null 2>&1 || true; sudo systemctl start tokenkey-qa-boundary.timer >/dev/null 2>&1 || true; sudo systemctl disable tokenkey-qa-stale-cleanup.timer >/dev/null 2>&1 || true; sudo systemctl stop tokenkey-qa-stale-cleanup.timer >/dev/null 2>&1 || true; else if [ \"${qa_boundary_enabled}\" = 1 ]; then sudo systemctl enable tokenkey-qa-boundary.timer >/dev/null 2>&1 || true; else sudo systemctl disable tokenkey-qa-boundary.timer >/dev/null 2>&1 || true; fi; if [ \"${qa_boundary_active}\" = 1 ]; then sudo systemctl start tokenkey-qa-boundary.timer >/dev/null 2>&1 || true; else sudo systemctl stop tokenkey-qa-boundary.timer >/dev/null 2>&1 || true; fi; if [ \"${qa_legacy_enabled}\" = 1 ]; then sudo systemctl enable tokenkey-qa-stale-cleanup.timer >/dev/null 2>&1 || true; else sudo systemctl disable tokenkey-qa-stale-cleanup.timer >/dev/null 2>&1 || true; fi; if [ \"${qa_legacy_active}\" = 1 ]; then sudo systemctl start tokenkey-qa-stale-cleanup.timer >/dev/null 2>&1 || true; else sudo systemctl stop tokenkey-qa-stale-cleanup.timer >/dev/null 2>&1 || true; fi; fi; fi; exit \"${qa_sync_rc}\"; }; trap qa_sync_restore EXIT",
    "if sudo systemctl list-unit-files tokenkey-qa-boundary.timer --no-legend 2>/dev/null | grep -q \"^tokenkey-qa-boundary[.]timer\"; then sudo systemctl disable --now tokenkey-qa-boundary.timer; fi",
    ("qa_sync_deadline=$(( $(date +%s) + " + ($drain_timeout | tostring) + " )); while sudo systemctl is-active --quiet tokenkey-qa-boundary.service; do if [ \"$(date +%s)\" -ge \"${qa_sync_deadline}\" ]; then echo \"timeout draining tokenkey-qa-boundary.service\" >&2; exit 1; fi; sleep 2; done"),
    "! sudo systemctl is-active --quiet tokenkey-qa-boundary.service",
    atomic_install($resolver; "/usr/local/lib/tokenkey/resolve-app-container.sh"; "0644"),
    atomic_install($helper; "/usr/local/lib/tokenkey/qa-export-orphan.py"; "0755"),
    atomic_install($boundary; "/usr/local/bin/tokenkey-qa-boundary.sh"; "0755"),
    "sudo test -d /var/lib/tokenkey/app/qa_blobs",
    "sudo test -d /var/lib/tokenkey/app/qa_dlq",
    "sudo test -d /var/lib/tokenkey/app/qa_exports_tmp",
    "sudo /usr/local/bin/tokenkey-qa-boundary.sh --install-units",
    "sudo systemctl daemon-reload",
    $owner_command,
    (if $timer_state == "auto" then empty else
      "test \"$(sudo systemctl is-enabled tokenkey-qa-boundary.timer)\" = \"" + $timer_state + "\""
    end),
    (if $timer_state == "auto" then empty else
      "test \"$(sudo systemctl is-active tokenkey-qa-boundary.timer)\" = \"" + $boundary_active + "\""
    end),
    "qa_sync_committed=1",
    "trap - EXIT",
    ("echo Live qa-boundary units now match deploy/aws@" + $artifact_sha + " timer=" + $timer_state + " on $(hostname)")
  ]}' >"${params}"

command_id="$(aws --region "${REGION}" ssm send-command \
  --instance-ids "${INSTANCE_ID}" --document-name AWS-RunShellScript \
  --comment "${COMMENT}" --parameters "file://${params}" \
  --query Command.CommandId --output text)"
echo "ssm command-id=${command_id}"
deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status=InProgress
while [[ "${status}" =~ ^(Pending|InProgress|Delayed)$ ]]; do
  (( $(date +%s) < deadline )) || { echo "timeout waiting for ${command_id}" >&2; exit 1; }
  sleep 3
  status="$(aws --region "${REGION}" ssm get-command-invocation --command-id "${command_id}" --instance-id "${INSTANCE_ID}" --query Status --output text)"
done
aws --region "${REGION}" ssm get-command-invocation --command-id "${command_id}" --instance-id "${INSTANCE_ID}" --query StandardOutputContent --output text >"${stdout}"
aws --region "${REGION}" ssm get-command-invocation --command-id "${command_id}" --instance-id "${INSTANCE_ID}" --query StandardErrorContent --output text >"${stderr}"
code="$(aws --region "${REGION}" ssm get-command-invocation --command-id "${command_id}" --instance-id "${INSTANCE_ID}" --query ResponseCode --output text)"
cat "${stdout}"
[[ ! -s "${stderr}" ]] || cat "${stderr}" >&2
[[ "${status}" == Success && "${code}" == 0 ]] || { echo "SSM failed status=${status} code=${code}" >&2; exit 1; }
