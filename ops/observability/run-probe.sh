#!/usr/bin/env bash
# run-probe.sh — Deliver a local probe script to a remote TokenKey host via SSM
# and return its StandardOutputContent. Wraps the base64+send-command+poll
# pattern that previously lived as prose inside the troubleshooting / traffic-profile
# skills. Read-only by convention: caller must not pass write-side scripts.
#
# Determinism contract (matches dev-rules-convention.mdc §"skill / command 确定性基线"):
#   - Same script + same env + same target → same SSM CommandId-independent stdout
#   - Field-named output is the responsibility of the probe script (e.g. row_to_json)
#   - This wrapper does NOT parse output; it transports bytes and surfaces SSM status
#
# Usage:
#   bash ops/observability/run-probe.sh \
#       --target prod | --target edge:<id> \
#       --script ops/observability/probe-caps.sh \
#       [--env KEY=VAL ...] \
#       [--remote-path /tmp/script-name.sh] \
#       [--comment "free text"] \
#       [--timeout-seconds 120]
#
#   --target prod        resolves region+instance from CloudFormation
#                        (stack=tokenkey-prod-stage0, region=us-east-1)
#   --target edge:<id>   resolves from deploy/aws/stage0/edge-targets.json
#                        and CloudFormation. Refuses planned (deployable=false)
#                        unless caller exports ALLOW_PLANNED=1.
#
# Env passthrough:
#   --env FOO=bar appears as `FOO=bar` in the remote shell *before* the script
#   line; multiple --env are concatenated with spaces.
#
# Output (always on stdout):
#   The remote script's StandardOutputContent verbatim.
#
# Stderr lines (decorated, distinguishable from probe stderr):
#   "[run-probe] resolved region=... instance_id=... command_id=..."
#   "[run-probe] status=... duration=...s"
#
# Exit codes:
#   0 — SSM Status=Success and the remote script exited 0
#   1 — wrapper validation failure (missing script, unknown target, etc.)
#   2 — SSM/AWS transport failure (assume-role, send-command, get-invocation)
#   3 — remote script Status != Success (timeout / cancelled / failed)
#
# The wrapper deliberately does NOT retry on transient SSM errors — retry is a
# decision the caller (skill author or human operator) should make. Silent
# retries hide pollution like "probe instance went offline" or "rate limited".
set -euo pipefail

usage() {
  sed -n '2,42p' "$0" | sed 's/^# \{0,1\}//'
}

TARGET=""
SCRIPT_PATH=""
REMOTE_PATH=""
COMMENT="run-probe wrapper"
TIMEOUT_SECONDS=120
declare -a ENVS=()

while [ "$#" -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --target) TARGET="${2:-}"; shift 2 ;;
    --script) SCRIPT_PATH="${2:-}"; shift 2 ;;
    --env) ENVS+=("${2:-}"); shift 2 ;;
    --remote-path) REMOTE_PATH="${2:-}"; shift 2 ;;
    --comment) COMMENT="${2:-}"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    *) echo "[run-probe] ERROR: unknown arg: $1" >&2; usage >&2; exit 1 ;;
  esac
done

if [ -z "$TARGET" ] || [ -z "$SCRIPT_PATH" ]; then
  echo "[run-probe] ERROR: --target and --script are required" >&2
  usage >&2
  exit 1
fi

if [ ! -f "$SCRIPT_PATH" ]; then
  echo "[run-probe] ERROR: script not found: $SCRIPT_PATH" >&2
  exit 1
fi

# Default remote path to /tmp/<basename>
if [ -z "$REMOTE_PATH" ]; then
  REMOTE_PATH="/tmp/$(basename "$SCRIPT_PATH")"
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# Resolve REGION + INSTANCE_ID per target shape
REGION=""
INSTANCE_ID=""
if [ "$TARGET" = "prod" ]; then
  REGION="us-east-1"
  STACK="tokenkey-prod-stage0"
elif [[ "$TARGET" == edge:* ]]; then
  EDGE_ID="${TARGET#edge:}"
  if [ -z "$EDGE_ID" ]; then
    echo "[run-probe] ERROR: --target edge: requires an edge id" >&2
    exit 1
  fi
  MATRIX="$REPO_ROOT/deploy/aws/stage0/edge-targets.json"
  ALLOW_PLANNED_FLAG=""
  if [ "${ALLOW_PLANNED:-0}" = "1" ]; then
    ALLOW_PLANNED_FLAG="--allow-planned"
  fi
  # resolve-edge-target.py prints key=value; harvest region+stack
  RESOLVED=$(python3 "$REPO_ROOT/deploy/aws/stage0/resolve-edge-target.py" \
    --edge-id "$EDGE_ID" --matrix "$MATRIX" $ALLOW_PLANNED_FLAG 2>&1) || {
    echo "[run-probe] ERROR: resolve-edge-target.py failed for edge_id=$EDGE_ID" >&2
    printf '%s\n' "$RESOLVED" >&2
    exit 1
  }
  REGION=$(printf '%s\n' "$RESOLVED" | awk -F= '/^region=/{print $2; exit}')
  STACK=$(printf '%s\n' "$RESOLVED" | awk -F= '/^stack=/{print $2; exit}')
  if [ -z "$REGION" ] || [ -z "$STACK" ]; then
    echo "[run-probe] ERROR: could not parse region/stack from resolve-edge-target output" >&2
    exit 1
  fi
