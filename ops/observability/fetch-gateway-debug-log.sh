#!/usr/bin/env bash
# fetch-gateway-debug-log.sh — pull SUB2API_DEBUG_GATEWAY_BODY log from prod/edge to local.
#
# Remote path default: /app/data/gateway_debug.log (inside tokenkey container).
# Flow: SSM gzip on instance → presigned S3 PUT (curl) → local download + gunzip.
#
# Usage:
#   bash ops/observability/fetch-gateway-debug-log.sh --target edge:us1
#   bash ops/observability/fetch-gateway-debug-log.sh --target edge:uk1 --out ./.cache/gateway-debug
#
# Env:
#   SSM_OUTPUT_S3_BUCKET   default: layer-zip-repro-682751977094-us-east-1
#   LOG_PATH               default: /app/data/gateway_debug.log
#   OUT_DIR                default: ./.cache/gateway-debug
#   AWS_SSM_WAIT_MAX       default: 900
#   PRESIGN_TTL_SEC        default: 7200
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TARGET=""
OUT_DIR="${OUT_DIR:-./.cache/gateway-debug}"
LOG_PATH="${LOG_PATH:-/app/data/gateway_debug.log}"
BUCKET="${SSM_OUTPUT_S3_BUCKET:-layer-zip-repro-682751977094-us-east-1}"
S3_REGION="${SSM_OUTPUT_S3_REGION:-us-east-1}"
PREFIX="tokenkey/gateway-debug"
WAIT_MAX="${AWS_SSM_WAIT_MAX:-900}"
PRESIGN_TTL="${PRESIGN_TTL_SEC:-7200}"

usage() {
  sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --target) TARGET="${2:-}"; shift 2 ;;
    --out) OUT_DIR="${2:-}"; shift 2 ;;
    --log-path) LOG_PATH="${2:-}"; shift 2 ;;
    *) echo "[fetch-gateway-debug-log] ERROR: unknown arg: $1" >&2; usage >&2; exit 1 ;;
  esac
done

if [ -z "$TARGET" ]; then
  echo "[fetch-gateway-debug-log] ERROR: --target is required (prod | edge:<id>)" >&2
  exit 1
fi

log() { echo "[fetch-gateway-debug-log] $*"; }
err() { echo "[fetch-gateway-debug-log] error: $*" >&2; }

command -v python3 >/dev/null 2>&1 || { err "python3 required"; exit 1; }
command -v aws >/dev/null 2>&1 || { err "aws CLI required"; exit 1; }
command -v jq >/dev/null 2>&1 || { err "jq required"; exit 1; }

REGION=""
INSTANCE_ID=""
EDGE_LABEL="$TARGET"
if [ "$TARGET" = "prod" ]; then
  REGION="us-east-1"
  STACK="tokenkey-prod-stage0"
  INSTANCE_ID=$(aws cloudformation describe-stacks \
    --region "$REGION" --stack-name "$STACK" \
    --query "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue" \
    --output text)
elif [[ "$TARGET" == edge:* ]]; then
  EDGE_ID="${TARGET#edge:}"
  RES_LINES=$(python3 "$REPO_ROOT/ops/stage0/edge_ssm_execution.py" \
    --repo-root "$REPO_ROOT" --edge-id "$EDGE_ID" --format env)
  eval "$RES_LINES"
  EDGE_LABEL="edge-${EDGE_ID}"
else
  err "--target must be prod or edge:<id>"
  exit 1
fi

if [ -z "${INSTANCE_ID:-}" ] || [ "$INSTANCE_ID" = "None" ]; then
  err "could not resolve instance id for $TARGET"
  exit 1
fi

mkdir -p "$OUT_DIR"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
S3_KEY="${PREFIX}/${EDGE_LABEL}/${STAMP}.log.gz"
LOCAL_GZ="${OUT_DIR}/${EDGE_LABEL}-gateway_debug.log.gz"
LOCAL_LOG="${OUT_DIR}/${EDGE_LABEL}-gateway_debug.log"
REMOTE_GZ="/tmp/${EDGE_LABEL}-gateway_debug-${STAMP}.log.gz"

SCRATCH="$(mktemp -d)"
trap 'rm -rf "$SCRATCH"' EXIT
VENV="$SCRATCH/presign-venv"
python3 -m venv "$VENV"
"$VENV/bin/pip" install -q boto3

