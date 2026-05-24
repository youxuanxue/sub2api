#!/usr/bin/env bash
set -euo pipefail

_OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${_OPS_DIR}/../.." && pwd)"
# shellcheck source=edge_admin_ssm_invocation_mi.inc.sh
source "${_OPS_DIR}/edge_admin_ssm_invocation_mi.inc.sh"

usage() {
  cat <<'EOF'
Usage:
  bash ops/stage0/reset-edge-admin-password.sh [--platform auto|ec2|lightsail] edge-<id>
  bash ops/stage0/reset-edge-admin-password.sh [--platform auto|ec2|lightsail] <id>

Examples:
  bash ops/stage0/reset-edge-admin-password.sh uk1              # auto: Lightsail when deployable=true in lightsail matrix
  bash ops/stage0/reset-edge-admin-password.sh --platform ec2 fra1
  bash ops/stage0/reset-edge-admin-password.sh --platform lightsail uk1

Behavior:
  - Uses deploy/aws/lightsail/edge-targets-lightsail.json vs deploy/aws/stage0/edge-targets.json:
      * Platform "auto": Lightsail region + Tag SSM targets when ls target exists and deployable=true;
                        otherwise EC2 region + InstanceId from CloudFormation Outputs.
      * EC2/Lightsail: force that resolution path via --platform.
  - Reads ADMIN_EMAIL from /var/lib/tokenkey/.env on the instance (same stack layout EC2 vs Lightsail).
  - Resets admin password via PostgreSQL (pgcrypto bcrypt).
  - Saves email/password to $HOME/Codes/keys/tokenkey-<id>-admin-password.txt
  - Never prints the new password.

Requires: aws cli, openssl, jq, python3 (and $HOME/Codes/keys/)
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
    --)
      shift
      break
      ;;
    -*)
      echo "[error] unknown flag: $1" >&2
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
  echo "[error] invalid --platform: ${PLATFORM_PREF} (use auto|ec2|lightsail)" >&2
  exit 1
  ;;
esac

if [[ ! "$EDGE_ID" =~ ^[a-z]{2,4}[0-9]+$ ]]; then
  echo "[error] edge id must match ^[a-z]{2,4}[0-9]+$: $EDGE_ID" >&2
  exit 1
fi

RES_LINE="$(python3 "${_OPS_DIR}/edge_admin_resolve_target.py" "${REPO_ROOT}" "${EDGE_ID}" "${PLATFORM_PREF}" | tr -d '\r')"
IFS=$'\t' read -r RES_MODE REGION EC2_STACK <<<"$RES_LINE"

if [[ -z "${RES_MODE:-}" ]]; then
  echo "[error] resolver returned empty output" >&2
  exit 1
fi

if [[ "$RES_MODE" == ec2 ]]; then
  INSTANCE_ID_EC2=""
  INSTANCE_ID_EC2="$(aws cloudformation describe-stacks \
    --region "$REGION" \
    --stack-name "$EC2_STACK" \
    --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue' \
    --output text)"
  if [[ -z "${INSTANCE_ID_EC2:-}" || "${INSTANCE_ID_EC2:-}" == "None" ]]; then
    echo "[error] could not resolve InstanceId from stack ${EC2_STACK} in ${REGION}" >&2
    exit 1
  fi
fi

NEW_PASSWORD="$(openssl rand -hex 16)"
KEYS_DIR="$HOME/Codes/keys"
CREDENTIAL_FILE="$KEYS_DIR/tokenkey-${EDGE_ID}-admin-password.txt"

if [[ ! -d "$KEYS_DIR" ]]; then
  echo "[error] keys directory not found: $KEYS_DIR" >&2
  exit 1
fi

echo "[info] edge_id=${EDGE_ID} platform=${RES_MODE} region=${REGION} stack=${EC2_STACK:-<lightsail-tags>}"
if [[ "$RES_MODE" == ec2 ]]; then
  echo "[info] ec2_instance_id=${INSTANCE_ID_EC2}"
fi
echo "[info] resetting admin password via SSM..."

COMMANDS_JSON="$(NEW_PASSWORD_ESC="$NEW_PASSWORD" python3 <<'PY'
import json
import os

new_password = os.environ["NEW_PASSWORD_ESC"]
commands = [
    "set -euo pipefail",
    "ADMIN_EMAIL=$(sudo grep '^ADMIN_EMAIL=' /var/lib/tokenkey/.env | cut -d= -f2)",
    "if [ -z \"${ADMIN_EMAIL:-}\" ]; then echo '[error] ADMIN_EMAIL not found in /var/lib/tokenkey/.env' >&2; exit 1; fi",
    "sudo docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -c 'CREATE EXTENSION IF NOT EXISTS pgcrypto;' >/dev/null",
    f"sudo docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 -v new_password={new_password} -v admin_email=\"${{ADMIN_EMAIL}}\" -c \"UPDATE users SET password_hash = crypt(:'new_password', gen_salt('bf', 10)), updated_at = NOW() WHERE email = :'admin_email' AND role = 'admin';\"",
    "UPDATED_COUNT=$(sudo docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 -v admin_email=\"${ADMIN_EMAIL}\" -tA -c \"SELECT COUNT(1) FROM users WHERE email = :'admin_email' AND role = 'admin';\" | tr -d '[:space:]')",
    "if [ \"${UPDATED_COUNT}\" != \"1\" ]; then echo \"[error] expected exactly 1 admin row for ${ADMIN_EMAIL}, got ${UPDATED_COUNT}\" >&2; exit 1; fi",
    "echo ADMIN_EMAIL=$ADMIN_EMAIL",
    "echo RESET_OK=1",
]
print(json.dumps(commands))
PY
)"

