#!/usr/bin/env bash
# Phase-1 stub: resolve + print hcloud create plan. Refuses paid create unless
# --confirm-paid is set (Phase-2+). Never flips DNS.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
RESOLVER="${ROOT}/deploy/hetzner/resolve-edge-hetzner-target.py"

EDGE_ID=""
CONFIRM_PAID=0
ALLOW_PLANNED=0

usage() {
  cat <<'EOF'
Usage: provision-edge.sh --edge-id <id> [--allow-planned] [--confirm-paid]

  Default: dry-run only (resolve matrix + print hcloud plan).
  --confirm-paid: actually call hcloud server create (requires HCLOUD_TOKEN).
  DNS / mirror cutover is NEVER done by this script.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --edge-id) EDGE_ID="${2:-}"; shift 2 ;;
    --allow-planned) ALLOW_PLANNED=1; shift ;;
    --confirm-paid) CONFIRM_PAID=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
done

if [[ -z "$EDGE_ID" ]]; then
  echo "provision-edge: --edge-id is required" >&2
  exit 2
fi

ARGS=(--edge-id "$EDGE_ID")
if [[ "$ALLOW_PLANNED" -eq 1 ]]; then
  ARGS+=(--allow-planned)
fi

RESOLVED="$(python3 "$RESOLVER" "${ARGS[@]}")"
get_field() {
  printf '%s\n' "$RESOLVED" | awk -F= -v k="$1" '$1==k { print substr($0, index($0,"=")+1); exit }'
}

PLATFORM="$(get_field platform)"
INSTANCE_NAME="$(get_field instance_name)"
LOCATION="$(get_field location)"
SERVER_TYPE="$(get_field server_type)"
ARCHITECTURE="$(get_field architecture)"
IMAGE="$(get_field image)"
DEPLOYABLE="$(get_field deployable)"
STAGING_DOMAIN="$(get_field staging_domain)"
SSM_PREFIX="$(get_field ssm_prefix)"
SSH_KEY_NAME="$(get_field ssh_key_name)"
EDGE_OUT="$(get_field edge_id)"

echo "platform=${PLATFORM}"
echo "edge_id=${EDGE_OUT}"
echo "instance_name=${INSTANCE_NAME}"
echo "location=${LOCATION}"
echo "server_type=${SERVER_TYPE}"
echo "architecture=${ARCHITECTURE}"
echo "deployable=${DEPLOYABLE}"
echo "staging_domain=${STAGING_DOMAIN}"
echo "ssm_prefix=${SSM_PREFIX}"

echo "plan: hcloud server create --name ${INSTANCE_NAME} --type ${SERVER_TYPE} --location ${LOCATION} --image ${IMAGE} --ssh-key ${SSH_KEY_NAME} --label tokenkey.io/role=edge --label tokenkey.io/edge-id=${EDGE_OUT} --label tokenkey.io/platform=hetzner --start-after-create=true"

if [[ "$CONFIRM_PAID" -ne 1 ]]; then
  echo "dry-run only (pass --confirm-paid to create). no-web-impact"
  exit 0
fi

if [[ -z "${HCLOUD_TOKEN:-}" ]]; then
  echo "provision-edge: HCLOUD_TOKEN unset" >&2
  exit 1
fi

if [[ "$DEPLOYABLE" != "true" && "$ALLOW_PLANNED" -ne 1 ]]; then
  echo "provision-edge: refuse paid create for non-deployable without --allow-planned" >&2
  exit 1
fi

echo "WARNING: creating paid Hetzner server ${INSTANCE_NAME} (no DNS change)"
hcloud server create \
  --name "${INSTANCE_NAME}" \
  --type "${SERVER_TYPE}" \
  --location "${LOCATION}" \
  --image "${IMAGE}" \
  --ssh-key "${SSH_KEY_NAME}" \
  --label "tokenkey.io/role=edge" \
  --label "tokenkey.io/edge-id=${EDGE_OUT}" \
  --label "tokenkey.io/platform=hetzner" \
  --start-after-create=true

echo "create submitted; complete §17 E0–E4 on staging before any DNS cutover"
