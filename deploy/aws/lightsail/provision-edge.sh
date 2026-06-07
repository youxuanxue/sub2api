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
SSM_HYBRID_ROLE_NAME="${16:-${SSM_HYBRID_ROLE_NAME:-tokenkey-lightsail-ssm-hybrid}}"
SWAP_GIB="${SWAP_GIB:-2}"

if [[ -z "$EDGE_ID" || -z "$TAG" || -z "$LIGHTSAIL_REGION" || -z "$INSTANCE_NAME" ]]; then
  echo "provision-edge: missing required args" >&2
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
bash "${REPO_ROOT}/deploy/aws/lightsail/render-bootstrap.sh"

TOKENKEY_IMAGE="ghcr.io/${GHCR_OWNER}/sub2api:${TAG}"
GHCR_PULL_USER="${GHCR_OWNER}"

echo "creating SSM hybrid activation name=${ACTIVATION_NAME} region=${LIGHTSAIL_REGION} iam-role=${SSM_HYBRID_ROLE_NAME}"
# --iam-role is required: AWS embeds the role into the activation so registered
# managed instances (mi-*) can call back into SSM. The role is created by
# cicd-oidc-lightsail-addon.yaml (one-time per account).
activation_json="$(aws ssm create-activation \
  --region "$LIGHTSAIL_REGION" \
  --iam-role "$SSM_HYBRID_ROLE_NAME" \
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
export INSTANCE_NAME='${INSTANCE_NAME}'
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
export SWAP_SIZE_GIB='${SWAP_GIB}'
EOF

launch_body="${REPO_ROOT}/deploy/aws/lightsail/generated-launch-script.sh"
user_data_file="$(mktemp)"
trap 'rm -f "$launch_env_file" "$user_data_file"' EXIT
{
  cat "$launch_env_file"
  echo
  cat "$launch_body"
} >"$user_data_file"

# preflight-allow: swallow — get-instance returns non-zero when instance is absent,
# which is the success path here. We then branch on whether stdout is empty.
existing="$(aws lightsail get-instance --region "$LIGHTSAIL_REGION" --instance-name "$INSTANCE_NAME" 2>/dev/null || true)"
if [[ -n "$existing" ]]; then
  if [[ "${RECREATE:-false}" != "true" ]]; then
    echo "::error::instance ${INSTANCE_NAME} already exists in region ${LIGHTSAIL_REGION}." >&2
    echo "  Set workflow input recreate=true to DESTROY + recreate (Static IP and SSM activation will be re-issued)." >&2
    echo "  For tag changes use operation=upgrade instead — it preserves the instance and Static IP." >&2
    exit 1
  fi
  echo "::warning::RECREATE=true — destroying existing instance ${INSTANCE_NAME}"
  aws lightsail stop-instance --region "$LIGHTSAIL_REGION" --instance-name "$INSTANCE_NAME" >/dev/null
  deadline=$(( $(date +%s) + 300 ))
  while [[ $(date +%s) -lt $deadline ]]; do
    state="$(aws lightsail get-instance --region "$LIGHTSAIL_REGION" --instance-name "$INSTANCE_NAME" \
      --query 'instance.state.name' --output text 2>/dev/null || echo unknown)"
    [[ "$state" == "stopped" ]] && break
    sleep 5
  done
  aws lightsail delete-instance --region "$LIGHTSAIL_REGION" --instance-name "$INSTANCE_NAME" >/dev/null
  # Deregister stale SSM managed-instance(s) for this edge. delete-instance drops
  # the Lightsail instance but its SSM managed-instance (mi-*) registration
  # lingers Online and stays tagged EdgeId=<id>; the post-recreate mi-* resolution
  # (tag/ComputerName fallback) could then pick the zombie and the workflow polls
  # a dead instance. Deregister every currently-tagged mi-* so the fresh instance
  # below registers the only one. Requires ssm:DeregisterManagedInstance
  # (granted by cicd-oidc-lightsail-addon).
  for stale_mi in $(aws ssm describe-instance-information --region "$LIGHTSAIL_REGION" \
      --filters "Key=tag:EdgeId,Values=${EDGE_ID}" \
      --query 'InstanceInformationList[].InstanceId' --output text 2>/dev/null || true); do
    [[ -z "$stale_mi" || "$stale_mi" == "None" ]] && continue
    echo "deregistering stale SSM managed-instance ${stale_mi} (EdgeId=${EDGE_ID})"
    aws ssm deregister-managed-instance --region "$LIGHTSAIL_REGION" --instance-id "$stale_mi" >/dev/null 2>&1 || true
  done
  # Detach only — keep the allocated Static IP (and its address) for re-attach below.
  # Releasing would force allocate-static-ip to mint a new address, breaking
  # pre-provisioned / DNS-pinned IPs (edge-us2/us3/us4 Lightsail rollout).
  if aws lightsail get-static-ip --region "$LIGHTSAIL_REGION" --static-ip-name "$STATIC_IP_NAME" >/dev/null 2>&1; then
    aws lightsail detach-static-ip --region "$LIGHTSAIL_REGION" --static-ip-name "$STATIC_IP_NAME" >/dev/null 2>&1 || true
  fi
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

