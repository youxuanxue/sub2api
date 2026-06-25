#!/usr/bin/env bash
# Create and validate a narrowly-scoped CloudFormation change set that reconciles
# the Stage0 prod DataVolume size parameter without replacing Instance/EIPAssoc.
#
# Default mode is plan-only: reuse the live template, stabilize the AMI SSM
# parameter against a stable SSM name that resolves to the current AMI, create
# a no-execute change set, validate it, and delete it unless --keep-change-set is
# passed. This deliberately avoids applying the full repo template: adding
# DataVolumeIops/DataVolumeThroughput to the live template currently pulls
# Instance/EIPAssoc into the same change set through UserData's DataVolume
# reference.
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage: ops/stage0/reconcile-cfn-datavolume-no-replace.sh [options]

Options:
  --stack NAME              default: tokenkey-prod-stage0
  --region REGION           default: us-east-1
  --size GIB                desired DataVolumeSizeGiB, default: 50
  --change-set-name NAME    default: datavolume-no-replace-<pid>-<epoch>
  --keep-change-set         leave the validated no-execute change set in CFN
  -h, --help

Safety:
  This script is intentionally plan-only. It only plans DataVolumeSizeGiB=50
  with the previous live template. IOPS/throughput CFN ownership still needs a
  separate maintenance-window plan because current live UserData references the
  DataVolume logical id and CloudFormation marks Instance/EIPAssoc conditional
  changes when those template fields are added.
USAGE
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

STACK="tokenkey-prod-stage0"
REGION="us-east-1"
SIZE="50"
CHANGE_SET_NAME="datavolume-no-replace-$$-$(date +%s)"
KEEP_CHANGE_SET=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --stack) STACK="${2:-}"; shift 2 ;;
    --region) REGION="${2:-}"; shift 2 ;;
    --size) SIZE="${2:-}"; shift 2 ;;
    --change-set-name) CHANGE_SET_NAME="${2:-}"; shift 2 ;;
    --keep-change-set) KEEP_CHANGE_SET=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done

[[ -n "${STACK}" && -n "${REGION}" ]] || { usage; exit 2; }
[[ "${SIZE}" =~ ^[0-9]+$ ]] || {
  echo "::error::size must be a positive integer" >&2
  exit 2
}

TMP_DIR="$(mktemp -d)"
PARAMS_FILE="${TMP_DIR}/parameters.json"
STACK_JSON="${TMP_DIR}/stack.json"
CHANGESET_JSON="${TMP_DIR}/changeset.json"
STABLE_AMI_PARAM=""
STABLE_AMI_PARAM_PREEXISTED=0
CHANGE_SET_CREATED=0
CHANGE_SET_VALIDATED=0

cleanup() {
  local rc=$?
  local cleanup_failed=0
  if [[ "${CHANGE_SET_CREATED}" = 1 && ! ( "${KEEP_CHANGE_SET}" = 1 && "${CHANGE_SET_VALIDATED}" = 1 ) ]]; then
    if ! aws cloudformation delete-change-set --region "${REGION}" \
      --stack-name "${STACK}" --change-set-name "${CHANGE_SET_NAME}" >/dev/null 2>"${TMP_DIR}/delete-change-set.err"; then
      echo "::error::failed to delete preview change set ${CHANGE_SET_NAME}" >&2
      cat "${TMP_DIR}/delete-change-set.err" >&2
      cleanup_failed=1
    fi
  fi
  if [[ -n "${STABLE_AMI_PARAM}" && ! ( "${KEEP_CHANGE_SET}" = 1 && "${CHANGE_SET_VALIDATED}" = 1 ) && "${STABLE_AMI_PARAM_PREEXISTED}" = 0 ]]; then
    if ! aws ssm delete-parameter --region "${REGION}" --name "${STABLE_AMI_PARAM}" >/dev/null 2>"${TMP_DIR}/delete-parameter.err"; then
      echo "::error::failed to delete preview SSM parameter ${STABLE_AMI_PARAM}" >&2
      cat "${TMP_DIR}/delete-parameter.err" >&2
      cleanup_failed=1
    fi
  fi
  rm -rf "${TMP_DIR}"
  if [[ "${rc}" = 0 && "${cleanup_failed}" = 1 ]]; then
    rc=2
  fi
  exit "${rc}"
}
trap cleanup EXIT

echo "[datavolume] describing stack ${STACK} (${REGION})" >&2
aws cloudformation describe-stacks --region "${REGION}" --stack-name "${STACK}" --output json >"${STACK_JSON}"

echo "[datavolume] creating temporary stable AMI SSM parameter" >&2
CURRENT_AMI_ID="$(aws cloudformation describe-stacks --region "${REGION}" --stack-name "${STACK}" \
  --query 'Stacks[0].Parameters[?ParameterKey==`AmazonLinux2023Arm64Ami`].ResolvedValue | [0]' \
  --output text)"
if [[ -z "${CURRENT_AMI_ID}" || "${CURRENT_AMI_ID}" == "None" ]]; then
  echo "::error::could not resolve current AmazonLinux2023Arm64Ami value from stack parameters" >&2
  exit 2