else
  echo "[run-probe] ERROR: --target must be 'prod' or 'edge:<id>', got: $TARGET" >&2
  exit 1
fi

INSTANCE_ID=$(aws cloudformation describe-stacks \
  --region "$REGION" --stack-name "$STACK" \
  --query "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue" \
  --output text 2>&1) || {
  echo "[run-probe] ERROR: describe-stacks failed for $STACK in $REGION" >&2
  printf '%s\n' "$INSTANCE_ID" >&2
  exit 2
}

if [ -z "$INSTANCE_ID" ] || [ "$INSTANCE_ID" = "None" ]; then
  # Fallback to describe-stack-resources for older stacks lacking InstanceId output
  INSTANCE_ID=$(aws cloudformation describe-stack-resources \
    --region "$REGION" --stack-name "$STACK" \
    --query "StackResources[?ResourceType=='AWS::EC2::Instance']|[0].PhysicalResourceId" \
    --output text 2>/dev/null || true)  # preflight-allow: swallow
fi
if [ -z "$INSTANCE_ID" ] || [ "$INSTANCE_ID" = "None" ]; then
  echo "[run-probe] ERROR: could not resolve InstanceId from stack $STACK" >&2
  exit 2
fi

# Build env prefix (e.g. "PLATFORM=anthropic ERR_HOURS=2")
ENV_PREFIX=""
for kv in "${ENVS[@]+"${ENVS[@]}"}"; do
  if [[ ! "$kv" =~ ^[A-Z_][A-Z0-9_]*= ]]; then
    echo "[run-probe] ERROR: --env must be KEY=VAL with uppercase KEY: $kv" >&2
    exit 1
  fi
  # shell-quote VAL to survive ssm parameters JSON re-quoting on remote side
  K="${kv%%=*}"
  V="${kv#*=}"
  ENV_PREFIX="$ENV_PREFIX $K='${V//\'/\'\\\'\'}'"
done

# Pack the local script as base64 and assemble a remote one-liner
B64=$(base64 < "$SCRIPT_PATH" | tr -d '\n')
REMOTE_LINE="echo $B64 | base64 -d > $REMOTE_PATH && chmod +x $REMOTE_PATH && env $ENV_PREFIX bash $REMOTE_PATH"

# Compose the SSM command JSON via python (avoids shell-quote hell)
PARAMS=$(python3 - "$REMOTE_LINE" <<'PY'
import json, sys
print(json.dumps({"commands": ["set -u", sys.argv[1]]}))
PY
)

echo "[run-probe] resolved region=$REGION instance_id=$INSTANCE_ID" >&2
START=$(date +%s)
CMD_ID=$(aws ssm send-command \
  --region "$REGION" \
  --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --comment "$COMMENT" \
  --timeout-seconds "$TIMEOUT_SECONDS" \
  --parameters "$PARAMS" \
  --query 'Command.CommandId' --output text 2>&1) || {
  echo "[run-probe] ERROR: ssm send-command failed" >&2
  printf '%s\n' "$CMD_ID" >&2
  exit 2
}
echo "[run-probe] command_id=$CMD_ID" >&2

# Wait without --no-cli-pager flag dependencies; AWS waiter handles polling.
aws ssm wait command-executed \
  --region "$REGION" --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
  >/dev/null 2>&1 || true  # preflight-allow: swallow

STATUS=$(aws ssm get-command-invocation \
  --region "$REGION" --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
  --query 'Status' --output text)
END=$(date +%s)
DURATION=$((END - START))
echo "[run-probe] status=$STATUS duration=${DURATION}s" >&2

STDOUT=$(aws ssm get-command-invocation \
  --region "$REGION" --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
  --query 'StandardOutputContent' --output text)

STDERR=$(aws ssm get-command-invocation \
  --region "$REGION" --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
  --query 'StandardErrorContent' --output text)

# Stream stderr through with a tag so it's distinguishable from wrapper messages
if [ -n "$STDERR" ] && [ "$STDERR" != "None" ]; then
  printf '%s\n' "$STDERR" | sed 's/^/[remote-stderr] /' >&2
fi

if [ "$STATUS" != "Success" ]; then
  echo "[run-probe] ERROR: remote status=$STATUS" >&2
  printf '%s\n' "$STDOUT"
  exit 3
fi

printf '%s\n' "$STDOUT"
