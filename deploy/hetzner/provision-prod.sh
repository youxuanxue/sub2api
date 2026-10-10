#!/usr/bin/env bash
# Provision TokenKey prod on Hetzner: Volume + server + SSM Hybrid + compose.
# Staging only — never flips formal DNS (api.tokenkey.dev) or deployable=true.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
RESOLVER="${ROOT}/deploy/hetzner/resolve-prod-hetzner-target.py"
RENDER="${ROOT}/deploy/hetzner/render-prod-bootstrap.sh"

CONFIRM_PAID=0
ALLOW_PLANNED=0
TAG="${TAG:-}"
ACME_EMAIL="${ACME_EMAIL:-}"
MAIN_GATEWAY_ALLOWED_CIDR="${MAIN_GATEWAY_ALLOWED_CIDR:-}"
GHCR_OWNER="${GHCR_OWNER:-}"
GHCR_PAT_SSM_NAME="${GHCR_PAT_SSM_NAME:-}"
ALLOW_SECRET_GENERATE="${ALLOW_SECRET_GENERATE:-true}"
SSM_REGION_OVERRIDE="${SSM_REGION:-}"

usage() {
  cat <<'EOF'
Usage: provision-prod.sh [--allow-planned] [--confirm-paid] [--tag X.Y.Z]

  Default: dry-run (resolve + print hcloud plan including Volume).
  --confirm-paid: create Volume (if missing) + server with user-data.
    Requires HCLOUD_TOKEN, aws creds, --tag, ACME_EMAIL,
    MAIN_GATEWAY_ALLOWED_CIDR, GHCR_OWNER.
  Formal DNS / P1 restore / freeze-write is NEVER done by this script.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --allow-planned) ALLOW_PLANNED=1; shift ;;
    --confirm-paid) CONFIRM_PAID=1; shift ;;
    --tag) TAG="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
done

ARGS=()
if [[ "$ALLOW_PLANNED" -eq 1 ]]; then
  ARGS+=(--allow-planned)
fi

RESOLVED="$(python3 "$RESOLVER" "${ARGS[@]}")"
get_field() {
  printf '%s\n' "$RESOLVED" | awk -F= -v k="$1" '$1==k { print substr($0, index($0,"=")+1); exit }'
}

INSTANCE_NAME="$(get_field instance_name)"
LOCATION="$(get_field location)"
SERVER_TYPE="$(get_field server_type)"
ARCHITECTURE="$(get_field architecture)"
IMAGE="$(get_field image)"
DEPLOYABLE="$(get_field deployable)"
STAGING_DOMAIN="$(get_field staging_domain)"
SSM_PREFIX="$(get_field ssm_prefix)"
SSH_KEY_NAME="$(get_field ssh_key_name)"
SWAP_GIB="$(get_field swap_gib)"
SSM_HYBRID_ROLE_NAME="$(get_field ssm_hybrid_role_name)"
VOLUME_NAME="$(get_field volume_name)"
VOLUME_SIZE_GB="$(get_field volume_size_gb)"
VOLUME_MOUNT="$(get_field volume_mount)"
SSM_REGION="${SSM_REGION_OVERRIDE:-$(get_field ssm_region)}"
[[ -n "$SSM_REGION" ]] || SSM_REGION="eu-west-2"

echo "role=prod"
echo "platform=hetzner"
echo "instance_name=${INSTANCE_NAME}"
echo "location=${LOCATION}"
echo "server_type=${SERVER_TYPE}"
echo "architecture=${ARCHITECTURE}"
echo "deployable=${DEPLOYABLE}"
echo "staging_domain=${STAGING_DOMAIN}"
echo "ssm_prefix=${SSM_PREFIX}"
echo "ssm_region=${SSM_REGION}"
echo "ssm_hybrid_role_name=${SSM_HYBRID_ROLE_NAME}"
echo "volume_name=${VOLUME_NAME}"
echo "volume_size_gb=${VOLUME_SIZE_GB}"
echo "volume_mount=${VOLUME_MOUNT}"

