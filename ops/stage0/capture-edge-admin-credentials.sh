#!/usr/bin/env bash
# capture-edge-admin-credentials.sh — Read the initial admin credentials that
# Stage0 AUTO_SETUP wrote into the host's logs on first boot, and persist them
# to $HOME/Codes/keys/tokenkey-<edge_id>-admin-password.txt (chmod 600).
#
# Sibling of ops/stage0/reset-edge-admin-password.sh — same credential file
# format, never prints the password.
#
# If the initial credentials cannot be found (logs already rotated),
# exits 3 — run reset-edge-admin-password.sh instead.
#
# Usage:
#   bash ops/stage0/capture-edge-admin-credentials.sh [--platform auto|ec2|lightsail] edge-<id>
#   bash ops/stage0/capture-edge-admin-credentials.sh [--platform auto|ec2|lightsail] <id>
#
# Exit codes:
#   0 — credentials captured and written
#   1 — usage / invalid edge id / keys dir missing
#   2 — AWS / SSM transport failure / resolver failure
#   3 — credentials NOT in logs; run reset-edge-admin-password.sh

set -euo pipefail

_OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${_OPS_DIR}/../.." && pwd)"
# shellcheck source=edge_admin_ssm_invocation_mi.inc.sh
source "${_OPS_DIR}/edge_admin_ssm_invocation_mi.inc.sh"

usage() {
  cat <<'EOF'
Usage:
  bash ops/stage0/capture-edge-admin-credentials.sh [--platform auto|ec2|lightsail] edge-<id>
  bash ops/stage0/capture-edge-admin-credentials.sh [--platform auto|ec2|lightsail] <id>

Lightsail probes also try /var/log/tokenkey-lightsail-bootstrap.log (EC2 bootstrap uses tokenkey-edge-bootstrap.log).

Requires: aws, jq, python3, $HOME/Codes/keys/
EOF
}

PLATFORM_PREF=auto
while [[ $# -gt 0 ]]; do
  case "$1" in
    -h | --help)
      usage
      exit 0
      ;;
    --platform=*)
      PLATFORM_PREF="${1#*=}"
      shift
      ;;
    --platform)
      PLATFORM_PREF="${2:?--platform requires a value}"
      shift 2
      ;;
    -*)
      echo "[capture-edge-admin-credentials] ERROR: unknown flag: $1" >&2
      usage >&2
      exit 1
      ;;
    *)
      break
      ;;
  esac
done

if [[ $# -ne 1 ]]; then
  usage >&2
  exit 1
fi

INPUT_TARGET="$1"
EDGE_ID="${INPUT_TARGET#edge-}"

case "${PLATFORM_PREF}" in
auto | ec2 | lightsail) ;;
*)
  echo "[capture-edge-admin-credentials] ERROR: invalid --platform: ${PLATFORM_PREF}" >&2
  exit 1
  ;;
esac

if [[ ! "$EDGE_ID" =~ ^[a-z]{2,4}[0-9]+$ ]]; then
  echo "[capture-edge-admin-credentials] ERROR: invalid edge id: $EDGE_ID" >&2
  exit 1
fi

RES_LINE="$(python3 "${_OPS_DIR}/edge_admin_resolve_target.py" "${REPO_ROOT}" "${EDGE_ID}" "${PLATFORM_PREF}" | tr -d '\r')"
IFS=$'\t' read -r RES_MODE REGION EC2_STACK <<<"${RES_LINE}"
if [[ -z "${RES_MODE:-}" ]]; then
  echo "[capture-edge-admin-credentials] ERROR: resolver returned empty output" >&2
  exit 2
fi

KEYS_DIR="$HOME/Codes/keys"
CREDENTIAL_FILE="$KEYS_DIR/tokenkey-${EDGE_ID}-admin-password.txt"
if [[ ! -d "$KEYS_DIR" ]]; then
  echo "[capture-edge-admin-credentials] ERROR: keys directory missing: $KEYS_DIR" >&2
  exit 1
fi

if [[ "${RES_MODE}" == ec2 ]]; then
  INSTANCE_ID_EC2="$(aws cloudformation describe-stacks \
    --region "$REGION" \
    --stack-name "$EC2_STACK" \
    --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue' \
    --output text)"
  if [[ -z "${INSTANCE_ID_EC2:-}" || "${INSTANCE_ID_EC2:-}" == "None" ]]; then
    echo "[capture-edge-admin-credentials] ERROR: no InstanceId in stack ${EC2_STACK} (${REGION})" >&2
    exit 2
  fi
fi

echo "[capture-edge-admin-credentials] edge_id=${EDGE_ID} platform=${RES_MODE} region=${REGION} stack=${EC2_STACK:-}"
if [[ "${RES_MODE}" == ec2 ]]; then
  echo "[capture-edge-admin-credentials] ec2_instance_id=${INSTANCE_ID_EC2}"
