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
#       [--with PATH ...]   upload additional local files to /tmp/<basename> on remote
#       [--remote-path /tmp/script-name.sh] \
#       [--comment "free text"] \
#       [--timeout-seconds 120]
#
# Endpoint route-gate matrix (group.platform × gateway path, prod):
#   bash ops/observability/run-probe.sh --target prod \
#       --script ops/observability/probe-endpoint-matrix.sh \
#       --with ops/pricing/probe_reserved_resources.sh
#
#   --target prod        resolves region+instance from CloudFormation
#                        (stack=tokenkey-prod-stage0, region=us-east-1)
#   --target edge:<id>   resolves via ops/stage0/edge_ssm_execution.py when
#                        ALLOW_PLANNED is unset (EC2 CFN or Lightsail MI from
#                        Parameter Store — same auto rule as admin reset).
#                        If ALLOW_PLANNED=1, falls back to
#                        deploy/aws/stage0/resolve-edge-target.py + CloudFormation
#                        (planned edges; EC2-matrix shaped only).
#
#   --timeout-seconds    30..2592000; passed to SSM and also used as the local
#                        polling budget. Polling never resubmits the command.
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
# Polling the original CommandId is not a retry. InvocationDoesNotExist is
# tolerated while the newly-created invocation becomes visible, but no command
# is ever re-submitted.
set -euo pipefail

usage() {
  sed -n '2,45p' "$0" | sed 's/^# \{0,1\}//'
}

TARGET=""
SCRIPT_PATH=""
REMOTE_PATH=""
COMMENT="run-probe wrapper"
TIMEOUT_SECONDS=120
declare -a ENVS=()
declare -a WITH_FILES=()

while [ "$#" -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --target) TARGET="${2:-}"; shift 2 ;;
    --script) SCRIPT_PATH="${2:-}"; shift 2 ;;
    --env) ENVS+=("${2:-}"); shift 2 ;;
    --with) WITH_FILES+=("${2:-}"); shift 2 ;;
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

if [[ ! "$TIMEOUT_SECONDS" =~ ^[0-9]+$ ]]; then
  echo "[run-probe] ERROR: --timeout-seconds must be an integer from 30 to 2592000" >&2
  exit 1