echo "plan: hcloud volume create --name ${VOLUME_NAME} --size ${VOLUME_SIZE_GB} --location ${LOCATION} (if missing)"
echo "plan: hcloud server create --name ${INSTANCE_NAME} --type ${SERVER_TYPE} --location ${LOCATION} --image ${IMAGE} --ssh-key ${SSH_KEY_NAME} --start-after-create=false --user-data-from-file <bootstrap>"
echo "plan: hcloud volume attach ${VOLUME_NAME} --server ${INSTANCE_NAME} && hcloud server poweron ${INSTANCE_NAME}"

if [[ "$CONFIRM_PAID" -ne 1 ]]; then
  echo "dry-run only (pass --confirm-paid --tag X.Y.Z to create). no-web-impact"
  exit 0
fi

if [[ -z "${HCLOUD_TOKEN:-}" ]]; then
  echo "provision-prod: HCLOUD_TOKEN unset" >&2
  exit 1
fi
if [[ "$DEPLOYABLE" != "true" && "$ALLOW_PLANNED" -ne 1 ]]; then
  echo "provision-prod: refuse paid create for non-deployable without --allow-planned" >&2
  exit 1
fi
if [[ -z "$TAG" ]]; then
  echo "provision-prod: --tag is required for --confirm-paid" >&2
  exit 1
fi
if [[ -z "$ACME_EMAIL" || -z "$MAIN_GATEWAY_ALLOWED_CIDR" || -z "$GHCR_OWNER" ]]; then
  echo "provision-prod: ACME_EMAIL, MAIN_GATEWAY_ALLOWED_CIDR, GHCR_OWNER required for --confirm-paid" >&2
  exit 1
fi
if [[ "$SSM_HYBRID_ROLE_NAME" != "tokenkey-hetzner-ssm-hybrid-prod" ]]; then
  echo "provision-prod: unexpected ssm_hybrid_role_name=${SSM_HYBRID_ROLE_NAME}" >&2
  exit 1
fi
if ! command -v aws >/dev/null 2>&1; then
  echo "provision-prod: aws CLI required for SSM Hybrid activation" >&2
  exit 1
fi
if ! command -v hcloud >/dev/null 2>&1; then
  echo "provision-prod: hcloud CLI required" >&2
  exit 1
fi

if hcloud server describe "${INSTANCE_NAME}" >/dev/null 2>&1; then
  echo "provision-prod: server ${INSTANCE_NAME} already exists; refuse recreate" >&2
  exit 1
fi

bash "$RENDER"
LAUNCH_BODY="${ROOT}/deploy/hetzner/generated-prod-user-data.sh"
[[ -f "$LAUNCH_BODY" ]] || { echo "provision-prod: missing ${LAUNCH_BODY}" >&2; exit 1; }

TOKENKEY_IMAGE="ghcr.io/${GHCR_OWNER}/sub2api:${TAG}"
ACTIVATION_NAME="tokenkey-hz-prod"

# Volume first (idempotent create)
volume_id=""
if hcloud volume describe "${VOLUME_NAME}" >/dev/null 2>&1; then
  volume_id="$(hcloud volume describe "${VOLUME_NAME}" -o json | jq -r '.id')"
  echo "reusing volume ${VOLUME_NAME} id=${volume_id}"
  server_ref="$(hcloud volume describe "${VOLUME_NAME}" -o json | jq -r '.server // empty')"
  if [[ -n "$server_ref" && "$server_ref" != "null" ]]; then
    echo "provision-prod: volume ${VOLUME_NAME} already attached to server ${server_ref}" >&2
    exit 1
  fi
else
  echo "creating volume ${VOLUME_NAME} size=${VOLUME_SIZE_GB}GiB location=${LOCATION}"
  hcloud volume create \
    --name "${VOLUME_NAME}" \
    --size "${VOLUME_SIZE_GB}" \
    --location "${LOCATION}" \
    --label "tokenkey.io/role=prod" \
    --label "tokenkey.io/platform=hetzner"
  volume_id="$(hcloud volume describe "${VOLUME_NAME}" -o json | jq -r '.id')"
