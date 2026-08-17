#!/usr/bin/env bash
# Plan and deploy the standalone QA raw archive CloudFormation stack.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMPLATE="${ROOT}/deploy/aws/cloudformation/stage0-qa-raw-archive.yaml"
STACK="${QA_RAW_ARCHIVE_STACK:-tokenkey-prod-qa-raw-archive}"
REGION="${AWS_REGION:-us-east-1}"
PROJECT="${QA_RAW_ARCHIVE_PROJECT:-tokenkey}"
ENVIRONMENT="${QA_RAW_ARCHIVE_ENVIRONMENT:-prod}"
CHANGE_SET="qa-raw-archive-$(date -u +%Y%m%dT%H%M%SZ)-$$"

require_value() {
  local name="$1" value="${2:-}"
  if [ -z "${value}" ]; then
    echo "${name} is required" >&2
    exit 1
  fi
}

require_value APP_INSTANCE_ROLE_ARN "${APP_INSTANCE_ROLE_ARN:-}"
require_value OPS_RECOVERY_PRINCIPAL_ARN "${OPS_RECOVERY_PRINCIPAL_ARN:-}"
require_value QA_RAW_ARCHIVE_VPC_ID "${QA_RAW_ARCHIVE_VPC_ID:-}"
require_value QA_RAW_ARCHIVE_ROUTE_TABLE_IDS "${QA_RAW_ARCHIVE_ROUTE_TABLE_IDS:-}"
require_value QA_BUNDLE_WORKER_PUBLIC_SUBNET_IDS "${QA_BUNDLE_WORKER_PUBLIC_SUBNET_IDS:-}"
require_value QA_BUNDLE_WORKER_IMAGE "${QA_BUNDLE_WORKER_IMAGE:-}"
require_value QA_BUNDLE_BROWSER_ALLOWED_ORIGIN "${QA_BUNDLE_BROWSER_ALLOWED_ORIGIN:-}"

role_pattern='^arn:[a-z0-9-]+:iam::[0-9]{12}:role/.+$'
principal_pattern='^arn:[a-z0-9-]+:iam::[0-9]{12}:(user|role)/.+$'
if [[ ! "${APP_INSTANCE_ROLE_ARN}" =~ ${role_pattern} ]]; then
  echo "APP_INSTANCE_ROLE_ARN is not a role ARN" >&2
  exit 1
fi
if [[ ! "${OPS_RECOVERY_PRINCIPAL_ARN}" =~ ${principal_pattern} ]]; then
  echo "OPS_RECOVERY_PRINCIPAL_ARN is not a user or role ARN" >&2
  exit 1
fi
if [[ ! "${QA_RAW_ARCHIVE_VPC_ID}" =~ ^vpc-[0-9a-f]+$ ]]; then
  echo "QA_RAW_ARCHIVE_VPC_ID is not a VPC ID" >&2
  exit 1
fi
image_pattern='^.+(@sha256:[a-f0-9]{64}|:[A-Za-z0-9][A-Za-z0-9._-]*)$'
if [[ "${QA_BUNDLE_WORKER_IMAGE}" == *:latest || ! "${QA_BUNDLE_WORKER_IMAGE}" =~ ${image_pattern} ]]; then
  echo "QA_BUNDLE_WORKER_IMAGE must be an immutable release tag or sha256 digest" >&2
  exit 1
fi
origin_pattern='^https://[A-Za-z0-9][A-Za-z0-9.-]*(:[0-9]{1,5})?$'
if [[ ! "${QA_BUNDLE_BROWSER_ALLOWED_ORIGIN}" =~ ${origin_pattern} ]]; then
  echo "QA_BUNDLE_BROWSER_ALLOWED_ORIGIN must be one exact HTTPS origin" >&2
  exit 1
fi
IFS=',' read -r -a route_tables <<<"${QA_RAW_ARCHIVE_ROUTE_TABLE_IDS}"
if [ "${#route_tables[@]}" -eq 0 ]; then
  echo "QA_RAW_ARCHIVE_ROUTE_TABLE_IDS is empty" >&2
  exit 1
fi
for route_table in "${route_tables[@]}"; do
  if [[ ! "${route_table}" =~ ^rtb-[0-9a-f]+$ ]]; then
    echo "QA_RAW_ARCHIVE_ROUTE_TABLE_IDS contains invalid ID: ${route_table}" >&2
    exit 1
  fi
