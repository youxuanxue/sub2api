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
require_value OPS_RECOVERY_ROLE_ARN "${OPS_RECOVERY_ROLE_ARN:-}"
require_value QA_RAW_ARCHIVE_VPC_ID "${QA_RAW_ARCHIVE_VPC_ID:-}"
require_value QA_RAW_ARCHIVE_ROUTE_TABLE_IDS "${QA_RAW_ARCHIVE_ROUTE_TABLE_IDS:-}"
require_value QA_RAW_ARCHIVE_AUDIT_BUCKET "${QA_RAW_ARCHIVE_AUDIT_BUCKET:-}"

role_pattern='^arn:[a-z0-9-]+:iam::[0-9]{12}:role/.+$'
if [[ ! "${APP_INSTANCE_ROLE_ARN}" =~ ${role_pattern} ]]; then
  echo "APP_INSTANCE_ROLE_ARN is not a role ARN" >&2
  exit 1
fi
if [[ ! "${OPS_RECOVERY_ROLE_ARN}" =~ ${role_pattern} ]]; then
  echo "OPS_RECOVERY_ROLE_ARN is not a role ARN" >&2
  exit 1
fi
if [[ ! "${QA_RAW_ARCHIVE_VPC_ID}" =~ ^vpc-[0-9a-f]+$ ]]; then
  echo "QA_RAW_ARCHIVE_VPC_ID is not a VPC ID" >&2
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
if [[ ! "${QA_RAW_ARCHIVE_AUDIT_BUCKET}" =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]]; then
  echo "QA_RAW_ARCHIVE_AUDIT_BUCKET is not an S3 bucket name" >&2
  exit 1
fi

account_id=$(aws sts get-caller-identity --query Account --output text)
raw_bucket="${PROJECT}-${ENVIRONMENT}-qa-raw-archive-${account_id}"
key_alias="alias/${PROJECT}-${ENVIRONMENT}-qa-raw-archive"
trail_name="${PROJECT}-${ENVIRONMENT}-qa-raw-data-events"

echo "QA raw archive proposed security binding:"
printf '  stack=%s region=%s\n' "${STACK}" "${REGION}"
printf '  app_role=%s\n' "${APP_INSTANCE_ROLE_ARN}"
printf '  recovery_role=%s\n' "${OPS_RECOVERY_ROLE_ARN}"
printf '  vpc=%s route_tables=%s\n' "${QA_RAW_ARCHIVE_VPC_ID}" "${QA_RAW_ARCHIVE_ROUTE_TABLE_IDS}"
printf '  raw_bucket=%s kms_alias=%s\n' "${raw_bucket}" "${key_alias}"
printf '  audit_bucket=%s trail=%s\n' "${QA_RAW_ARCHIVE_AUDIT_BUCKET}" "${trail_name}"

change_type=UPDATE
if ! aws cloudformation describe-stacks --region "${REGION}" --stack-name "${STACK}" >/dev/null 2>&1; then
  change_type=CREATE
fi

parameters=$(python3 - "${PROJECT}" "${ENVIRONMENT}" "${APP_INSTANCE_ROLE_ARN}" \
  "${OPS_RECOVERY_ROLE_ARN}" "${QA_RAW_ARCHIVE_VPC_ID}" \
  "${QA_RAW_ARCHIVE_ROUTE_TABLE_IDS}" "${QA_RAW_ARCHIVE_AUDIT_BUCKET}" <<'PY'
import json
import sys
names = (
    "ProjectName", "Environment", "AppInstanceRoleArn", "OpsRecoveryRoleArn",
    "VpcId", "RouteTableIds", "AuditLogBucketName",
)
print(json.dumps([
    {"ParameterKey": name, "ParameterValue": value}
    for name, value in zip(names, sys.argv[1:])
], separators=(",", ":")))
PY
)

aws cloudformation create-change-set \
  --region "${REGION}" \
  --stack-name "${STACK}" \
  --change-set-name "${CHANGE_SET}" \
  --change-set-type "${change_type}" \
  --description "TokenKey QA raw archive security closeout" \
  --template-body "file://${TEMPLATE}" \
  --parameters "${parameters}" \
  --output json >/dev/null

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

aws cloudformation describe-change-set \
  --region "${REGION}" \
  --stack-name "${STACK}" \
  --change-set-name "${CHANGE_SET}" \
  --query '{Status:Status,Changes:Changes[*].ResourceChange.{Action:Action,LogicalId:LogicalResourceId,Type:ResourceType,Replacement:Replacement}}' \
  --output table

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
