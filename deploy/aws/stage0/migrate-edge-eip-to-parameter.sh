#!/usr/bin/env bash
# One-time per-stack migration from EIP-as-CloudFormation-resource (old shape:
# ElasticIP + EIPAssociation with Retain) to EIP-as-stack-parameter (new shape:
# EipAllocationId parameter + EIPAssociation only, no ElasticIP resource).
#
# After this migration, every future IP rotation for the migrated stack runs
# via deploy-edge-stage0.yml operation=rotate_egress_ip — a pure CFN UpdateStack
# with no detach, no IMPORT, no drift class.
#
# Why this script exists at all:
#   The legacy template had Retain on ElasticIP, so the EIP survives template
#   updates that remove the resource. We pass the EIP's existing allocation-id
#   to the new template as EipAllocationId. CFN reuses the same physical EIP;
#   the public IP does not change. After this completes, the stack matches the
#   new template shape and routine rotation works.
#
# Why it is per-stack and operator-driven:
#   This is a deliberate, irreversible-by-template change to live prod and edge
#   stacks. The script REFUSES to loop across stacks; the operator must invoke
#   it once per edge_id in a planned window.
#
# Usage:
#   bash deploy/aws/stage0/migrate-edge-eip-to-parameter.sh <edge_id> [--apply]
#
#     edge_id  — uk1 | us1 | fra1 | sg1 (must exist in edge-targets.json)
#     --apply  — actually run aws cloudformation deploy. Without it the script
#                does a read-only dry-run that prints the planned parameters
#                and exits 0 — safe to run any time.
#
# Exit codes:
#   0 — dry-run succeeded, or --apply succeeded
#   1 — operator-recoverable error (missing AWS creds, stack missing, wrong region, EIP already in param form, etc.)
#   2 — bad input
#   3 — AWS error during apply

set -euo pipefail

EDGE_ID="${1:-}"
APPLY="${2:-}"

if [ -z "$EDGE_ID" ]; then
  echo "usage: $0 <edge_id> [--apply]" >&2
  exit 2
fi

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
TARGETS="${REPO_ROOT}/deploy/aws/stage0/edge-targets.json"
TEMPLATE="${REPO_ROOT}/deploy/aws/cloudformation/stage0-edge-ec2.yaml"
OIDC_STACK_NAME="${AWS_OIDC_STACK_NAME:-tokenkey-cicd-oidc}"
OIDC_STACK_REGION="${AWS_OIDC_STACK_REGION:-us-east-1}"

for f in "$TARGETS" "$TEMPLATE"; do
  if [ ! -f "$f" ]; then echo "missing $f" >&2; exit 1; fi
done
if ! command -v jq >/dev/null 2>&1; then echo "jq required" >&2; exit 1; fi
if ! command -v aws >/dev/null 2>&1; then echo "aws CLI required" >&2; exit 1; fi

target=$(jq -r --arg id "$EDGE_ID" '.targets[$id] // empty' "$TARGETS")
if [ -z "$target" ] || [ "$target" = "null" ]; then
  echo "edge_id ${EDGE_ID} not found in ${TARGETS}" >&2
  exit 1
fi

REGION=$(jq -r .region <<<"$target")
STACK_NAME=$(jq -r .stack <<<"$target")

# Refuse to migrate the production gateway accidentally — this script is for
# edges only. The prod-stage0 stack has different blast radius (active client
# connections) and a different migration story.
case "$STACK_NAME" in
  tokenkey-edge-*) ;;
  *)
    echo "migrate-edge-eip-to-parameter: refusing — stack ${STACK_NAME} is not tokenkey-edge-*" >&2
    exit 1
    ;;
esac

# Resolve the CFN execution role from the OIDC stack (same lookup as
# operation=provision in deploy-edge-stage0.yml).
case "$EDGE_ID" in
  uk1)  ROLE_OUTPUT_KEY=EdgeUk1CloudFormationExecutionRoleArn ;;
  fra1) ROLE_OUTPUT_KEY=EdgeFra1CloudFormationExecutionRoleArn ;;
  us1)  ROLE_OUTPUT_KEY=EdgeUs1CloudFormationExecutionRoleArn ;;
  *)
    echo "edge_id ${EDGE_ID} has no CFN execution role in deploy-edge-stage0.yml; extend cicd-oidc first" >&2
    exit 1
    ;;
esac

CFN_ROLE=$(aws cloudformation describe-stacks --region "$OIDC_STACK_REGION" \
  --stack-name "$OIDC_STACK_NAME" \
  --query "Stacks[0].Outputs[?OutputKey=='${ROLE_OUTPUT_KEY}'].OutputValue | [0]" --output text 2>/dev/null || echo "")
if [ -z "${CFN_ROLE}" ] || [ "$CFN_ROLE" = "None" ]; then
  echo "could not resolve ${ROLE_OUTPUT_KEY} from cicd-oidc stack" >&2
  exit 1
fi

echo "== migrate-edge-eip-to-parameter =="
echo "edge_id:  ${EDGE_ID}"
echo "region:   ${REGION}"
echo "stack:    ${STACK_NAME}"
echo "cfn role: ${CFN_ROLE}"