done
IFS=',' read -r -a worker_subnets <<<"${QA_BUNDLE_WORKER_PUBLIC_SUBNET_IDS}"
for worker_subnet in "${worker_subnets[@]}"; do
  if [[ ! "${worker_subnet}" =~ ^subnet-[0-9a-f]+$ ]]; then
    echo "QA_BUNDLE_WORKER_PUBLIC_SUBNET_IDS contains invalid ID: ${worker_subnet}" >&2
    exit 1
  fi
done
account_id=$(aws sts get-caller-identity --query Account --output text)
raw_bucket="${PROJECT}-${ENVIRONMENT}-qa-raw-archive-${account_id}"
key_alias="alias/${PROJECT}-${ENVIRONMENT}-qa-raw-archive"
trail_name="${PROJECT}-${ENVIRONMENT}-qa-raw-data-events"
audit_bucket="${PROJECT}-${ENVIRONMENT}-qa-raw-audit-${account_id}"
bundle_bucket="${PROJECT}-${ENVIRONMENT}-qa-bundles-${account_id}"

change_type=UPDATE
if recovery_role=$(aws cloudformation describe-stacks \
  --region "${REGION}" --stack-name "${STACK}" \
  --query "Stacks[0].Outputs[?OutputKey=='QaRawArchiveRecoveryRoleArn'].OutputValue | [0]" \
  --output text 2>/dev/null); then
  require_value QaRawArchiveRecoveryRoleArn "${recovery_role}"
  if [ "${recovery_role}" = "None" ]; then
    echo "QaRawArchiveRecoveryRoleArn stack output is required" >&2
    exit 1
  fi
  if [[ ! "${recovery_role}" =~ ${role_pattern} ]]; then
    echo "QaRawArchiveRecoveryRoleArn stack output is not a role ARN" >&2
    exit 1
  fi
else
  change_type=CREATE
  recovery_role="resolved-after-create:${STACK}.QaRawArchiveRecoveryRoleArn"
fi

echo "QA raw archive proposed security binding:"
printf '  stack=%s region=%s\n' "${STACK}" "${REGION}"
printf '  app_role=%s\n' "${APP_INSTANCE_ROLE_ARN}"
printf '  recovery_principal=%s recovery_role=%s\n' "${OPS_RECOVERY_PRINCIPAL_ARN}" "${recovery_role}"
printf '  vpc=%s route_tables=%s\n' "${QA_RAW_ARCHIVE_VPC_ID}" "${QA_RAW_ARCHIVE_ROUTE_TABLE_IDS}"
printf '  raw_bucket=%s kms_alias=%s\n' "${raw_bucket}" "${key_alias}"
printf '  audit_bucket=%s trail=%s\n' "${audit_bucket}" "${trail_name}"
printf '  bundle_bucket=%s worker_desired_count=%s\n' "${bundle_bucket}" "${QA_BUNDLE_WORKER_DESIRED_COUNT:-1}"
printf '  iam_boundary=shared_ec2_instance_role_no_process_isolation\n'

parameters=$(python3 - "${PROJECT}" "${ENVIRONMENT}" "${APP_INSTANCE_ROLE_ARN}" \
  "${OPS_RECOVERY_PRINCIPAL_ARN}" "${QA_RAW_ARCHIVE_VPC_ID}" \
  "${QA_RAW_ARCHIVE_ROUTE_TABLE_IDS}" "${QA_BUNDLE_WORKER_PUBLIC_SUBNET_IDS}" \
  "${QA_BUNDLE_WORKER_IMAGE}" "${QA_BUNDLE_WORKER_DESIRED_COUNT:-1}" "${QA_BUNDLE_BROWSER_ALLOWED_ORIGIN}" <<'PY'
import json
import sys
names = (
    "ProjectName", "Environment", "AppInstanceRoleArn", "OpsRecoveryPrincipalArn",
    "VpcId", "RouteTableIds", "BundleWorkerPublicSubnetIds",
    "BundleWorkerImage", "BundleWorkerDesiredCount", "BundleBrowserAllowedOrigin",
)
print(json.dumps([
    {"ParameterKey": name, "ParameterValue": value}
    for name, value in zip(names, sys.argv[1:])
], separators=(",", ":")))
PY
)

