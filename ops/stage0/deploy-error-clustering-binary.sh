#!/usr/bin/env bash
# Build the error_clustering binary inside a transient golang:1.26-alpine
# container on the prod host (Graviton arm64), then install it to
# /usr/local/bin/error_clustering. The Ops Daily Diagnostics error clustering job then
# invokes it via SSM + a transient docker container attached to the
# tokenkey_default network.
#
# Idempotent: re-running rebuilds and replaces the binary.
#
# Required:
#   AWS credentials with SSM SendCommand on the target instance.
#   Instance resolve: INSTANCE_ID env, or cutover-aware resolve_prod_ssm_target
#   (PROD_SSM_TARGET / STACK defaults).
#
# This script is designed to be runnable both locally (operator) and from CI
# (after AWS OIDC).

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
REGION=${AWS_REGION:-us-east-1}
STACK=${STACK:-tokenkey-prod-stage0}
PROD_SSM_TARGET="${PROD_SSM_TARGET:-auto}"
INSTANCE_ID="${INSTANCE_ID:-}"

if ! command -v aws >/dev/null 2>&1; then
  echo "aws CLI not found" >&2
  exit 1
fi

SRC_DIR="$REPO_ROOT/tools/error_clustering"
if [[ ! -f "$SRC_DIR/main.go" ]]; then
  echo "missing $SRC_DIR/main.go" >&2
  exit 1
fi

if [[ -z "$INSTANCE_ID" ]]; then
  RESOLVED_JSON="$(python3 "$REPO_ROOT/ops/stage0/resolve_prod_ssm_target.py" \
    --target "${PROD_SSM_TARGET}" \
    --stack "$STACK" \
    --format json)"
  INSTANCE_ID="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["instance_id"])' <<<"$RESOLVED_JSON")"
  REGION="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["ssm_region"])' <<<"$RESOLVED_JSON")"
fi
if [[ -z "$INSTANCE_ID" || "$INSTANCE_ID" == "None" ]]; then
  echo "could not resolve prod SSM instance (INSTANCE_ID / resolve_prod_ssm_target)" >&2
  exit 1
fi
case "$INSTANCE_ID" in
  i-* | mi-*) ;;
  *)
    echo "expected i-* or mi-* instance id, got $INSTANCE_ID" >&2
    exit 1
    ;;
esac

# Tar + base64 the standalone module (~5 KB total) so it fits in one SSM payload.
TAR_B64=$(cd "$REPO_ROOT" && tar czf - -C tools error_clustering | base64 | tr -d '\n')

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT
PARAMS_FILE="$WORKDIR/params.json"

# jq builds the JSON document so the embedded base64 cannot break shell quoting.
jq -n --arg b64 "$TAR_B64" '{
  commands: [
    "set -euo pipefail",
    "TMPD=$(mktemp -d)",
    "cd \"$TMPD\"",
    ("echo \($b64) | base64 -d | tar xz"),
    "cd error_clustering",
    "sudo docker run --rm -v \"$PWD\":/src -w /src --user 0:0 golang:1.26-alpine sh -c \"go mod download && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags=\\\"-s -w\\\" -o /src/error_clustering ./\"",
    "sudo install -m 0755 -o root -g root error_clustering /usr/local/bin/error_clustering",
    "/usr/local/bin/error_clustering --help 2>&1 || true",
    "sha256sum /usr/local/bin/error_clustering",
    "rm -rf \"$TMPD\""
  ]
}' > "$PARAMS_FILE"

CMD_ID=$(aws ssm send-command \
  --region "$REGION" \
  --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --comment "deploy error_clustering binary" \
  --parameters "file://$PARAMS_FILE" \
  --query 'Command.CommandId' --output text)

echo "SSM command-id: $CMD_ID instance=$INSTANCE_ID region=$REGION"
echo "polling until completion..."

# AWS_SSM_WAIT_MAX seconds total (default 240); poll every 5s. SSM's built-in
# wait command-executed has a fixed 600s ceiling and tight 20s polling that
# tends to time out; we roll our own loop to surface output sooner.
DEADLINE=$(( $(date +%s) + ${AWS_SSM_WAIT_MAX:-240} ))
while true; do
  STATUS=$(aws ssm get-command-invocation --region "$REGION" \
    --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
    --query 'Status' --output text 2>/dev/null || echo "InProgress")
  case "$STATUS" in
    Success|Cancelled|TimedOut|Failed) break ;;
  esac
  if [[ $(date +%s) -ge $DEADLINE ]]; then
    echo "timed out waiting for SSM command" >&2
    break
  fi
  sleep 5
done

aws ssm get-command-invocation --region "$REGION" \
  --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
  --query '{Status:Status,Stdout:StandardOutputContent,Stderr:StandardErrorContent}' \
  --output json

if [[ "$STATUS" != "Success" ]]; then
  exit 1
fi
