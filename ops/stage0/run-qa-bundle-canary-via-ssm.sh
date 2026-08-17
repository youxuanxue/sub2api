#!/usr/bin/env bash
# Execute the canonical end-to-end QA Bundle canary on the prod Stage0 host.
set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-qa-bundle-canary}}"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-900}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.qa-bundle-canary}"

[[ "${INSTANCE_ID}" =~ ^i-[0-9a-f]{17}$ ]] || { echo "valid instance id required" >&2; exit 1; }
mkdir -p "${OUTPUT_DIR}"
params="${OUTPUT_DIR}/ssm-params.json"
stdout="${OUTPUT_DIR}/stdout.txt"
stderr="${OUTPUT_DIR}/stderr.txt"
jq -n '{commands:[
  "set -euo pipefail",
  "sudo /usr/local/bin/tokenkey-qa-maintenance.sh --qa-bundle-canary"
]}' >"${params}"

command_id="$(aws --region "${REGION}" ssm send-command \
  --instance-ids "${INSTANCE_ID}" --document-name AWS-RunShellScript \
  --comment "${COMMENT}" --parameters "file://${params}" \
  --query Command.CommandId --output text)"
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
[[ "${status}" = Success && "${code}" = 0 ]] || { echo "QA Bundle canary SSM failed status=${status} code=${code}" >&2; exit 1; }

python3 - "${stdout}" <<'PY'
import json
import pathlib
import sys

receipt = None
for raw in pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace").splitlines():
    try:
        candidate = json.loads(raw)
    except json.JSONDecodeError:
        continue
    if isinstance(candidate, dict) and candidate.get("schema_version") == "qa-bundle-canary-v1":
        receipt = candidate
valid = (
    isinstance(receipt, dict)
    and receipt.get("ok") is True
    and receipt.get("commit_count") == 24
    and receipt.get("record_count") == 0
    and isinstance(receipt.get("job_id"), str)
    and len(receipt["job_id"]) == 64
)
raise SystemExit(0 if valid else "invalid QA Bundle canary receipt")
PY