# Confirm stack exists.
stack_status=$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK_NAME" \
  --query 'Stacks[0].StackStatus' --output text 2>/dev/null || echo MISSING)
if [ "$stack_status" = "MISSING" ]; then
  echo "stack ${STACK_NAME} does not exist in ${REGION}" >&2
  exit 1
fi
echo "stack status: ${stack_status}"

# Already migrated?
existing_alloc_param=$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK_NAME" \
  --query "Stacks[0].Parameters[?ParameterKey=='EipAllocationId'].ParameterValue | [0]" --output text 2>/dev/null || echo None)
if [ -n "$existing_alloc_param" ] && [ "$existing_alloc_param" != "None" ]; then
  echo "stack already migrated (EipAllocationId=${existing_alloc_param}); operation=rotate_egress_ip is now the canonical IP-change path"
  exit 0
fi

# Resolve current live allocation by tag on the EIP. This matches scripts/edge-ip-status.sh.
EIP_NAME_TAG="tokenkey-${EDGE_ID}-eip"
existing=$(aws ec2 describe-addresses --region "$REGION" \
  --filters "Name=tag:Name,Values=${EIP_NAME_TAG}" \
  --query 'Addresses[?AssociationId!=null] | [0]' --output json 2>/dev/null || echo '{}')
ALLOC_ID=$(jq -r '.AllocationId // empty' <<<"$existing")
PUBLIC_IP=$(jq -r '.PublicIp // empty' <<<"$existing")
INSTANCE_ID=$(jq -r '.InstanceId // empty' <<<"$existing")

if [ -z "$ALLOC_ID" ]; then
  echo "could not find a live EIP for ${EDGE_ID} (queried tag:Name=${EIP_NAME_TAG} in ${REGION})" >&2
  echo "if the EIP has a different Name tag, set it to ${EIP_NAME_TAG} first via aws ec2 create-tags" >&2
  exit 1
fi
echo "current live EIP: ${PUBLIC_IP} (${ALLOC_ID}) associated to ${INSTANCE_ID}"

# Re-tag the EIP with Project=tokenkey so ec2:ReleaseAddress (post-rotation
# cleanup) is permitted by the new IAM (Sid: ReleaseTaggedTokenkeyCandidateEip,
# Condition aws:ResourceTag/Project=tokenkey). Idempotent; safe to re-run.
echo "ensuring EIP carries Project=tokenkey tag for future rotation IAM…"
if [ "$APPLY" = "--apply" ]; then
  aws ec2 create-tags --region "$REGION" --resources "$ALLOC_ID" \
    --tags Key=Project,Value=tokenkey >/dev/null
fi

if [ "$APPLY" != "--apply" ]; then
  echo
  echo "DRY RUN — no changes made."
  echo "To apply: $0 ${EDGE_ID} --apply"
  echo
  echo "When --apply runs, the following will happen, in order:"
  echo "  1. aws ec2 create-tags Project=tokenkey on ${ALLOC_ID}"
  echo "  2. aws cloudformation deploy --stack-name ${STACK_NAME} \\"
  echo "       --template-file deploy/aws/cloudformation/stage0-edge-ec2.yaml \\"
  echo "       --role-arn ${CFN_ROLE} --capabilities CAPABILITY_IAM \\"
  echo "       --parameter-overrides EipAllocationId=${ALLOC_ID}"
  echo "  3. Post-update: verify public IP still ${PUBLIC_IP} (no EIP swap)"
  echo "  4. Post-update: verify ssm PingStatus=Online for ${INSTANCE_ID}"
  exit 0
fi

echo
echo "applying CFN update — this removes the ElasticIP resource (Retain keeps the live EIP alive) and binds it back via the new EipAllocationId parameter…"
aws cloudformation deploy \
  --stack-name "$STACK_NAME" \
  --region "$REGION" \
  --template-file "$TEMPLATE" \
  --role-arn "$CFN_ROLE" \
  --capabilities CAPABILITY_IAM \
  --no-fail-on-empty-changeset \
  --parameter-overrides EipAllocationId="$ALLOC_ID"

# Post-apply invariants — these are the same gates rotate_egress_ip enforces.
after_ip=$(aws ec2 describe-addresses --region "$REGION" --allocation-ids "$ALLOC_ID" \
  --query 'Addresses[0].PublicIp' --output text)
if [ "$after_ip" != "$PUBLIC_IP" ]; then
  echo "::error::EIP public IP changed during migration (${PUBLIC_IP} → ${after_ip}); investigate before continuing" >&2
  exit 3
fi
echo "verified public IP unchanged: ${PUBLIC_IP}"

bash "$(dirname "$0")/verify-ssm-online.sh" "$INSTANCE_ID" "$REGION"

echo
echo "✅ ${EDGE_ID} migrated. Future IP rotations: gh workflow run deploy-edge-stage0.yml \\"
echo "     -f edge_id=${EDGE_ID} -f operation=rotate_egress_ip \\"
echo "     -f confirm_stack=${STACK_NAME} -f rotation_reason='<short reason>'"
