#!/usr/bin/env bash
# run-probe.sh — Deliver a local probe script to a remote TokenKey host via SSM
# or SSH and return its stdout. Wraps the base64+send-command+poll
# pattern that previously lived as prose inside the troubleshooting / traffic-profile
# skills. Probes are read-only by default. A write-capable script must carry its own
# explicit opt-in guard; migration recovery uses CANARY_CREATE_DUMP=1 for that purpose.
#
# Determinism contract (matches dev-rules-convention.mdc §"skill / command 确定性基线"):
#   - Same script + same env + same target → same transport-independent stdout
#   - Field-named output is the responsibility of the probe script (e.g. row_to_json)
#   - This wrapper does NOT parse output; it transports bytes and surfaces remote status
#
# Usage:
#   bash ops/observability/run-probe.sh \
#       --target prod | --target cn-prod \
#       --script ops/observability/probe-caps.sh \
#       [--env KEY=VAL ...] \
#       [--with PATH ...]   upload additional local files to /tmp/<basename> on remote
#       [--remote-path /tmp/script-name.sh] \
#       [--comment "free text"] \
#       [--expected-instance-id i-*|mi-*|uhost-*] \
#       [--timeout-seconds 120]
#       [--compressed-output] integrity-checked gzip transport for large stdout
#       [--describe-script]  print the registered companion/verdict contract and exit
#
# Probe contracts (ops/observability/probe-contracts.json):
#   Optional for delivery. Registered scripts auto-upload companions and expose a
#   machine-readable verdict vocabulary via --describe-script. Unregistered scripts
#   still run (empty companions) so plain SQL/observability probes are not gated.
#   --describe-script remains fail-closed for unregistered scripts.
#
# Endpoint route-gate matrix (group.platform × gateway path, prod):
#   bash ops/observability/run-probe.sh --target prod \
#       --script ops/observability/probe-endpoint-matrix.sh \
#       --with ops/pricing/probe_reserved_resources.sh
#
#   --target prod        resolves region+instance via
#                        ops/stage0/resolve_prod_ssm_target.py (auto → Tencent
#                        USW SSM Hybrid mi-*; frozen Tokyo i-* is explicit-only)
#   --target cn-prod     runs the same probe through the registered SSM Hybrid
#                        managed instance. Set UCLOUD_SSM_INSTANCE_ID and
#                        UCLOUD_SSM_REGION to override the domestic manifest;
#                        UCLOUD_SSH_* remains the one-time/bootstrap fallback.
#
#   --timeout-seconds    30..2592000; passed to the remote transport and also
#                        used as the local polling budget. Polling never
#                        resubmits the command.
#
# Env passthrough:
#   --env FOO=bar appears as `FOO=bar` in the remote shell *before* the script
#   line; multiple --env are concatenated with spaces.
#
# Output (always on stdout):
#   The remote script's stdout verbatim.
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
EXPECTED_INSTANCE_ID=""
DESCRIBE_SCRIPT=0
COMPRESSED_OUTPUT=0
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
    --expected-instance-id) EXPECTED_INSTANCE_ID="${2:-}"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --describe-script) DESCRIBE_SCRIPT=1; shift ;;
    --compressed-output) COMPRESSED_OUTPUT=1; shift ;;
    *) echo "[run-probe] ERROR: unknown arg: $1" >&2; usage >&2; exit 1 ;;
  esac
done

if [ -z "$SCRIPT_PATH" ] || { [ "$DESCRIBE_SCRIPT" = "0" ] && [ -z "$TARGET" ]; }; then
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

if [ "$DESCRIBE_SCRIPT" = "0" ] && [ "$TARGET" != "prod" ] && [ "$TARGET" != "cn-prod" ]; then
	echo "[run-probe] ERROR: --target must be 'prod' or 'cn-prod', got: $TARGET" >&2
  exit 1
fi

# Default remote path to /tmp/<basename>
if [ -z "$REMOTE_PATH" ]; then
  REMOTE_PATH="/tmp/$(basename "$SCRIPT_PATH")"
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROBE_CONTRACTS="$REPO_ROOT/ops/observability/probe-contracts.json"
OUTPUT_TRANSPORT="$REPO_ROOT/ops/observability/probe_output_transport.py"
if [ "$COMPRESSED_OUTPUT" = "1" ]; then
  WITH_FILES+=("$OUTPUT_TRANSPORT")
