#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  bash scripts/reset-edge-admin-password.sh edge-<id>
  bash scripts/reset-edge-admin-password.sh <id>

Examples:
  bash scripts/reset-edge-admin-password.sh edge-fra1
  bash scripts/reset-edge-admin-password.sh fra1

Behavior:
  - Resolves region/stack from deploy/aws/stage0/edge-targets.json
  - Reads InstanceId from CloudFormation stack outputs
  - Reads ADMIN_EMAIL from /var/lib/tokenkey/.env on the instance
  - Resets admin password to a random 32-hex string via PostgreSQL (pgcrypto bcrypt)
  - Saves email/password to $HOME/Codes/keys/tokenkey-<id>-admin-password.txt
  - Prints only status and the credential file path, never the password
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" || $# -ne 1 ]]; then
  usage
  exit $([[ $# -eq 1 ]] && echo 0 || echo 1)
fi

INPUT_TARGET="$1"
EDGE_ID="${INPUT_TARGET#edge-}"

if [[ ! "$EDGE_ID" =~ ^[a-z]{2,4}[0-9]+$ ]]; then
  echo "[error] edge id must match ^[a-z]{2,4}[0-9]+$: $EDGE_ID" >&2
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MATRIX_PATH="$REPO_ROOT/deploy/aws/stage0/edge-targets.json"

if [[ ! -f "$MATRIX_PATH" ]]; then
  echo "[error] edge target matrix not found: $MATRIX_PATH" >&2
  exit 1
fi

read -r REGION STACK <<<"$(python3 - "$MATRIX_PATH" "$EDGE_ID" <<'PY'
import json
import sys

path, edge_id = sys.argv[1], sys.argv[2]
with open(path, 'r', encoding='utf-8') as f:
    data = json.load(f)

target = (data.get('targets') or {}).get(edge_id)
if not target:
    print(f"[error] unknown edge_id {edge_id}", file=sys.stderr)
    sys.exit(1)

region = target.get('region')
stack = target.get('stack')
if not region or not stack:
    print(f"[error] edge {edge_id} missing region/stack in matrix", file=sys.stderr)
    sys.exit(1)

print(region, stack)
PY
)"

INSTANCE_ID="$(aws cloudformation describe-stacks \
  --region "$REGION" \
  --stack-name "$STACK" \
  --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue' \
  --output text)"

if [[ -z "$INSTANCE_ID" || "$INSTANCE_ID" == "None" ]]; then
  echo "[error] could not resolve InstanceId from stack $STACK in $REGION" >&2
  exit 1
fi

NEW_PASSWORD="$(openssl rand -hex 16)"
KEYS_DIR="$HOME/Codes/keys"
CREDENTIAL_FILE="$KEYS_DIR/tokenkey-$EDGE_ID-admin-password.txt"

if [[ ! -d "$KEYS_DIR" ]]; then
  echo "[error] keys directory not found: $KEYS_DIR" >&2
  exit 1
fi

echo "[info] edge_id=$EDGE_ID region=$REGION stack=$STACK instance_id=$INSTANCE_ID"
echo "[info] resetting admin password via SSM..."

COMMANDS_JSON="$(python3 - "$NEW_PASSWORD" <<'PY'
import json
import sys

new_password = sys.argv[1]
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

COMMAND_ID="$(aws ssm send-command \
  --region "$REGION" \
  --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --comment "reset edge admin password ($EDGE_ID)" \
  --parameters "commands=$COMMANDS_JSON" \
  --query 'Command.CommandId' \
  --output text)"

echo "[info] ssm_command_id=$COMMAND_ID"

if ! aws ssm wait command-executed \
  --region "$REGION" \
  --command-id "$COMMAND_ID" \
  --instance-id "$INSTANCE_ID"; then
  echo "[warn] waiter reported non-success; fetching invocation details..." >&2
fi

STATUS="$(aws ssm get-command-invocation \
  --region "$REGION" \
  --command-id "$COMMAND_ID" \
  --instance-id "$INSTANCE_ID" \
  --query 'Status' \
  --output text)"

STDOUT_CONTENT="$(aws ssm get-command-invocation \
  --region "$REGION" \
  --command-id "$COMMAND_ID" \
  --instance-id "$INSTANCE_ID" \
  --query 'StandardOutputContent' \
  --output text)"

STDERR_CONTENT="$(aws ssm get-command-invocation \
  --region "$REGION" \
  --command-id "$COMMAND_ID" \
  --instance-id "$INSTANCE_ID" \
  --query 'StandardErrorContent' \
  --output text)"

if [[ "$STATUS" != "Success" ]]; then
  echo "[error] SSM command failed: status=$STATUS" >&2
  if [[ -n "$STDOUT_CONTENT" ]]; then
    echo "[error] stdout:" >&2
    printf '%s\n' "$STDOUT_CONTENT" >&2
  fi
  if [[ -n "$STDERR_CONTENT" ]]; then
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
} >"$CREDENTIAL_FILE"
chmod 600 "$CREDENTIAL_FILE"

echo ""
echo "[ok] edge admin password reset complete"
echo "EDGE_ID=$EDGE_ID"
echo "REGION=$REGION"
echo "STACK=$STACK"
echo "INSTANCE_ID=$INSTANCE_ID"
echo "CREDENTIAL_FILE=$CREDENTIAL_FILE"
echo "[ok] admin credentials saved; password was not printed"
