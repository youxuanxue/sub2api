#!/usr/bin/env bash
# Install the QA hourly boundary runtime and optionally switch from legacy cleanup.
set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-ops-qa-boundary-timer-sync}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"
TIMER_STATE="${QA_BOUNDARY_TIMER_STATE:-disabled}"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"

[[ "${INSTANCE_ID}" =~ ^i-[0-9a-f]{17}$ ]] || { echo "valid instance id required" >&2; exit 1; }
case "${TIMER_STATE}" in
  disabled)
    owner_command="sudo systemctl disable --now tokenkey-qa-boundary.timer"
    boundary_active=inactive
    ;;
  enabled)
    owner_command="sudo bash -c 'set -euo pipefail; rollback() { local rc=\$?; trap - ERR; systemctl disable --now tokenkey-qa-stale-cleanup.timer || true; systemctl enable --now tokenkey-qa-boundary.timer || true; return \"\${rc}\"; }; trap rollback ERR; finalize_count=\$(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 -c \"SELECT count(*) FROM qa_lifecycle_receipts f JOIN qa_lifecycle_receipts a ON a.t0_utc=f.t0_utc AND a.phase=concat(chr(97),chr(99),chr(116),chr(105),chr(118),chr(97),chr(116),chr(101)) WHERE f.phase=concat(chr(102),chr(105),chr(110),chr(97),chr(108),chr(105),chr(122),chr(101))\" | tr -d \"[:space:]\"); test \"\${finalize_count}\" = 1; systemctl disable --now tokenkey-qa-stale-cleanup.timer; systemctl enable --now tokenkey-qa-boundary.timer; test \"\$(systemctl is-enabled tokenkey-qa-boundary.timer)\" = enabled; test \"\$(systemctl is-active tokenkey-qa-boundary.timer)\" = active; test \"\$(systemctl is-enabled tokenkey-qa-stale-cleanup.timer)\" = disabled; test \"\$(systemctl is-active tokenkey-qa-stale-cleanup.timer)\" = inactive; trap - ERR'"
    boundary_active=active
    ;;
  *)
    echo "QA_BOUNDARY_TIMER_STATE must be disabled or enabled" >&2
    exit 1
    ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BOUNDARY_SRC="${SCRIPT_DIR}/../../deploy/aws/stage0/tokenkey-qa-boundary.sh"
HELPER_SRC="${SCRIPT_DIR}/../../deploy/aws/stage0/tokenkey-qa-export-orphan.py"
RESOLVER_SRC="${SCRIPT_DIR}/../lib/resolve-app-container.sh"
for source in "${BOUNDARY_SRC}" "${HELPER_SRC}" "${RESOLVER_SRC}"; do
  [[ -f "${source}" ]] || { echo "missing ${source}" >&2; exit 1; }
done

boundary_payload="$(gzip -9n -c "${BOUNDARY_SRC}" | base64 | tr -d '\n')"
helper_payload="$(gzip -9n -c "${HELPER_SRC}" | base64 | tr -d '\n')"
resolver_payload="$(gzip -9n -c "${RESOLVER_SRC}" | base64 | tr -d '\n')"
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
  --arg boundary_active "${boundary_active}" '
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
    "if sudo systemctl list-unit-files tokenkey-qa-boundary.timer --no-legend 2>/dev/null | grep -q \"^tokenkey-qa-boundary[.]timer\"; then sudo systemctl disable --now tokenkey-qa-boundary.timer; fi",
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
    ("test \"$(sudo systemctl is-enabled tokenkey-qa-boundary.timer)\" = \"" + $timer_state + "\""),
    ("test \"$(sudo systemctl is-active tokenkey-qa-boundary.timer)\" = \"" + $boundary_active + "\"")
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