create_change_set() {
  if [ -n "${QA_CLOUDFORMATION_SERVICE_ROLE_ARN:-}" ]; then
    aws cloudformation create-change-set \
      --region "${REGION}" --stack-name "${STACK}" --change-set-name "${CHANGE_SET}" \
      --change-set-type "${change_type}" --description "TokenKey QA raw archive security closeout" \
      --template-body "file://${TEMPLATE}" --parameters "${parameters}" \
      --capabilities CAPABILITY_IAM --role-arn "${QA_CLOUDFORMATION_SERVICE_ROLE_ARN}" --output json >/dev/null
  else
    aws cloudformation create-change-set \
      --region "${REGION}" --stack-name "${STACK}" --change-set-name "${CHANGE_SET}" \
      --change-set-type "${change_type}" --description "TokenKey QA raw archive security closeout" \
      --template-body "file://${TEMPLATE}" --parameters "${parameters}" \
      --capabilities CAPABILITY_IAM --output json >/dev/null
  fi
}
create_change_set

for _ in $(seq 1 60); do
  status=$(aws cloudformation describe-change-set \
    --region "${REGION}" --stack-name "${STACK}" --change-set-name "${CHANGE_SET}" \
    --query Status --output text)
  case "${status}" in
    CREATE_COMPLETE) break ;;
    FAILED)
      reason=$(aws cloudformation describe-change-set \
        --region "${REGION}" --stack-name "${STACK}" --change-set-name "${CHANGE_SET}" \
        --query StatusReason --output text)
      if [[ "${reason}" == *"didn't contain changes"* || "${reason}" == *"No updates"* ]]; then
        echo "No CloudFormation changes; nothing executed."
        exit 0
      fi
      echo "change set failed: ${reason}" >&2
      exit 1
      ;;
  esac
  sleep 2
done
if [ "${status}" != "CREATE_COMPLETE" ]; then
  echo "change set did not become ready: ${status}" >&2
  exit 1
fi

change_set_file="$(mktemp)"
trap 'rm -f -- "${change_set_file}"' EXIT
aws cloudformation describe-change-set \
  --region "${REGION}" \
  --stack-name "${STACK}" \
  --change-set-name "${CHANGE_SET}" \
  --output json >"${change_set_file}"
python3 - "${change_set_file}" <<'PY'
import json
import pathlib
import sys

payload = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
unsafe = []
for change in payload.get("Changes", []):
    resource = change.get("ResourceChange", {})
    action = resource.get("Action")
    replacement = resource.get("Replacement")
    if action == "Remove" or replacement in {"True", "Conditional"}:
        unsafe.append(
            f"{resource.get('LogicalResourceId', '<unknown>')} "
            f"action={action} replacement={replacement}"
        )
if unsafe:
    raise SystemExit("unsafe CloudFormation change set:\n" + "\n".join(unsafe))
PY

python3 - "${change_set_file}" <<'PY'
import json
import pathlib
import sys
payload = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
print("Action\tLogicalId\tReplacement\tType")
for change in payload.get("Changes", []):
    resource = change.get("ResourceChange", {})
    print("{}\t{}\t{}\t{}".format(
        resource.get("Action", ""),
        resource.get("LogicalResourceId", ""),
        resource.get("Replacement") or "None",
        resource.get("ResourceType", ""),
    ))
PY

if [ "${QA_RAW_ARCHIVE_CONFIRM:-}" != "yes" ]; then
  echo "Set QA_RAW_ARCHIVE_CONFIRM=yes to execute change set ${CHANGE_SET}" >&2
  exit 1
fi

aws cloudformation execute-change-set \
  --region "${REGION}" \
  --stack-name "${STACK}" \
  --change-set-name "${CHANGE_SET}"
if [ "${change_type}" = "CREATE" ]; then
  aws cloudformation wait stack-create-complete --region "${REGION}" --stack-name "${STACK}"
else
  aws cloudformation wait stack-update-complete --region "${REGION}" --stack-name "${STACK}"
fi

aws cloudformation describe-stacks \
  --region "${REGION}" \
  --stack-name "${STACK}" \
  --query 'Stacks[0].Outputs' \
  --output table