fi
if [[ -z "$volume_id" || "$volume_id" == "null" ]]; then
  echo "provision-prod: could not resolve volume id for ${VOLUME_NAME}" >&2
  exit 1
fi
echo "volume_id=${volume_id}"

echo "creating SSM hybrid activation name=${ACTIVATION_NAME} region=${SSM_REGION} iam-role=${SSM_HYBRID_ROLE_NAME}"
activation_json="$(aws ssm create-activation \
  --region "$SSM_REGION" \
  --iam-role "$SSM_HYBRID_ROLE_NAME" \
  --description "tokenkey hetzner prod staging" \
  --default-instance-name "${INSTANCE_NAME}" \
  --registration-limit 1 \
  --tags "Key=Project,Value=tokenkey" "Key=Role,Value=prod" "Key=Platform,Value=hetzner")"
activation_id="$(echo "$activation_json" | jq -r '.ActivationId')"
activation_code="$(echo "$activation_json" | jq -r '.ActivationCode')"
if [[ -z "$activation_id" || "$activation_id" == "null" ]]; then
  echo "::error::SSM create-activation failed" >&2
  exit 1
fi

user_data_file="$(mktemp)"
trap 'rm -f "$user_data_file"' EXIT
{
  cat <<EOF
#!/bin/bash
# tokenkey hetzner prod user-data — generated by provision-prod.sh
set -euo pipefail
export INSTANCE_NAME='${INSTANCE_NAME}'
export API_DOMAIN='${STAGING_DOMAIN}'
export ACME_EMAIL='${ACME_EMAIL}'
export MAIN_GATEWAY_ALLOWED_CIDR='${MAIN_GATEWAY_ALLOWED_CIDR}'
export TOKENKEY_IMAGE='${TOKENKEY_IMAGE}'
export GHCR_PULL_USER='${GHCR_OWNER}'
export GHCR_PAT_SSM_NAME='${GHCR_PAT_SSM_NAME}'
export SSM_REGION='${SSM_REGION}'
export SSM_ACTIVATION_ID='${activation_id}'
export SSM_ACTIVATION_CODE='${activation_code}'
export ADMIN_EMAIL='admin@${STAGING_DOMAIN}'
export TZ_VALUE='UTC'
export SWAP_SIZE_GIB='${SWAP_GIB}'
export ALLOW_SECRET_GENERATE='${ALLOW_SECRET_GENERATE}'
export VOLUME_ID='${volume_id}'
export VOLUME_MOUNT='${VOLUME_MOUNT}'

EOF
  if [[ "$(head -n1 "$LAUNCH_BODY")" == '#!'* ]]; then
    tail -n +2 "$LAUNCH_BODY"
  else
    cat "$LAUNCH_BODY"
  fi
} >"$user_data_file"

echo "WARNING: creating paid Hetzner prod server ${INSTANCE_NAME} (staging=${STAGING_DOMAIN}; no formal DNS)"
hcloud server create \
  --name "${INSTANCE_NAME}" \
  --type "${SERVER_TYPE}" \
  --location "${LOCATION}" \
  --image "${IMAGE}" \
  --ssh-key "${SSH_KEY_NAME}" \
  --user-data-from-file "${user_data_file}" \
  --label "tokenkey.io/role=prod" \
  --label "tokenkey.io/platform=hetzner" \
  --start-after-create=false

echo "attaching volume ${VOLUME_NAME} → ${INSTANCE_NAME}"
hcloud volume attach "${VOLUME_NAME}" --server "${INSTANCE_NAME}"
echo "powering on ${INSTANCE_NAME}"
hcloud server poweron "${INSTANCE_NAME}"

public_ip="$(hcloud server describe "${INSTANCE_NAME}" -o json | jq -r '.public_net.ipv4.ip // empty')"
if [[ -z "$public_ip" ]]; then
  echo "provision-prod: could not read public IPv4 for ${INSTANCE_NAME}" >&2
  exit 1
fi
echo "public_ip=${public_ip}"
echo "::notice::Point staging DNS ${STAGING_DOMAIN} A → ${public_ip} before expecting ACME / E1 HTTPS smoke."