fi

probe_contract() {
  python3 - "$PROBE_CONTRACTS" "$(basename "$SCRIPT_PATH")" "$1" <<'PY'
import json
import sys

path, script, field = sys.argv[1:4]
with open(path, encoding="utf-8") as handle:
    contract = json.load(handle).get(script)
if contract is None:
    raise SystemExit(2)
if field == "json":
    print(json.dumps({"script": script, **contract}, ensure_ascii=False, indent=2))
else:
    for item in contract[field]:
        print(item)
PY
}

if [ "$DESCRIBE_SCRIPT" = "1" ]; then
  if [ ! -f "$PROBE_CONTRACTS" ] || ! probe_contract json; then
    echo "[run-probe] ERROR: no registered contract for $(basename "$SCRIPT_PATH")" >&2
    exit 1
  fi
  exit 0
fi

# Registration is optional for delivery: only registered probes auto-load companions.
if [ -f "$PROBE_CONTRACTS" ] && probe_contract json >/dev/null 2>&1; then
  while IFS= read -r companion; do
    [ -n "$companion" ] && WITH_FILES+=("$REPO_ROOT/$companion")
  done < <(probe_contract companions)
fi

for extra in "${WITH_FILES[@]+"${WITH_FILES[@]}"}"; do
  if [ ! -f "$extra" ]; then
    echo "[run-probe] ERROR: companion file not found: $extra" >&2
    exit 1
  fi
done

# Always ship the canonical app-container resolvers alongside the probe, so any
# probe can `. /tmp/resolve-app-container.sh` without every call site having to
# remember an explicit --with. Uploading them unconditionally is what makes a
# single owner viable for remote scripts: the alternative is each probe carrying
# its own copy of the active-color logic, which is exactly the drift this
# replaced. Both shapes go up because probes are split between bash bodies and
# python heredocs.
for _tk_resolver in \
  "$REPO_ROOT/ops/lib/resolve-app-container.sh" \
  "$REPO_ROOT/ops/lib/resolve_app_container.py"; do
  if [ -f "$_tk_resolver" ] && [ "$_tk_resolver" != "$SCRIPT_PATH" ]; then
    WITH_FILES+=("$_tk_resolver")
  fi
done

# Local macOS/Homebrew preflight: catch the known aws/pyexpat loader breakage
# before the first real AWS call. Diagnose only; repair stays explicit in the
# helper's --apply mode so this wrapper remains read-only.
if [ "$TARGET" != "cn-prod" ] && [ "$(uname -s)" = "Darwin" ]; then
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
  # Same resolver as deploy-stage0 / feishu sync: auto → Tencent USW mi-*; fails closed otherwise.
  RESOLVE_PROD="${RUN_PROBE_RESOLVE_PROD:-$REPO_ROOT/ops/stage0/resolve_prod_ssm_target.py}"
  PYERR=$(mktemp)
  # Honor PROD_SSM_TARGET explicitly; bare --format kv defaults to auto and
  # fails closed on flip-param AccessDenied after Wave B (#2574 / #2576).
  if ! RES_LINES=$(python3 "${RESOLVE_PROD}" \
      --target "${PROD_SSM_TARGET:-auto}" --format kv 2>"$PYERR"); then
    echo "[run-probe] ERROR: resolve_prod_ssm_target failed" >&2
    cat "$PYERR" >&2
    rm -f "$PYERR"
    exit 2
  fi
  rm -f "$PYERR"
  # kv lines: instance_id=... ssm_region=...
  INSTANCE_ID="$(printf '%s\n' "${RES_LINES}" | awk -F= '$1=="instance_id" {print $2; exit}')"
  REGION="$(printf '%s\n' "${RES_LINES}" | awk -F= '$1=="ssm_region" {print $2; exit}')"
  if [ -z "${REGION:-}" ]; then
    echo "[run-probe] ERROR: resolve_prod_ssm_target returned empty ssm_region" >&2
    exit 2
  fi
fi

if [ "$TARGET" != "cn-prod" ] && { [ -z "${INSTANCE_ID:-}" ] || [ "$INSTANCE_ID" = "None" ]; }; then
  echo "[run-probe] ERROR: could not resolve instance id for target $TARGET" >&2
  exit 2
