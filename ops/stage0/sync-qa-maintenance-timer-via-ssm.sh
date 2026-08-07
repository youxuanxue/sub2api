#!/usr/bin/env bash
# Install tokenkey-qa-maintenance.{sh,service,timer} on prod Stage0 via SSM.
#
# Usage:
#   bash ops/stage0/sync-qa-maintenance-timer-via-ssm.sh <instance-id> [comment]

set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-ops-qa-maintenance-timer-sync}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

if [ -z "${INSTANCE_ID}" ]; then
  echo "sync_qa_maintenance_timer_via_ssm: instance id is required" >&2
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
MAINT_SRC="${SCRIPT_DIR}/../../deploy/aws/stage0/tokenkey-qa-maintenance.sh"
[[ -f "${MAINT_SRC}" ]] || { echo "missing ${MAINT_SRC}" >&2; exit 1; }

MAINT_SH_B64="$(base64 <"${MAINT_SRC}" | tr -d '\n')"
TEMPLATE_SHA="${GITHUB_SHA:-local}"

jq -n \
  --arg maint "${MAINT_SH_B64}" \
  --arg sha "${TEMPLATE_SHA}" \
  '{
    commands: [
      "set -euo pipefail",
      "echo === qa-maintenance timer sync ===",
      ("echo " + $maint + " | base64 -d | sudo tee /usr/local/bin/tokenkey-qa-maintenance.sh > /dev/null"),
      "sudo chmod +x /usr/local/bin/tokenkey-qa-maintenance.sh",
      "sudo /usr/local/bin/tokenkey-qa-maintenance.sh --selftest",
      "sudo /usr/local/bin/tokenkey-qa-maintenance.sh --install-units",
      "sudo systemctl daemon-reload",
      "sudo systemctl enable tokenkey-qa-maintenance.timer",
      "echo \"--- timer not started until QA_ARCHIVE_ENABLED=true ---\"",
      "sudo systemctl list-timers tokenkey-qa-maintenance.timer --no-pager || true",
      ("echo Live qa-maintenance units now match deploy/aws@" + $sha + " on $(hostname)")
    ]
  }' > "${params_file}"

cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "${COMMENT}" \
  --parameters "file://${params_file}" \
  --query 'Command.CommandId' --output text)"

echo "ssm command-id=${cmd_id}"

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status="InProgress"
while [ "${status}" = "InProgress" ] || [ "${status}" = "Pending" ] || [ "${status}" = "Delayed" ]; do
  if [ "$(date +%s)" -ge "${deadline}" ]; then
    echo "timeout waiting for ssm command ${cmd_id}" >&2
    exit 1
  fi
  sleep 3
  status="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
    --command-id "${cmd_id}" \
    --instance-id "${INSTANCE_ID}" \
    --query Status --output text)"
done

aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" \
  --instance-id "${INSTANCE_ID}" \
  --query StandardOutputContent --output text > "${stdout_file}"
aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" \
  --instance-id "${INSTANCE_ID}" \
  --query StandardErrorContent --output text > "${stderr_file}"
code="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" \
  --instance-id "${INSTANCE_ID}" \
  --query ResponseCode --output text)"

cat "${stdout_file}"
if [ -s "${stderr_file}" ]; then
  echo "--- stderr ---" >&2
  cat "${stderr_file}" >&2
fi
if [ "${status}" != "Success" ] || [ "${code}" != "0" ]; then
  echo "ssm command failed status=${status} code=${code}" >&2
  exit 1
fi
