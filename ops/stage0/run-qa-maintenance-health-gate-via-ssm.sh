#!/usr/bin/env bash
# Run the blocking QA maintenance health gate through the real systemd unit.
set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-qa-maintenance-systemd-health-gate}}"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-2700}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.qa-maintenance-health-gate}"

[[ "${INSTANCE_ID}" =~ ^i-[0-9a-f]{17}$ ]] || { echo "valid instance id required" >&2; exit 1; }
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GATE_SRC="$(cd "${SCRIPT_DIR}/../.." && pwd)/deploy/aws/stage0/tokenkey-qa-maintenance-health-gate.sh"
[[ -f "${GATE_SRC}" ]] || { echo "missing ${GATE_SRC}" >&2; exit 1; }

mkdir -p "${OUTPUT_DIR}"
params="${OUTPUT_DIR}/ssm-params.json"
stdout="${OUTPUT_DIR}/stdout.txt"
stderr="${OUTPUT_DIR}/stderr.txt"
gate_payload="$(base64 <"${GATE_SRC}" | tr -d '\n')"

jq -n --arg gate "${gate_payload}" '{commands:[
  "set -euo pipefail",
  ("printf %s " + ($gate | @sh) + " | base64 -d | sudo bash")
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
[[ "${status}" = Success && "${code}" = 0 ]] || { echo "QA maintenance health gate SSM failed status=${status} code=${code}" >&2; exit 1; }