echo "waiting for SSM managed instance (activation_id=${activation_id}, up to 15m)"
managed_id=""
deadline=$(( $(date +%s) + 900 ))
while [[ $(date +%s) -lt $deadline ]]; do
  managed_id="$(aws ssm describe-instance-information \
    --region "$SSM_REGION" \
    --filters "Key=ActivationIds,Values=${activation_id}" \
    --query 'InstanceInformationList[0].InstanceId' --output text 2>/dev/null || true)"
  if [[ -n "$managed_id" && "$managed_id" != "None" && "$managed_id" != "null" ]]; then
    break
  fi
  sleep 15
done
if [[ -z "$managed_id" || "$managed_id" == "None" || "$managed_id" == "null" ]]; then
  echo "::error::SSM managed instance not registered; check /var/log/tokenkey-hetzner-bootstrap.log" >&2
  exit 1
fi
echo "ssm_managed_instance_id=${managed_id}"

echo "waiting for docker compose stack health on ${managed_id} (up to 12m)"
stack_check_json="$(mktemp)"
trap 'rm -f "$user_data_file" "$stack_check_json"' EXIT
cat > "$stack_check_json" <<'JSON'
{"commands":["findmnt -n /var/lib/tokenkey >/dev/null && docker ps --filter name=tokenkey-postgres --filter health=healthy --format '{{.Names}}' | grep -qx tokenkey-postgres && docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -tAc \"SELECT to_regclass('public.settings')\" 2>/dev/null | grep -qx settings && echo STACK_READY"]}
JSON
stack_ready=false
stack_deadline=$(( $(date +%s) + 720 ))
while [[ $(date +%s) -lt $stack_deadline ]]; do
  chk_id="$(aws ssm send-command --region "$SSM_REGION" --instance-ids "$managed_id" \
    --document-name AWS-RunShellScript --parameters "file://${stack_check_json}" \
    --query 'Command.CommandId' --output text 2>/dev/null || true)"
  if [[ -n "$chk_id" && "$chk_id" != "None" ]]; then
    sleep 8
    chk_out="$(aws ssm get-command-invocation --region "$SSM_REGION" \
      --command-id "$chk_id" --instance-id "$managed_id" \
      --query 'StandardOutputContent' --output text 2>/dev/null || true)"
    if grep -q STACK_READY <<<"$chk_out"; then stack_ready=true; break; fi
  fi
  sleep 12
done
if [[ "$stack_ready" != true ]]; then
  echo "::error::docker compose stack / volume mount not ready" >&2
  exit 1
fi
echo "docker compose stack healthy on ${managed_id} (volume mounted)"

put_param() {
  aws ssm put-parameter --region "$SSM_REGION" --name "$1" --type String --value "$2" --overwrite >/dev/null
}
put_param "${SSM_PREFIX}/instance_name" "$INSTANCE_NAME"
put_param "${SSM_PREFIX}/public_ip" "$public_ip"
put_param "${SSM_PREFIX}/ssm_managed_instance_id" "$managed_id"
put_param "${SSM_PREFIX}/tokenkey_image" "$TOKENKEY_IMAGE"
put_param "${SSM_PREFIX}/staging_domain" "$STAGING_DOMAIN"
put_param "${SSM_PREFIX}/volume_name" "$VOLUME_NAME"
put_param "${SSM_PREFIX}/volume_id" "$volume_id"

echo "provision complete role=prod instance=${INSTANCE_NAME} managed_id=${managed_id} ip=${public_ip} volume=${VOLUME_NAME} staging=https://${STAGING_DOMAIN}"
echo "next: set staging DNS A, then E0/E1. Do NOT formal DNS, P1 restore, or freeze-write yet."
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    echo "managed_instance_id=${managed_id}"
    echo "public_ip=${public_ip}"
    echo "staging_domain=${STAGING_DOMAIN}"
    echo "instance_name=${INSTANCE_NAME}"
    echo "volume_id=${volume_id}"
  } >>"$GITHUB_OUTPUT"
fi
