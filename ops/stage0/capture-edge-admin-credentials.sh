#!/usr/bin/env bash
# capture-edge-admin-credentials.sh — Read the initial admin credentials that
# Stage0 AUTO_SETUP wrote into the host's logs on first boot, and persist them
# to $HOME/Codes/keys/tokenkey-<edge_id>-admin-password.txt (chmod 600).
#
# This replaces the §3.2 prose bash blob in the tokenkey-stage0-edge-expansion
# skill. Sibling of ops/stage0/reset-edge-admin-password.sh — same target
# resolution, same credentials file format, never prints the password.
#
# If the initial credentials cannot be found (logs already rotated, env unset),
# the script exits non-zero and instructs the operator to run
# reset-edge-admin-password.sh instead.
#
# Determinism contract (matches dev-rules-convention.mdc §"skill / command 确定性基线"):
#   - Same edge state → same exit code + same credentials file contents.
#   - Password is never echoed to stdout/stderr/journal.
#
# Usage:
#   bash ops/stage0/capture-edge-admin-credentials.sh edge-<id>
#   bash ops/stage0/capture-edge-admin-credentials.sh <id>
#
# Exit codes:
#   0 — credentials captured and written
#   1 — usage / invalid edge id / keys dir missing
#   2 — AWS / SSM transport failure
#   3 — credentials NOT in logs (typical when capture is too late); operator
#       should run reset-edge-admin-password.sh
set -euo pipefail

usage() {
  sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" || $# -ne 1 ]]; then
  usage
  exit $([[ $# -eq 1 ]] && echo 0 || echo 1)
fi

INPUT_TARGET="$1"
EDGE_ID="${INPUT_TARGET#edge-}"

if [[ ! "$EDGE_ID" =~ ^[a-z]{2,4}[0-9]+$ ]]; then
  echo "[capture-edge-admin-credentials] ERROR: edge id must match ^[a-z]{2,4}[0-9]+$: $EDGE_ID" >&2
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MATRIX_PATH="$REPO_ROOT/deploy/aws/stage0/edge-targets.json"

if [[ ! -f "$MATRIX_PATH" ]]; then
  echo "[capture-edge-admin-credentials] ERROR: edge target matrix not found: $MATRIX_PATH" >&2
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
  echo "[capture-edge-admin-credentials] ERROR: could not resolve InstanceId from stack $STACK in $REGION" >&2
  exit 2
fi

KEYS_DIR="$HOME/Codes/keys"
CREDENTIAL_FILE="$KEYS_DIR/tokenkey-$EDGE_ID-admin-password.txt"

if [[ ! -d "$KEYS_DIR" ]]; then
  echo "[capture-edge-admin-credentials] ERROR: keys directory not found: $KEYS_DIR" >&2
  exit 1
fi

echo "[capture-edge-admin-credentials] edge_id=$EDGE_ID region=$REGION stack=$STACK instance_id=$INSTANCE_ID"
echo "[capture-edge-admin-credentials] reading initial admin credentials via SSM..."

# Probe four likely sources; each tolerates absence so a missing log path
# does not abort the SSM batch. ADMIN_EMAIL comes from .env; the generated
# one-time password is printed by AUTO_SETUP into docker logs / journal /
# the bootstrap log file.
# preflight-allow: swallow  (each remote command intentionally falls through on absence)
COMMANDS_JSON=$(python3 <<'PY'
import json
commands = [
  "set -euo pipefail",
  "sudo grep '^ADMIN_EMAIL=' /var/lib/tokenkey/.env || true",  # preflight-allow: swallow
  "sudo docker logs tokenkey 2>&1 | grep -E 'Generated admin password' || true",  # preflight-allow: swallow
  "sudo journalctl -u tokenkey.service --no-pager | grep -E 'Generated admin password' || true",  # preflight-allow: swallow
  "sudo grep -E 'Generated admin password|ADMIN_EMAIL' /var/log/tokenkey-edge-bootstrap.log || true",  # preflight-allow: swallow
]
print(json.dumps({"commands": commands}))
PY
)

CMD_ID="$(aws ssm send-command \
  --region "$REGION" \
  --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --comment "capture initial edge admin credentials ($EDGE_ID)" \
  --parameters "$COMMANDS_JSON" \
  --query 'Command.CommandId' \
  --output text)"

echo "[capture-edge-admin-credentials] ssm_command_id=$CMD_ID"

aws ssm wait command-executed \
  --region "$REGION" --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" || true  # preflight-allow: swallow

STATUS=$(aws ssm get-command-invocation \
  --region "$REGION" --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
  --query 'Status' --output text)

STDOUT=$(aws ssm get-command-invocation \
  --region "$REGION" --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
  --query 'StandardOutputContent' --output text)

if [[ "$STATUS" != "Success" ]]; then
  echo "[capture-edge-admin-credentials] ERROR: SSM Status=$STATUS" >&2
  exit 2
fi

# Parse ADMIN_EMAIL and the most-recent generated password from the combined stdout.
ADMIN_EMAIL=$(printf '%s\n' "$STDOUT" | grep -Eo 'ADMIN_EMAIL=[^[:space:]]+' \
  | tail -n 1 | cut -d= -f2-)
ADMIN_PASSWORD=$(printf '%s\n' "$STDOUT" \
  | sed -nE 's/.*Generated admin password \(one-time\): ([^[:space:]]+).*/\1/p' \
  | tail -n 1)

if [[ -z "$ADMIN_EMAIL" ]] || [[ -z "$ADMIN_PASSWORD" ]]; then
  echo "[capture-edge-admin-credentials] WARN: initial admin credential not found in logs." >&2
  echo "[capture-edge-admin-credentials] Hint: logs may have rotated. Run instead:" >&2
  echo "[capture-edge-admin-credentials]   bash ops/stage0/reset-edge-admin-password.sh $EDGE_ID" >&2
  exit 3
fi

umask 077
{
  printf 'email=%s\n' "$ADMIN_EMAIL"
  printf 'password=%s\n' "$ADMIN_PASSWORD"
} >"$CREDENTIAL_FILE"
chmod 600 "$CREDENTIAL_FILE"

# Scrub temporaries from this shell — password may still live in process env until exit.
unset STDOUT ADMIN_PASSWORD

echo
echo "[capture-edge-admin-credentials] ok: credentials saved (password not printed)"
echo "EDGE_ID=$EDGE_ID"
echo "REGION=$REGION"
echo "STACK=$STACK"
echo "INSTANCE_ID=$INSTANCE_ID"
echo "CREDENTIAL_FILE=$CREDENTIAL_FILE"