# Instance-level firewall (Lightsail "Networking"): distinct from EC2 SGs / Caddy allowlists.
# Default blueprints commonly expose SSH only — without 80/443, public curls time out (~2m) despite correct DNS/static IP.
echo "opening Lightsail public ports 80 and 443 (IPv4) on ${INSTANCE_NAME}"
for port in 80 443; do
  if aws lightsail open-instance-public-ports \
    --region "$LIGHTSAIL_REGION" \
    --instance-name "$INSTANCE_NAME" \
    --port-info "fromPort=${port},toPort=${port},protocol=tcp,cidrs=0.0.0.0/0" >/dev/null; then
    echo "lightsail firewall: TCP ${port} allowed from 0.0.0.0/0"
  else
    echo "::notice::open-instance-public-ports tcp/${port} failed or rule already exists — check:" >&2
    echo "       aws lightsail get-instance-port-states --region ${LIGHTSAIL_REGION} --instance-name ${INSTANCE_NAME}" >&2
  fi
done

public_ip="$(aws lightsail get-static-ip --region "$LIGHTSAIL_REGION" --static-ip-name "$STATIC_IP_NAME" \
  --query 'staticIp.ipAddress' --output text)"

echo "waiting for SSM managed instance registration (activation_id=${activation_id}, up to 15m)"
echo "::notice::describe-instance-information must use EITHER tag filters OR non-tag filters — never combine (AWS API)."
managed_id=""
deadline=$(( $(date +%s) + 900 ))
while [[ $(date +%s) -lt $deadline ]]; do
  # Primary: ActivationIds uniquely identifies the Hybrid registration minted above
  # (registration_limit=1). This avoids unreliable ComputerName fallback — AL2023 nodes
  # often report dhcp hostnames unrelated to Lightsail instance_name.
  managed_id="$(aws ssm describe-instance-information \
    --region "$LIGHTSAIL_REGION" \
    --filters "Key=ActivationIds,Values=${activation_id}" \
    --query 'InstanceInformationList[0].InstanceId' --output text 2>/dev/null || true)"
  if [[ -n "$managed_id" && "$managed_id" != "None" && "$managed_id" != "null" ]]; then
    break
  fi
  # Secondary: activation tags propagate to MI but cannot combine with ResourceType.
  managed_id="$(aws ssm describe-instance-information \
    --region "$LIGHTSAIL_REGION" \
    --filters "Key=tag:EdgeId,Values=${EDGE_ID}" \
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
  echo "::notice::describe-activations (registration count / expiration):"
  aws ssm describe-activations --region "$LIGHTSAIL_REGION" --no-paginate \
    --filters "FilterKey=ActivationIds,FilterValues=${activation_id}" || true
  exit 1
fi

# SSM registration (mi-*) only proves the agent is up — the docker compose stack
# (postgres + app) is still pulling/starting via bootstrap user-data, and the
# app's healthcheck is /health/live (liveness), which goes green BEFORE the app
# finishes DB migrations. Without this gate the workflow's next step (Sync Feishu
# alert config does `docker exec tokenkey-postgres psql ... INSERT INTO settings`)
# races and fails with either "No such container: tokenkey-postgres" (stack not
# up) or 'relation "settings" does not exist' (migrations not done yet). Gate on
# postgres healthy AND the settings table existing (= app migrations have run) —
# the exact precondition for the Feishu sync — before declaring provision done.
echo "waiting for docker compose stack health (postgres healthy + settings table) on ${managed_id} (up to 6m)"
stack_check_json="$(mktemp)"
# IMPORTANT: AWS-RunShellScript concatenates the `commands` array into ONE shell
# script with NO `set -e`, so a non-zero intermediate command does NOT stop the
# script — only the LAST command's effect is observed. If the two health checks
# were separate array elements, `echo STACK_READY` would run unconditionally and
# the gate would pass on the first poll regardless of actual health. Chain both
# checks with `&&` into a single command so STACK_READY is emitted ONLY when
# postgres is healthy AND the settings table exists.
cat > "$stack_check_json" <<'JSON'
{"commands":["docker ps --filter name=tokenkey-postgres --filter health=healthy --format '{{.Names}}' | grep -qx tokenkey-postgres && docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -tAc \"SELECT to_regclass('public.settings')\" 2>/dev/null | grep -qx settings && echo STACK_READY"]}
JSON
stack_ready=false
stack_deadline=$(( $(date +%s) + 360 ))
while [[ $(date +%s) -lt $stack_deadline ]]; do
  chk_id="$(aws ssm send-command --region "$LIGHTSAIL_REGION" --instance-ids "$managed_id" \
    --document-name AWS-RunShellScript --parameters "file://${stack_check_json}" \
    --query 'Command.CommandId' --output text 2>/dev/null || true)"
  if [[ -n "$chk_id" && "$chk_id" != "None" ]]; then
    sleep 8
    chk_out="$(aws ssm get-command-invocation --region "$LIGHTSAIL_REGION" \
      --command-id "$chk_id" --instance-id "$managed_id" \
      --query 'StandardOutputContent' --output text 2>/dev/null || true)"
    if grep -q STACK_READY <<<"$chk_out"; then stack_ready=true; break; fi
  fi
  sleep 12
done
rm -f "$stack_check_json"
if [[ "$stack_ready" != true ]]; then
  echo "::error::docker compose stack did not reach ready (tokenkey-postgres healthy + settings table migrated) within timeout; check /var/log/tokenkey-lightsail-bootstrap.log"
  exit 1
fi
echo "docker compose stack healthy on ${managed_id}"

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
