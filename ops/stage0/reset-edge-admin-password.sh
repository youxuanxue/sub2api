#!/usr/bin/env bash
set -euo pipefail

_OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${_OPS_DIR}/../.." && pwd)"

usage() {
  cat <<'EOF'
Usage:
  bash ops/stage0/reset-edge-admin-password.sh [--platform auto|ec2|lightsail|hetzner] edge-<id>
  bash ops/stage0/reset-edge-admin-password.sh [--platform auto|ec2|lightsail|hetzner] <id>
  bash ops/stage0/reset-edge-admin-password.sh prod

Examples:
  bash ops/stage0/reset-edge-admin-password.sh uk1              # auto: live Hetzner target
  bash ops/stage0/reset-edge-admin-password.sh --platform lightsail uk1
  bash ops/stage0/reset-edge-admin-password.sh prod            # prod main gateway (live hcloud prod; identity/region from shared resolver)

Behavior:
  - Target is an edge id (e.g. uk1, us4) or the literal "prod" (prod Stage0 gateway).
  - Uses the shared prod/edge identity resolvers:
      * Platform "auto": live Hetzner target + SSM Hybrid identity.
      * "lightsail": explicit Lightsail resolution, including planned targets.
      * "ec2": rejected for edges (retired).
      * "prod": shared prod resolver; default Hetzner, explicit ec2 selects standby.
  - Reads ADMIN_EMAIL from /var/lib/tokenkey/.env on the instance (shared runtime layout).
  - Resets admin password via PostgreSQL (pgcrypto bcrypt).
  - Saves email/password to $HOME/Codes/keys/tokenkey-<id>-admin-password.txt (prod -> tokenkey-prod-admin-password.txt)
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
auto | ec2 | lightsail | hetzner) ;;
*)
  echo "[error] invalid --platform: ${PLATFORM_PREF} (use auto|ec2|lightsail|hetzner)" >&2
  exit 1
  ;;
esac

if [[ "$EDGE_ID" != "prod" && ! "$EDGE_ID" =~ ^[a-z]{2,4}[0-9]+$ ]]; then
  echo "[error] target must be 'prod' or an edge id matching ^[a-z]{2,4}[0-9]+$: $EDGE_ID" >&2
  exit 1
fi

RES_LINE="$(python3 "${_OPS_DIR}/edge_admin_resolve_target.py" "${REPO_ROOT}" "${EDGE_ID}" "${PLATFORM_PREF}" | tr -d '\r')"
IFS=$'\t' read -r RES_MODE REGION INSTANCE_ID_SSM <<<"$RES_LINE"

if [[ -z "${RES_MODE:-}" ]]; then
  echo "[error] resolver returned empty output" >&2
  exit 1
fi

NEW_PASSWORD="$(openssl rand -hex 16)"
KEYS_DIR="$HOME/Codes/keys"
CREDENTIAL_FILE="$KEYS_DIR/tokenkey-${EDGE_ID}-admin-password.txt"

if [[ ! -d "$KEYS_DIR" ]]; then
  echo "[error] keys directory not found: $KEYS_DIR" >&2
  exit 1
fi

echo "[info] edge_id=${EDGE_ID} platform=${RES_MODE} region=${REGION}"
echo "[info] resetting admin password via SSM..."

COMMANDS_JSON="$(NEW_PASSWORD_ESC="$NEW_PASSWORD" python3 <<'PY'
import json
import os

new_password = os.environ["NEW_PASSWORD_ESC"]
commands = [
    "set -euo pipefail",
    "ADMIN_EMAIL=$(sudo grep '^ADMIN_EMAIL=' /var/lib/tokenkey/.env | cut -d= -f2)",
    "if [ -z \"${ADMIN_EMAIL:-}\" ]; then echo '[error] ADMIN_EMAIL not found in /var/lib/tokenkey/.env' >&2; exit 1; fi",
    f"NEW_PASSWORD='{new_password}'",
    "sudo docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -c 'CREATE EXTENSION IF NOT EXISTS pgcrypto;' >/dev/null",
    "sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 <<EOSQL",
    "UPDATE users SET password_hash = crypt('${NEW_PASSWORD}', gen_salt('bf', 10)), updated_at = NOW() WHERE email = '${ADMIN_EMAIL}' AND role = 'admin' AND deleted_at IS NULL;",
    "EOSQL",
    "UPDATED_COUNT=$(sudo docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 -tA -c \"SELECT COUNT(1) FROM users WHERE email = '${ADMIN_EMAIL}' AND role = 'admin' AND deleted_at IS NULL;\")",
    "UPDATED_COUNT=$(printf '%s' \"${UPDATED_COUNT}\" | tr -d '[:space:]')",
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
python3 "${_OPS_DIR}/ssm_wrap_bash_commands.py" "${PARAM_BODY}"

COMMAND_ID=""
COMMAND_ID="$(aws ssm send-command \
  --region "$REGION" \
  --instance-ids "$INSTANCE_ID_SSM" \
  --document-name AWS-RunShellScript \
  --comment "reset edge admin password (${EDGE_ID} ${RES_MODE})" \
  --parameters "file://${PARAM_BODY}" \
  --query 'Command.CommandId' \
  --output text)"

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
echo "SSM_INSTANCE_ID=${INSTANCE_ID_SSM}"
echo "SSM_PRIMARY_ID=${INSTANCE_ID_SSM}"
echo "CREDENTIAL_FILE=${CREDENTIAL_FILE}"
echo "[ok] admin credentials saved; password was not printed"
