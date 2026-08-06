#!/usr/bin/env bash
# Deploy the standalone QA raw archive CloudFormation stack (Phase 2 step 1).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMPLATE="${ROOT}/deploy/aws/cloudformation/stage0-qa-raw-archive.yaml"
STACK="${QA_RAW_ARCHIVE_STACK:-tokenkey-prod-qa-raw-archive}"
# Prod Stage0 lives in us-east-1; raw archive must colocate with the app instance.
REGION="${AWS_REGION:-us-east-1}"

if [ -z "${APP_INSTANCE_ROLE_ARN:-}" ]; then
  echo "APP_INSTANCE_ROLE_ARN is required (prod InstanceRole ARN)" >&2
  exit 1
fi

args=(
  cloudformation deploy
  --region "${REGION}"
  --stack-name "${STACK}"
  --template-file "${TEMPLATE}"
  --parameter-overrides
  "AppInstanceRoleArn=${APP_INSTANCE_ROLE_ARN}"
)
if [ -n "${OPS_RECOVERY_ROLE_ARN:-}" ]; then
  args+=("OpsRecoveryRoleArn=${OPS_RECOVERY_ROLE_ARN}")
fi
if [ "${QA_RAW_ARCHIVE_CONFIRM:-}" != "yes" ]; then
  echo "Set QA_RAW_ARCHIVE_CONFIRM=yes to deploy ${STACK} in ${REGION}" >&2
  exit 1
fi

aws "${args[@]}"

aws cloudformation describe-stacks \
  --region "${REGION}" \
  --stack-name "${STACK}" \
  --query 'Stacks[0].Outputs' \
  --output table