fi
echo "[capture-edge-admin-credentials] reading logs via SSM..."

PARAM_BODY="$(mktemp)"
cleanup_param() {
  rm -f "${PARAM_BODY}"
}
trap cleanup_param EXIT
python3 - <<'PY' >"${PARAM_BODY}"
import json
import sys

commands = [
    "set -euo pipefail",
    "sudo grep '^ADMIN_EMAIL=' /var/lib/tokenkey/.env || true",
    "sudo docker logs tokenkey 2>&1 | grep -E 'Generated admin password' || true",
    "sudo journalctl -u tokenkey.service --no-pager | grep -E 'Generated admin password' || true",
    # EC2 / historical naming
    "sudo grep -E 'Generated admin password|ADMIN_EMAIL' /var/log/tokenkey-edge-bootstrap.log || true",
    # Lightsail (deploy/aws/lightsail/generated-launch-script.sh logs to tokenkey-lightsail-bootstrap.log)
    "sudo grep -E 'Generated admin password|ADMIN_EMAIL' /var/log/tokenkey-lightsail-bootstrap.log || true",
]
json.dump({"commands": commands}, sys.stdout)
PY

COMMAND_ID=""
if [[ "${RES_MODE}" == lightsail ]]; then
  COMMAND_ID="$(aws ssm send-command \
    --region "$REGION" \
    --targets "Key=tag:EdgeId,Values=${EDGE_ID}" "Key=tag:Platform,Values=lightsail" \
    --document-name AWS-RunShellScript \
    --comment "capture initial edge admin credentials (${EDGE_ID} lightsail)" \
    --parameters "file://${PARAM_BODY}" \
    --query Command.CommandId --output text)"
  INSTANCE_ID_SSM="$(edge_admin_resolve_ssm_invocation_mi "${REGION}" "${COMMAND_ID}")"
else
  COMMAND_ID="$(aws ssm send-command \
    --region "$REGION" \
    --instance-ids "${INSTANCE_ID_EC2}" \
    --document-name AWS-RunShellScript \
    --comment "capture initial edge admin credentials (${EDGE_ID} ec2)" \
    --parameters "file://${PARAM_BODY}" \
    --query Command.CommandId --output text)"
  INSTANCE_ID_SSM="${INSTANCE_ID_EC2}"
fi

echo "[capture-edge-admin-credentials] ssm_command_id=${COMMAND_ID}"
echo "[capture-edge-admin-credentials] ssm_invocation_instance_id=${INSTANCE_ID_SSM}"

aws ssm wait command-executed \
  --region "${REGION}" --command-id "${COMMAND_ID}" \
  --instance-id "${INSTANCE_ID_SSM}" || true

STATUS="$(aws ssm get-command-invocation \
  --region "${REGION}" --command-id "${COMMAND_ID}" \
  --instance-id "${INSTANCE_ID_SSM}" --query Status --output text)"

STDOUT="$(aws ssm get-command-invocation \
  --region "${REGION}" --command-id "${COMMAND_ID}" \
  --instance-id "${INSTANCE_ID_SSM}" --query StandardOutputContent --output text)"

if [[ "${STATUS:-}" != "Success" ]]; then
  echo "[capture-edge-admin-credentials] ERROR: SSM Status=${STATUS:-empty}" >&2
  exit 2
fi

ADMIN_EMAIL=$(printf '%s\n' "$STDOUT" | grep -Eo 'ADMIN_EMAIL=[^[:space:]]+' |
  tail -n 1 | cut -d= -f2-)
ADMIN_PASSWORD=$(printf '%s\n' "$STDOUT" |
  sed -nE 's/.*Generated admin password \(one-time\): ([^[:space:]]+).*/\1/p' |
  tail -n 1)

if [[ -z "$ADMIN_EMAIL" ]] || [[ -z "$ADMIN_PASSWORD" ]]; then
  echo "[capture-edge-admin-credentials] WARN: credential not found in logs." >&2
  echo "[capture-edge-admin-credentials] Hint: try" >&2
  echo "[capture-edge-admin-credentials]   bash ops/stage0/reset-edge-admin-password.sh --platform ${RES_MODE} ${EDGE_ID}" >&2
  exit 3
fi

umask 077
{
  printf 'email=%s\n' "${ADMIN_EMAIL}"
  printf 'password=%s\n' "${ADMIN_PASSWORD}"
} >"${CREDENTIAL_FILE}"
chmod 600 "${CREDENTIAL_FILE}"

unset STDOUT ADMIN_PASSWORD

echo ""
echo "[capture-edge-admin-credentials] ok: credentials saved (password not printed)"
echo "EDGE_ID=${EDGE_ID}"
echo "SSM_ROUTING=${RES_MODE}"
echo "REGION=${REGION}"
[[ "${RES_MODE}" == ec2 ]] && echo "EC2_STACK=${EC2_STACK}"
echo "CREDENTIAL_FILE=${CREDENTIAL_FILE}"