fi
STACK_PATH_COMPONENT="$(printf '%s' "${STACK}" | tr -c 'A-Za-z0-9_.-' '-')"
STABLE_AMI_PARAM="/tokenkey/${STACK_PATH_COMPONENT}/cfn-datavolume-no-replace/current-ami"
if aws ssm get-parameter --region "${REGION}" --name "${STABLE_AMI_PARAM}" >/dev/null 2>&1; then
  STABLE_AMI_PARAM_PREEXISTED=1
fi
aws ssm put-parameter --region "${REGION}" \
  --name "${STABLE_AMI_PARAM}" \
  --type String \
  --overwrite \
  --value "${CURRENT_AMI_ID}" >/dev/null

python3 - "${STACK_JSON}" "${PARAMS_FILE}" "${SIZE}" "${STABLE_AMI_PARAM}" <<'PY'
import json
import sys

stack_file, params_file, size, stable_ami_param = sys.argv[1:5]
with open(stack_file, encoding="utf-8") as f:
    stack = json.load(f)["Stacks"][0]

desired = {
    "DataVolumeSizeGiB": size,
    "AmazonLinux2023Arm64Ami": stable_ami_param,
}
params = []
for p in stack.get("Parameters", []):
    key = p["ParameterKey"]
    if key in desired:
        params.append({"ParameterKey": key, "ParameterValue": desired[key]})
    else:
        params.append({"ParameterKey": key, "UsePreviousValue": True})
with open(params_file, "w", encoding="utf-8") as f:
    json.dump(params, f, indent=2)
    f.write("\n")
PY

echo "[datavolume] creating no-execute change set ${CHANGE_SET_NAME}" >&2
if ! aws cloudformation create-change-set --region "${REGION}" \
  --stack-name "${STACK}" \
  --change-set-name "${CHANGE_SET_NAME}" \
  --change-set-type UPDATE \
  --use-previous-template \
  --parameters "file://${PARAMS_FILE}" \
  --capabilities CAPABILITY_IAM CAPABILITY_NAMED_IAM >/dev/null 2>"${TMP_DIR}/create.err"; then
  echo "::error::create-change-set failed" >&2
  cat "${TMP_DIR}/create.err" >&2
  exit 2
fi
CHANGE_SET_CREATED=1

set +e
aws cloudformation wait change-set-create-complete --region "${REGION}" \
  --stack-name "${STACK}" --change-set-name "${CHANGE_SET_NAME}" >/dev/null 2>"${TMP_DIR}/wait.err"
WAIT_RC=$?
set -e

if ! STATUS="$(aws cloudformation describe-change-set --region "${REGION}" \
  --stack-name "${STACK}" --change-set-name "${CHANGE_SET_NAME}" \
  --query Status --output text 2>"${TMP_DIR}/status.err")"; then
  echo "::error::failed to describe change set status" >&2
  cat "${TMP_DIR}/status.err" >&2
  if [[ "${WAIT_RC}" -ne 0 ]]; then
    cat "${TMP_DIR}/wait.err" >&2
  fi
  exit 2
fi
if ! REASON="$(aws cloudformation describe-change-set --region "${REGION}" \
  --stack-name "${STACK}" --change-set-name "${CHANGE_SET_NAME}" \
  --query StatusReason --output text 2>"${TMP_DIR}/reason.err")"; then
  echo "::error::failed to describe change set status reason" >&2
  cat "${TMP_DIR}/reason.err" >&2
  exit 2
fi

if [[ "${STATUS}" == "FAILED" ]] && grep -qiE "didn't contain changes|No updates are to be performed" <<<"${REASON}"; then
  echo "ok: ${STACK} DataVolume parameters already converged"
  exit 0
fi
if [[ "${STATUS}" != "CREATE_COMPLETE" ]]; then
  echo "::error::change set did not reach CREATE_COMPLETE: status=${STATUS} reason=${REASON}" >&2
  exit 2
fi

aws cloudformation describe-change-set --region "${REGION}" \
  --stack-name "${STACK}" --change-set-name "${CHANGE_SET_NAME}" --output json >"${CHANGESET_JSON}"

echo "[datavolume] validating DataVolume-only / Replacement=False contract" >&2
python3 "${SCRIPT_DIR}/cfn_datavolume_changeset_guard.py" --allowed-properties Size <"${CHANGESET_JSON}"
CHANGE_SET_VALIDATED=1

echo
aws cloudformation describe-change-set --region "${REGION}" \
  --stack-name "${STACK}" --change-set-name "${CHANGE_SET_NAME}" \
  --query 'Changes[].ResourceChange.{action:Action,id:LogicalResourceId,type:ResourceType,replacement:Replacement,scope:Scope}' \
  --output table

if [[ "${KEEP_CHANGE_SET}" = 1 ]]; then
  echo "validated_change_set=${CHANGE_SET_NAME}"
  echo "stable_ami_ssm_parameter=${STABLE_AMI_PARAM}"
  echo "note: keep this SSM parameter permanently if the retained change set is executed"
  echo "next: human review, then manually apply the validated change set named ${CHANGE_SET_NAME}"
else
  echo "ok: validated preview only; change set will be deleted"
fi