PARAM_BODY="$(mktemp)"
cleanup_param() {
  rm -f "${PARAM_BODY}"
}
trap cleanup_param EXIT
printf '{"commands":%s}\n' "${COMMANDS_JSON}" >"${PARAM_BODY}"

COMMAND_ID=""
if [[ "$RES_MODE" == lightsail ]]; then
  COMMAND_ID="$(aws ssm send-command \
    --region "$REGION" \
    --targets "Key=tag:EdgeId,Values=${EDGE_ID}" "Key=tag:Platform,Values=lightsail" \
    --document-name AWS-RunShellScript \
    --comment "reset edge admin password (${EDGE_ID} lightsail)" \
    --parameters "file://${PARAM_BODY}" \
    --query 'Command.CommandId' \
    --output text)"
  INSTANCE_ID_SSM=""
  INSTANCE_ID_SSM="$(edge_admin_resolve_ssm_invocation_mi "$REGION" "$COMMAND_ID")"
else
  COMMAND_ID="$(aws ssm send-command \
    --region "$REGION" \
    --instance-ids "$INSTANCE_ID_EC2" \
    --document-name AWS-RunShellScript \
    --comment "reset edge admin password (${EDGE_ID} ec2)" \
    --parameters "file://${PARAM_BODY}" \
    --query 'Command.CommandId' \
    --output text)"
  INSTANCE_ID_SSM="$INSTANCE_ID_EC2"
fi

echo "[info] ssm_command_id=${COMMAND_ID}"
echo "[info] ssm_invocation_instance_id=${INSTANCE_ID_SSM}"

if ! aws ssm wait command-executed \
  --region "$REGION" \
  --command-id "$COMMAND_ID" \
  --instance-id "$INSTANCE_ID_SSM"; then
  echo "[warn] waiter reported non-success; fetching invocation details..." >&2
fi

STATUS="$(aws ssm get-command-invocation \
  --region "$REGION" \
  --command-id "$COMMAND_ID" \
  --instance-id "$INSTANCE_ID_SSM" \
  --query 'Status' \
  --output text)"

STDOUT_CONTENT="$(aws ssm get-command-invocation \
  --region "$REGION" \
  --command-id "$COMMAND_ID" \
  --instance-id "$INSTANCE_ID_SSM" \
  --query 'StandardOutputContent' \
  --output text)"

STDERR_CONTENT="$(aws ssm get-command-invocation \
  --region "$REGION" \
  --command-id "$COMMAND_ID" \
  --instance-id "$INSTANCE_ID_SSM" \
  --query 'StandardErrorContent' \
  --output text)"

if [[ "${STATUS:-}" != "Success" ]]; then
  echo "[error] SSM command failed: status=${STATUS:-empty}" >&2
  if [[ -n "${STDOUT_CONTENT:-}" ]]; then
    echo "[error] stdout:" >&2
    printf '%s\n' "$STDOUT_CONTENT" >&2
  fi
  if [[ -n "${STDERR_CONTENT:-}" ]]; then
    echo "[error] stderr:" >&2
    printf '%s\n' "$STDERR_CONTENT" >&2
  fi
  exit 1
fi

ADMIN_EMAIL_LINE="$(printf '%s\n' "$STDOUT_CONTENT" | grep '^ADMIN_EMAIL=' | tail -n 1 || true)"
ADMIN_EMAIL="${ADMIN_EMAIL_LINE#ADMIN_EMAIL=}"

if [[ -z "$ADMIN_EMAIL" || "$ADMIN_EMAIL" == "$ADMIN_EMAIL_LINE" ]]; then
  echo "[error] could not parse ADMIN_EMAIL from SSM output" >&2
  printf '%s\n' "$STDOUT_CONTENT" >&2
  exit 1
fi

umask 077
{
  printf 'email=%s\n' "$ADMIN_EMAIL"
  printf 'password=%s\n' "$NEW_PASSWORD"
} >"${CREDENTIAL_FILE}"
chmod 600 "${CREDENTIAL_FILE}"

echo ""
echo "[ok] edge admin password reset complete"
echo "EDGE_ID=${EDGE_ID}"
echo "SSM_ROUTING=${RES_MODE}"
echo "REGION=${REGION}"
[[ "$RES_MODE" == ec2 ]] && echo "EC2_STACK=${EC2_STACK}" && echo "EC2_INSTANCE_ID=${INSTANCE_ID_EC2}"
[[ "$RES_MODE" == lightsail ]] && echo "SSM_TAGS=EdgeId=${EDGE_ID},Platform=lightsail"
echo "SSM_PRIMARY_ID=${INSTANCE_ID_SSM}"
echo "CREDENTIAL_FILE=${CREDENTIAL_FILE}"
echo "[ok] admin credentials saved; password was not printed"
