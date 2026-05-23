#!/usr/bin/env bash
# Provision or refresh a TokenKey Edge Lightsail instance.
# Called from deploy-edge-lightsail-stage0.yml (provision operation).
set -euo pipefail

EDGE_ID="${1:-${EDGE_ID:-}}"
TAG="${2:-${TAG:-}}"
LIGHTSAIL_REGION="${3:-${LIGHTSAIL_REGION:-}}"
INSTANCE_NAME="${4:-${INSTANCE_NAME:-}}"
STATIC_IP_NAME="${5:-${STATIC_IP_NAME:-}}"
AVAILABILITY_ZONE="${6:-${AVAILABILITY_ZONE:-}}"
BUNDLE_ID="${7:-${BUNDLE_ID:-}}"
BLUEPRINT_ID="${8:-${BLUEPRINT_ID:-}}"
API_DOMAIN="${9:-${API_DOMAIN:-}}"
ACME_EMAIL="${10:-${ACME_EMAIL:-}}"
MAIN_GATEWAY_ALLOWED_CIDR="${11:-${MAIN_GATEWAY_ALLOWED_CIDR:-34.194.234.88/32}}"
GHCR_OWNER="${12:-${GHCR_OWNER:-}}"
GHCR_PAT_SSM_NAME="${13:-${GHCR_PAT_SSM_NAME:-}}"
SSM_PREFIX="${14:-${SSM_PREFIX:-}}"
ACTIVATION_NAME="${15:-${ACTIVATION_NAME:-tokenkey-ls-${EDGE_ID}}}"

if [[ -z "$EDGE_ID" || -z "$TAG" || -z "$LIGHTSAIL_REGION" || -z "$INSTANCE_NAME" ]]; then
  echo "provision-edge: missing required args" >&2
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
bash "${REPO_ROOT}/deploy/aws/lightsail/render-bootstrap.sh"

TOKENKEY_IMAGE="ghcr.io/${GHCR_OWNER}/sub2api:${TAG}"
GHCR_PULL_USER="${GHCR_OWNER}"

echo "creating SSM hybrid activation name=${ACTIVATION_NAME} region=${LIGHTSAIL_REGION}"
activation_json="$(aws ssm create-activation \
  --region "$LIGHTSAIL_REGION" \
  --description "tokenkey lightsail edge ${EDGE_ID}" \
  --default-instance-name "${INSTANCE_NAME}" \
  --registration-limit 1 \
  --tags "Key=Project,Value=tokenkey" "Key=EdgeId,Value=${EDGE_ID}" "Key=Platform,Value=lightsail")"
activation_id="$(echo "$activation_json" | jq -r '.ActivationId')"
activation_code="$(echo "$activation_json" | jq -r '.ActivationCode')"
if [[ -z "$activation_id" || "$activation_id" == "null" ]]; then
  echo "::error::SSM create-activation failed" >&2
  exit 1
fi

launch_env_file="$(mktemp)"
trap 'rm -f "$launch_env_file"' EXIT
cat >"$launch_env_file" <<EOF
export EDGE_ID='${EDGE_ID}'
export API_DOMAIN='${API_DOMAIN}'
export ACME_EMAIL='${ACME_EMAIL}'
export MAIN_GATEWAY_ALLOWED_CIDR='${MAIN_GATEWAY_ALLOWED_CIDR}'
export TOKENKEY_IMAGE='${TOKENKEY_IMAGE}'
export GHCR_PULL_USER='${GHCR_PULL_USER}'
export GHCR_PAT_SSM_NAME='${GHCR_PAT_SSM_NAME}'
export LIGHTSAIL_REGION='${LIGHTSAIL_REGION}'
export SSM_ACTIVATION_ID='${activation_id}'
export SSM_ACTIVATION_CODE='${activation_code}'
export ADMIN_EMAIL='admin@${API_DOMAIN}'
export TZ_VALUE='UTC'
EOF

launch_body="${REPO_ROOT}/deploy/aws/lightsail/generated-launch-script.sh"
user_data_file="$(mktemp)"
trap 'rm -f "$launch_env_file" "$user_data_file"' EXIT
{
  cat "$launch_env_file"
  echo
  cat "$launch_body"
} >"$user_data_file"