fi
if [ -n "$EXPECTED_INSTANCE_ID" ]; then
  if [[ ! "$EXPECTED_INSTANCE_ID" =~ ^(i|mi)-[0-9a-f]{8,17}$ ]] && [[ ! "$EXPECTED_INSTANCE_ID" =~ ^uhost-[a-z0-9]+$ ]]; then
    echo "[run-probe] ERROR: --expected-instance-id must be an EC2, SSM-managed, or UHost instance id" >&2
    exit 1
  fi
  if [ "$TARGET" != "cn-prod" ] && [ "$INSTANCE_ID" != "$EXPECTED_INSTANCE_ID" ]; then
    echo "[run-probe] ERROR: resolved instance does not match --expected-instance-id for target $TARGET" >&2
    exit 1
  fi
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

# Compress before base64: native smoke and its companions exceed SSM's 97 KB
# document limit uncompressed. -n keeps the payload independent of file timestamps.
REMOTE_PARTS=("umask 077")
for extra in "${WITH_FILES[@]+"${WITH_FILES[@]}"}"; do
  EB64=$(gzip -cn < "$extra" | base64 | tr -d '\n')
  EP="/tmp/$(basename "$extra")"
  REMOTE_PARTS+=("echo $EB64 | base64 -d | gzip -d > $EP")
done
B64=$(gzip -cn < "$SCRIPT_PATH" | base64 | tr -d '\n')
REMOTE_PARTS+=("echo $B64 | base64 -d | gzip -d > $REMOTE_PATH && chmod +x $REMOTE_PATH")
if [ "$COMPRESSED_OUTPUT" = "1" ]; then
  REMOTE_PARTS+=("env $ENV_PREFIX python3 /tmp/probe_output_transport.py encode -- bash $REMOTE_PATH")
else
  REMOTE_PARTS+=("env $ENV_PREFIX bash $REMOTE_PATH")
fi
REMOTE_LINE=""
for part in "${REMOTE_PARTS[@]}"; do
  if [ -z "$REMOTE_LINE" ]; then
    REMOTE_LINE="$part"
  else
    REMOTE_LINE="$REMOTE_LINE && $part"
  fi
done

# Compose the SSM command JSON via python (avoids shell-quote hell)
PARAMS=$(python3 - "$REMOTE_LINE" "$TIMEOUT_SECONDS" <<'PY'
import json, sys
# send-command --timeout-seconds controls delivery, not script execution.
# AWS-RunShellScript caps execution at two days.
print(json.dumps({"commands": ["set -u", sys.argv[1]],
                  "executionTimeout": [str(min(int(sys.argv[2]), 172800))]}))
PY
)

