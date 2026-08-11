#!/usr/bin/env bash
# Reconcile live prod QA raw archive bucket policy with repository IAM contract.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DEPLOY_SCRIPT="${ROOT}/ops/qa/deploy_qa_raw_archive_cfn.sh"
VERIFY_SCRIPT="${ROOT}/ops/qa/verify_raw_archive_iam_contract.py"
STACK="${QA_RAW_ARCHIVE_STACK:-tokenkey-prod-qa-raw-archive}"
REGION="${AWS_REGION:-us-east-1}"
STAGE0_STACK="${QA_STAGE0_STACK:-tokenkey-prod-stage0}"

require_value() {
  local name="$1" value="${2:-}"
  if [ -z "${value}" ]; then
    echo "${name} is required" >&2
    exit 1
  fi
}

resolve_stack_parameter() {
  local stack_name="$1" key="$2"
  aws cloudformation describe-stacks \
    --region "${REGION}" \
    --stack-name "${stack_name}" \
    --query "Stacks[0].Parameters[?ParameterKey=='${key}'].ParameterValue | [0]" \
    --output text
}

resolve_stack_resource() {
  local stack_name="$1" logical_id="$2"
  aws cloudformation describe-stack-resources \
    --region "${REGION}" \
    --stack-name "${stack_name}" \
    --logical-resource-id "${logical_id}" \
    --query "StackResources[0].PhysicalResourceId" \
    --output text
}

resolve_vpc_and_route_tables() {
  local vpc_id route_table_ids
  vpc_id="$(aws cloudformation describe-stacks \
    --region "${REGION}" \
    --stack-name "${STAGE0_STACK}" \
    --query "Stacks[0].Outputs[?OutputKey=='VpcId'].OutputValue | [0]" \
    --output text 2>/dev/null || true)"
  if [ -z "${vpc_id}" ] || [ "${vpc_id}" = "None" ]; then
    vpc_id="$(resolve_stack_resource "${STAGE0_STACK}" "VPC" 2>/dev/null || true)"
  fi
  if [ -z "${vpc_id}" ] || [ "${vpc_id}" = "None" ]; then
    vpc_id="$(aws ec2 describe-vpcs \
      --region "${REGION}" \
      --filters "Name=tag:Name,Values=*tokenkey*prod*" \
      --query 'Vpcs[0].VpcId' \
      --output text)"
  fi
  route_table_ids="$(resolve_stack_resource "${STAGE0_STACK}" "PublicRouteTable" 2>/dev/null || true)"
  if [ -z "${route_table_ids}" ] || [ "${route_table_ids}" = "None" ]; then
    route_table_ids="$(aws ec2 describe-route-tables \
      --region "${REGION}" \
      --filters "Name=vpc-id,Values=${vpc_id}" "Name=tag:Name,Values=*public*rt*" \
      --query 'RouteTables[0].RouteTableId' \
      --output text)"
  fi
  require_value QA_RAW_ARCHIVE_VPC_ID "${vpc_id}"
  require_value QA_RAW_ARCHIVE_ROUTE_TABLE_IDS "${route_table_ids}"
  export QA_RAW_ARCHIVE_VPC_ID="${vpc_id}"
  export QA_RAW_ARCHIVE_ROUTE_TABLE_IDS="${route_table_ids}"
}

main() {
  export APP_INSTANCE_ROLE_ARN
  export OPS_RECOVERY_PRINCIPAL_ARN
  APP_INSTANCE_ROLE_ARN="$(resolve_stack_parameter "${STACK}" AppInstanceRoleArn)"
  OPS_RECOVERY_PRINCIPAL_ARN="$(resolve_stack_parameter "${STACK}" OpsRecoveryPrincipalArn)"
  require_value APP_INSTANCE_ROLE_ARN "${APP_INSTANCE_ROLE_ARN}"
  require_value OPS_RECOVERY_PRINCIPAL_ARN "${OPS_RECOVERY_PRINCIPAL_ARN}"
  resolve_vpc_and_route_tables

  if [ "${1:-}" = "--check" ]; then
    python3 "${VERIFY_SCRIPT}" --json
    exit $?
  fi

  if [ "${QA_RAW_ARCHIVE_CONFIRM:-}" != "yes" ]; then
    echo "Set QA_RAW_ARCHIVE_CONFIRM=yes to apply CloudFormation IAM contract reconciliation" >&2
    exit 1
  fi

  bash "${DEPLOY_SCRIPT}"
  python3 "${VERIFY_SCRIPT}" --json
}

main "$@"