existing="$(aws lightsail get-instance --region "$LIGHTSAIL_REGION" --instance-name "$INSTANCE_NAME" 2>/dev/null || true)"
if [[ -n "$existing" ]]; then
  echo "instance ${INSTANCE_NAME} already exists; stopping for recreate"
  aws lightsail stop-instance --region "$LIGHTSAIL_REGION" --instance-name "$INSTANCE_NAME" || true
  deadline=$(( $(date +%s) + 300 ))
  while [[ $(date +%s) -lt $deadline ]]; do
    state="$(aws lightsail get-instance --region "$LIGHTSAIL_REGION" --instance-name "$INSTANCE_NAME" \
      --query 'instance.state.name' --output text 2>/dev/null || echo unknown)"
    [[ "$state" == "stopped" ]] && break
    sleep 5
  done
  aws lightsail delete-instance --region "$LIGHTSAIL_REGION" --instance-name "$INSTANCE_NAME" || true
fi

user_data_payload="$(cat "$user_data_file")"
echo "creating lightsail instance ${INSTANCE_NAME} bundle=${BUNDLE_ID} blueprint=${BLUEPRINT_ID}"
aws lightsail create-instances \
  --region "$LIGHTSAIL_REGION" \
  --instance-names "$INSTANCE_NAME" \
  --availability-zone "$AVAILABILITY_ZONE" \
  --blueprint-id "$BLUEPRINT_ID" \
  --bundle-id "$BUNDLE_ID" \
  --user-data "$user_data_payload"

deadline=$(( $(date +%s) + 600 ))
while [[ $(date +%s) -lt $deadline ]]; do
  state="$(aws lightsail get-instance --region "$LIGHTSAIL_REGION" --instance-name "$INSTANCE_NAME" \
    --query 'instance.state.name' --output text 2>/dev/null || echo pending)"
  echo "instance state=${state}"
  [[ "$state" == "running" ]] && break
  sleep 10
done

if ! aws lightsail get-static-ip --region "$LIGHTSAIL_REGION" --static-ip-name "$STATIC_IP_NAME" >/dev/null 2>&1; then
  aws lightsail allocate-static-ip --region "$LIGHTSAIL_REGION" --static-ip-name "$STATIC_IP_NAME"
fi
aws lightsail attach-static-ip \
  --region "$LIGHTSAIL_REGION" \
  --static-ip-name "$STATIC_IP_NAME" \
  --instance-name "$INSTANCE_NAME"

public_ip="$(aws lightsail get-static-ip --region "$LIGHTSAIL_REGION" --static-ip-name "$STATIC_IP_NAME" \
  --query 'staticIp.ipAddress' --output text)"

echo "waiting for SSM managed instance registration (up to 10m)"
managed_id=""
deadline=$(( $(date +%s) + 600 ))
while [[ $(date +%s) -lt $deadline ]]; do
  managed_id="$(aws ssm describe-instance-information \
    --region "$LIGHTSAIL_REGION" \
    --filters "Key=tag:EdgeId,Values=${EDGE_ID}" "Key=ResourceType,Values=ManagedInstance" \
    --query 'InstanceInformationList[0].InstanceId' --output text 2>/dev/null || true)"
  if [[ -n "$managed_id" && "$managed_id" != "None" && "$managed_id" != "null" ]]; then
    break
  fi
  managed_id="$(aws ssm describe-instance-information \
    --region "$LIGHTSAIL_REGION" \
    --filters "Key=ComputerName,Values=${INSTANCE_NAME}" \
    --query 'InstanceInformationList[0].InstanceId' --output text 2>/dev/null || true)"
  if [[ -n "$managed_id" && "$managed_id" != "None" && "$managed_id" != "null" ]]; then
    break
  fi
  sleep 15
done

if [[ -z "$managed_id" || "$managed_id" == "None" || "$managed_id" == "null" ]]; then
  echo "::error::SSM managed instance not registered within timeout; check /var/log/tokenkey-lightsail-bootstrap.log via Lightsail browser SSH"
  exit 1
fi

put_param() {
  local name="$1" value="$2"
  aws ssm put-parameter --region "$LIGHTSAIL_REGION" --name "$name" --type String \
    --value "$value" --overwrite >/dev/null
}

put_param "${SSM_PREFIX}/instance_name" "$INSTANCE_NAME"
put_param "${SSM_PREFIX}/static_ip_name" "$STATIC_IP_NAME"
put_param "${SSM_PREFIX}/public_ip" "$public_ip"
put_param "${SSM_PREFIX}/ssm_managed_instance_id" "$managed_id"
put_param "${SSM_PREFIX}/tokenkey_image" "$TOKENKEY_IMAGE"

api_url="https://${API_DOMAIN}"
echo "provision complete edge=${EDGE_ID} instance=${INSTANCE_NAME} managed_id=${managed_id} ip=${public_ip} api=${api_url}"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    echo "managed_instance_id=${managed_id}"
    echo "public_ip=${public_ip}"
    echo "api_url=${api_url}"
    echo "instance_name=${INSTANCE_NAME}"
  } >>"$GITHUB_OUTPUT"
fi