export AWS_REGION="$S3_REGION"
export SSM_OUTPUT_S3_BUCKET="$BUCKET"
export S3_KEY_FOR_PRESIGN="$S3_KEY"
export PRESIGN_TTL_SEC="$PRESIGN_TTL"

PUT_URL="$("$VENV/bin/python" -c "
import os, boto3
b = os.environ['SSM_OUTPUT_S3_BUCKET']
k = os.environ['S3_KEY_FOR_PRESIGN']
r = os.environ['AWS_REGION']
e = int(os.environ['PRESIGN_TTL_SEC'])
print(boto3.client('s3', region_name=r).generate_presigned_url(
    'put_object', Params={'Bucket': b, 'Key': k}, ExpiresIn=e), end='')
")"
PRESIGN_B64="$(printf '%s' "$PUT_URL" | base64 | tr -d '\n')"

PARAMS="$SCRATCH/ssm-params.json"
jq -n \
  --arg log_path "$LOG_PATH" \
  --arg remote_gz "$REMOTE_GZ" \
  --arg b64 "$PRESIGN_B64" \
  '{
    commands: [
      "set -euo pipefail",
      ("docker exec tokenkey test -f " + ($log_path | @sh)),
      ("docker exec tokenkey gzip -c " + ($log_path | @sh) + " > " + ($remote_gz | @sh)),
      ("echo " + $b64 + " | base64 -d > /tmp/gw-debug-put.url"),
      ("curl -fS --max-time 3600 -X PUT --upload-file " + ($remote_gz | @sh) + " \"$(cat /tmp/gw-debug-put.url)\""),
      "rm -f /tmp/gw-debug-put.url",
      ("wc -c < " + ($remote_gz | @sh) + " | tr -d \" \\n\"")
    ]
  }' > "$PARAMS"

log "target=$TARGET region=$REGION instance=$INSTANCE_ID"
log "remote=$LOG_PATH → s3://$BUCKET/$S3_KEY → $LOCAL_LOG"

CMD_ID=$(aws ssm send-command \
  --region "$REGION" \
  --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --comment "fetch gateway_debug.log ($EDGE_LABEL)" \
  --timeout-seconds "$WAIT_MAX" \
  --parameters "file://$PARAMS" \
  --query 'Command.CommandId' --output text)

log "command_id=$CMD_ID (waiting up to ${WAIT_MAX}s)…"
DEADLINE=$(( $(date +%s) + WAIT_MAX ))
while true; do
  STATUS=$(aws ssm get-command-invocation \
    --region "$REGION" --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)
  case "$STATUS" in
    Success|Failed|TimedOut|Cancelled) break ;;
  esac
  if [ "$(date +%s)" -ge "$DEADLINE" ]; then
    STATUS=TimedOut
    break
  fi
  sleep 8
done

if [ "$STATUS" != "Success" ]; then
  err "remote status=$STATUS"
  aws ssm get-command-invocation \
    --region "$REGION" --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
    --query '{stderr:StandardErrorContent,stdout:StandardOutputContent}' --output text >&2 || true
  exit 1
fi

REMOTE_BYTES=$(aws ssm get-command-invocation \
  --region "$REGION" --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
  --query 'StandardOutputContent' --output text | tr -d '[:space:]')
log "remote gzip bytes=$REMOTE_BYTES"

log "downloading s3://$BUCKET/$S3_KEY"
aws s3 cp --region "$S3_REGION" "s3://${BUCKET}/${S3_KEY}" "$LOCAL_GZ"
gunzip -f "$LOCAL_GZ"
BYTES=$(wc -c < "$LOCAL_LOG" | tr -d ' ')
log "saved $LOCAL_LOG (${BYTES} bytes)"

aws s3 rm --region "$S3_REGION" "s3://${BUCKET}/${S3_KEY}" >/dev/null 2>&1 || true
aws ssm send-command \
  --region "$REGION" \
  --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --comment "cleanup gateway debug tmp ($EDGE_LABEL)" \
  --parameters "{\"commands\":[\"rm -f '$REMOTE_GZ'\"]}" \
  --query 'Command.CommandId' --output text >/dev/null 2>&1 || true

printf '%s\n' "$LOCAL_LOG"