if [ "$TARGET" = "cn-prod" ]; then
  # UCloud cn-prod is an independent Docker/Postgres host. Prefer its outbound
  # AWS SSM Hybrid agent so a changing operator IP never becomes a probe input.
  UCLOUD_MANIFEST="$REPO_ROOT/deploy/domestic/prod-target.json"
  if [ -n "$EXPECTED_INSTANCE_ID" ] && [ -f "$UCLOUD_MANIFEST" ]; then
    UCLOUD_INSTANCE_ID=$(python3 - "$UCLOUD_MANIFEST" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    print(str(json.load(handle).get("instance_id") or ""))
PY
    )
    if [ "$UCLOUD_INSTANCE_ID" != "$EXPECTED_INSTANCE_ID" ]; then
      echo "[run-probe] ERROR: UCloud instance does not match --expected-instance-id" >&2
      exit 1
    fi
  fi
  UCLOUD_SSM_INSTANCE_ID="${UCLOUD_SSM_INSTANCE_ID-__manifest__}"
  UCLOUD_SSM_REGION="${UCLOUD_SSM_REGION-__manifest__}"
  if [ -f "$UCLOUD_MANIFEST" ] && { [ "$UCLOUD_SSM_INSTANCE_ID" = "__manifest__" ] || [ "$UCLOUD_SSM_REGION" = "__manifest__" ]; }; then
    read -r MANIFEST_SSM_REGION MANIFEST_SSM_INSTANCE_ID < <(python3 - "$UCLOUD_MANIFEST" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    target = json.load(handle)
print(str(target.get("ssm_region") or ""), str(target.get("ssm_instance_id") or ""))
PY
    )
    [ "$UCLOUD_SSM_REGION" != "__manifest__" ] || UCLOUD_SSM_REGION="$MANIFEST_SSM_REGION"
    [ "$UCLOUD_SSM_INSTANCE_ID" != "__manifest__" ] || UCLOUD_SSM_INSTANCE_ID="$MANIFEST_SSM_INSTANCE_ID"
  fi
  if [ -n "$UCLOUD_SSM_INSTANCE_ID" ]; then
    if [[ ! "$UCLOUD_SSM_INSTANCE_ID" =~ ^mi-[0-9a-f]{17}$ ]]; then
      echo "[run-probe] ERROR: UCloud SSM instance id is invalid: $UCLOUD_SSM_INSTANCE_ID" >&2
      exit 1
    fi
    if [[ ! "$UCLOUD_SSM_REGION" =~ ^[a-z0-9-]+$ ]]; then
      echo "[run-probe] ERROR: UCloud SSM region is invalid: $UCLOUD_SSM_REGION" >&2
      exit 1
    fi
    SSM_INFO=""
    if ! SSM_INFO=$(aws ssm describe-instance-information \
      --region "$UCLOUD_SSM_REGION" \
      --filters "Key=InstanceIds,Values=$UCLOUD_SSM_INSTANCE_ID" \
      --output json 2>&1); then
      echo "[run-probe] ERROR: UCloud SSM managed instance lookup failed" >&2
      printf '%s\n' "$SSM_INFO" >&2
      exit 2
    fi
    SSM_STATE=$(printf '%s' "$SSM_INFO" | python3 -c '
import json, sys
items = json.load(sys.stdin).get("InstanceInformationList", [])
if len(items) != 1:
    raise SystemExit(1)
item = items[0]
print("\t".join(str(item.get(k) or "") for k in ("PingStatus", "Name", "PlatformType")))
' 2>/dev/null || true)
    IFS=$'\t' read -r SSM_PING_STATUS SSM_NAME SSM_PLATFORM <<<"$SSM_STATE"
    if [ "$SSM_PING_STATUS" != "Online" ] || [ "$SSM_NAME" != "cn-prod" ] || [ "$SSM_PLATFORM" != "Linux" ]; then
      echo "[run-probe] ERROR: UCloud SSM identity is not ready (name=${SSM_NAME:-<empty>} status=${SSM_PING_STATUS:-<empty>} platform=${SSM_PLATFORM:-<empty>})" >&2
      exit 2
    fi
    REGION="$UCLOUD_SSM_REGION"
    INSTANCE_ID="$UCLOUD_SSM_INSTANCE_ID"
    echo "[run-probe] resolved target=cn-prod ssm_region=$REGION instance_id=$INSTANCE_ID" >&2
  else
    # SSH remains available for one-time bootstrap or explicit recovery. It is
    # never used when the manifest contains a healthy SSM identity.
  UCLOUD_HOST="${UCLOUD_SSH_HOST:-}"
  if [ -z "$UCLOUD_HOST" ]; then
    if [ -f "$UCLOUD_MANIFEST" ]; then
      UCLOUD_HOST=$(python3 - "$UCLOUD_MANIFEST" <<'PY'
import json
import sys
from urllib.parse import urlsplit

with open(sys.argv[1], encoding="utf-8") as handle:
    target = json.load(handle)
host = urlsplit(str(target.get("public_origin") or "")).hostname or ""
print(host)
PY
      )
    fi
  fi
  UCLOUD_USER="${UCLOUD_SSH_USER:-root}"
  UCLOUD_PORT="${UCLOUD_SSH_PORT:-22}"
  UCLOUD_KEY="${UCLOUD_SSH_KEY:-${HOME:-}/.local/share/tokenkey/domestic-onboard/cn-prod-ssh}"
  UCLOUD_KNOWN_HOSTS="${UCLOUD_SSH_KNOWN_HOSTS:-${HOME:-}/.ssh/known_hosts}"
  if [ -z "$UCLOUD_HOST" ]; then
    echo "[run-probe] ERROR: UCloud host is empty; set UCLOUD_SSH_HOST" >&2
    exit 2
  fi
  if [[ ! "$UCLOUD_HOST" =~ ^[A-Za-z0-9._:-]+$ ]]; then
    echo "[run-probe] ERROR: UCLOUD_SSH_HOST contains invalid characters" >&2
    exit 1
  fi
  if [ ! -r "$UCLOUD_KEY" ]; then
    echo "[run-probe] ERROR: UCloud SSH key is not readable: $UCLOUD_KEY" >&2
    echo "[run-probe] Fix: set UCLOUD_SSH_KEY to the cn-prod read-only operator key" >&2
    exit 2
  fi
  if [[ ! "$UCLOUD_PORT" =~ ^[0-9]+$ ]] || [ "$UCLOUD_PORT" -lt 1 ] || [ "$UCLOUD_PORT" -gt 65535 ]; then
    echo "[run-probe] ERROR: UCLOUD_SSH_PORT must be 1..65535" >&2
    exit 1
  fi
  SSH_ARGS=(
    -i "$UCLOUD_KEY"
    -p "$UCLOUD_PORT"
    -o BatchMode=yes
    -o ConnectTimeout=30
    -o ServerAliveInterval=15
    -o ServerAliveCountMax=3
    -o StrictHostKeyChecking="${UCLOUD_SSH_STRICT_HOST_KEY_CHECKING:-yes}"
  )
  if [ -n "$UCLOUD_KNOWN_HOSTS" ]; then
    SSH_ARGS+=(-o "UserKnownHostsFile=$UCLOUD_KNOWN_HOSTS")
  fi
  SSH_TIMEOUT=()
  if command -v timeout >/dev/null 2>&1; then
    SSH_TIMEOUT=(timeout "$TIMEOUT_SECONDS")
  elif command -v gtimeout >/dev/null 2>&1; then
    SSH_TIMEOUT=(gtimeout "$TIMEOUT_SECONDS")
  fi
  SSH_STDOUT=$(mktemp)
  SSH_STDERR=$(mktemp)
  trap 'rm -f "$SSH_STDOUT" "$SSH_STDERR"' EXIT
  echo "[run-probe] resolved target=cn-prod host=$UCLOUD_HOST user=$UCLOUD_USER" >&2
  START=$(date +%s)
  if [ "${#SSH_TIMEOUT[@]}" -gt 0 ]; then
    SSH_RUN=("${SSH_TIMEOUT[@]}" ssh)
  else
    SSH_RUN=(ssh)
  fi
  if "${SSH_RUN[@]}" "${SSH_ARGS[@]}" "$UCLOUD_USER@$UCLOUD_HOST" bash -s >"$SSH_STDOUT" 2>"$SSH_STDERR" <<EOF
set -u
$REMOTE_LINE
EOF
  then
    SSH_STATUS=0
  else
    SSH_STATUS=$?
  fi
  END=$(date +%s)
  DURATION=$((END - START))
  if [ -s "$SSH_STDERR" ]; then
    sed 's/^/[remote-stderr] /' "$SSH_STDERR" >&2
  fi
  if [ "$SSH_STATUS" -ne 0 ]; then
    if [ "$SSH_STATUS" -eq 255 ]; then
      echo "[run-probe] ERROR: UCloud SSH transport failed (exit=$SSH_STATUS)" >&2
      exit 2
    fi
    echo "[run-probe] status=Failed duration=${DURATION}s" >&2
    echo "[run-probe] ERROR: remote status=Failed (exit=$SSH_STATUS)" >&2
    exit 3
  fi
  echo "[run-probe] status=Success duration=${DURATION}s" >&2
  if [ "$COMPRESSED_OUTPUT" = "1" ]; then
    if ! python3 "$OUTPUT_TRANSPORT" decode <"$SSH_STDOUT"; then
      echo "[run-probe] ERROR: compressed output integrity check failed" >&2
      exit 2
    fi
  else
    cat "$SSH_STDOUT"
  fi
  exit 0
  fi
fi

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

if [[ "$STDOUT" == *"--output truncated--"* ]]; then
  echo "[run-probe] ERROR: SSM stdout truncated; refusing partial probe output" >&2
  exit 2
fi
if [ "$COMPRESSED_OUTPUT" = "1" ]; then
  if ! printf '%s\n' "$STDOUT" | python3 "$OUTPUT_TRANSPORT" decode; then
    echo "[run-probe] ERROR: compressed output integrity check failed" >&2
    exit 2
  fi
else
  printf '%s\n' "$STDOUT"
fi