fi
TIMEOUT_SECONDS=$((10#$TIMEOUT_SECONDS))
if [ "$TIMEOUT_SECONDS" -lt 30 ] || [ "$TIMEOUT_SECONDS" -gt 2592000 ]; then
  echo "[run-probe] ERROR: --timeout-seconds must be an integer from 30 to 2592000" >&2
  exit 1
fi

if [ ! -f "$SCRIPT_PATH" ]; then
  echo "[run-probe] ERROR: script not found: $SCRIPT_PATH" >&2
  exit 1
fi

for extra in "${WITH_FILES[@]+"${WITH_FILES[@]}"}"; do
  if [ ! -f "$extra" ]; then
    echo "[run-probe] ERROR: --with file not found: $extra" >&2
    exit 1
  fi
done

# Default remote path to /tmp/<basename>
if [ -z "$REMOTE_PATH" ]; then
  REMOTE_PATH="/tmp/$(basename "$SCRIPT_PATH")"
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# Local macOS/Homebrew preflight: catch the known aws/pyexpat loader breakage
# before the first real AWS call. Diagnose only; repair stays explicit in the
# helper's --apply mode so this wrapper remains read-only.
if [ "$(uname -s)" = "Darwin" ]; then
  AWS_PYEXPAT_HELPER="$REPO_ROOT/scripts/checks/check-local-aws-pyexpat.py"
  if [ -f "$AWS_PYEXPAT_HELPER" ] && ! python3 "$AWS_PYEXPAT_HELPER" --quiet; then
    echo "[run-probe] ERROR: local aws bootstrap check failed" >&2
    echo "[run-probe] Fix: python3 scripts/checks/check-local-aws-pyexpat.py --apply" >&2
    exit 2
  fi
fi

# Resolve REGION + INSTANCE_ID per target shape
REGION=""
INSTANCE_ID=""
if [ "$TARGET" = "prod" ]; then
  REGION="us-east-1"
  STACK="tokenkey-prod-stage0"
  INSTANCE_ID=$(aws cloudformation describe-stacks \
    --region "$REGION" --stack-name "$STACK" \
    --query "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue" \
    --output text 2>&1) || {
    echo "[run-probe] ERROR: describe-stacks failed for $STACK in $REGION" >&2
    printf '%s\n' "$INSTANCE_ID" >&2
    exit 2
  }
  if [ -z "$INSTANCE_ID" ] || [ "$INSTANCE_ID" = "None" ]; then
    INSTANCE_ID=$(aws cloudformation describe-stack-resources \
      --region "$REGION" --stack-name "$STACK" \
      --query "StackResources[?ResourceType=='AWS::EC2::Instance']|[0].PhysicalResourceId" \
      --output text 2>/dev/null || true)
  fi
elif [[ "$TARGET" == edge:* ]]; then
  EDGE_ID="${TARGET#edge:}"
  if [ -z "$EDGE_ID" ]; then
    echo "[run-probe] ERROR: --target edge: requires an edge id" >&2
    exit 1
  fi
  if [ "${ALLOW_PLANNED:-0}" = "1" ]; then
    MATRIX="$REPO_ROOT/deploy/aws/stage0/edge-targets.json"
    ALLOW_PLANNED_FLAG="--allow-planned"
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
    INSTANCE_ID=$(aws cloudformation describe-stacks \
      --region "$REGION" --stack-name "$STACK" \
      --query "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue" \
      --output text 2>&1) || {
      echo "[run-probe] ERROR: describe-stacks failed for $STACK in $REGION" >&2
      printf '%s\n' "$INSTANCE_ID" >&2
      exit 2
    }
    if [ -z "$INSTANCE_ID" ] || [ "$INSTANCE_ID" = "None" ]; then
      INSTANCE_ID=$(aws cloudformation describe-stack-resources \
        --region "$REGION" --stack-name "$STACK" \
        --query "StackResources[?ResourceType=='AWS::EC2::Instance']|[0].PhysicalResourceId" \
        --output text 2>/dev/null || true)
    fi
  else
    PYERR=$(mktemp)
    if ! RES_LINES=$(python3 "$REPO_ROOT/ops/stage0/edge_ssm_execution.py" \
        --repo-root "$REPO_ROOT" --edge-id "$EDGE_ID" --format env 2>"$PYERR"); then
      echo "[run-probe] ERROR: edge_ssm_execution.py failed for edge_id=$EDGE_ID" >&2
      cat "$PYERR" >&2
      rm -f "$PYERR"
      exit 1
    fi
    rm -f "$PYERR"
    eval "$RES_LINES"
  fi
else
  echo "[run-probe] ERROR: --target must be 'prod' or 'edge:<id>', got: $TARGET" >&2
  exit 1
fi

if [ -z "${INSTANCE_ID:-}" ] || [ "$INSTANCE_ID" = "None" ]; then
  echo "[run-probe] ERROR: could not resolve instance id for target $TARGET" >&2
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

# Pack the local script (and optional --with companions) as base64 and assemble a remote one-liner
REMOTE_PARTS=()
for extra in "${WITH_FILES[@]+"${WITH_FILES[@]}"}"; do
  EB64=$(base64 < "$extra" | tr -d '\n')
  EP="/tmp/$(basename "$extra")"
  REMOTE_PARTS+=("echo $EB64 | base64 -d > $EP")
done
B64=$(base64 < "$SCRIPT_PATH" | tr -d '\n')
REMOTE_PARTS+=("echo $B64 | base64 -d > $REMOTE_PATH && chmod +x $REMOTE_PATH")
REMOTE_PARTS+=("env $ENV_PREFIX bash $REMOTE_PATH")
REMOTE_LINE=""
for part in "${REMOTE_PARTS[@]}"; do
  if [ -z "$REMOTE_LINE" ]; then
    REMOTE_LINE="$part"
  else
    REMOTE_LINE="$REMOTE_LINE && $part"
  fi
done

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

# The AWS waiter has a fixed ~100s budget (5s x 20 checks), independent of the
# caller's --timeout-seconds. Poll here so every caller gets the timeout it
# requested while continuing to observe the same CommandId.
DEADLINE=$((START + TIMEOUT_SECONDS))
INVOCATION_JSON=""
INVOCATION_ERROR_FILE=$(mktemp)
trap 'rm -f "$INVOCATION_ERROR_FILE"' EXIT
POLL_TIMED_OUT=0
while :; do
  if INVOCATION_JSON=$(aws ssm get-command-invocation \
      --region "$REGION" --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
      --output json 2>"$INVOCATION_ERROR_FILE"); then
    STATUS=$(printf '%s' "$INVOCATION_JSON" | python3 -c \
      'import json, sys; print(json.load(sys.stdin).get("Status", ""))')
    case "$STATUS" in
      Success|Cancelled|TimedOut|Failed) break ;;
      Pending|InProgress|Delayed|Cancelling) ;;
      *)
        echo "[run-probe] ERROR: unexpected SSM status: ${STATUS:-<empty>}" >&2
        exit 2
        ;;
    esac
  else
    INVOCATION_ERROR=$(cat "$INVOCATION_ERROR_FILE")
    if [[ "$INVOCATION_ERROR" == *"InvocationDoesNotExist"* ]]; then
      STATUS="Pending"
      INVOCATION_JSON=""
    else
      echo "[run-probe] ERROR: ssm get-command-invocation failed" >&2
      printf '%s\n' "$INVOCATION_ERROR" >&2
      exit 2
    fi
  fi

  NOW=$(date +%s)
  REMAINING=$((DEADLINE - NOW))
  if [ "$REMAINING" -le 0 ]; then
    POLL_TIMED_OUT=1
    break
  fi
  if [ "$REMAINING" -lt 5 ]; then
    sleep "$REMAINING"
  else
    sleep 5
  fi
done

END=$(date +%s)
DURATION=$((END - START))
echo "[run-probe] status=$STATUS duration=${DURATION}s" >&2

STDOUT=""
STDERR=""
if [ -n "$INVOCATION_JSON" ]; then
  STDOUT=$(printf '%s' "$INVOCATION_JSON" | python3 -c \
    'import json, sys; sys.stdout.write(json.load(sys.stdin).get("StandardOutputContent") or "")')
  STDERR=$(printf '%s' "$INVOCATION_JSON" | python3 -c \
    'import json, sys; sys.stdout.write(json.load(sys.stdin).get("StandardErrorContent") or "")')
fi

# Stream stderr through with a tag so it's distinguishable from wrapper messages
if [ -n "$STDERR" ] && [ "$STDERR" != "None" ]; then
  printf '%s\n' "$STDERR" | sed 's/^/[remote-stderr] /' >&2
fi

if [ "$POLL_TIMED_OUT" = "1" ]; then
  echo "[run-probe] ERROR: polling timed out status=$STATUS command_id=$CMD_ID" >&2
  printf '%s\n' "$STDOUT"
  exit 3
fi

if [ "$STATUS" != "Success" ]; then
  echo "[run-probe] ERROR: remote status=$STATUS" >&2
  printf '%s\n' "$STDOUT"
  exit 3
fi

printf '%s\n' "$STDOUT"
