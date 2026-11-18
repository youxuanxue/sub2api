#!/usr/bin/env bash
# Build the error_clustering binary inside a transient golang:1.27.4-alpine
# Daily transport: TAT via execute_remote_params.py (TOKENKEY_REMOTE_VIA default).
# Break-glass: TOKENKEY_REMOTE_VIA=ssm.
# container on the prod host (native host architecture), then install it to
# /usr/local/bin/error_clustering. Image tag must satisfy
# tools/error_clustering/go.mod (go >= 1.27.4); GOTOOLCHAIN=local in the
# official image will not auto-fetch a newer toolchain. The Ops Daily
# Diagnostics error clustering job then invokes it via SSM + a transient
# docker container attached to the tokenkey_default network.
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
    --node "${PROD_SSM_NODE:-app-old}" \
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
    "sudo docker run --rm -v \"$PWD\":/src -w /src --user 0:0 golang:1.27.4-alpine sh -c \"go mod download && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags=\\\"-s -w\\\" -o /src/error_clustering ./\"",
    "sudo install -m 0755 -o root -g root error_clustering /usr/local/bin/error_clustering",
    "/usr/local/bin/error_clustering --help 2>&1 || true",
    "sha256sum /usr/local/bin/error_clustering",
    "rm -rf \"$TMPD\""
  ]
}' > "$PARAMS_FILE"

# Hybrid/Ubuntu SSM RunShellScript starts under dash, which rejects pipefail.
# Same wrap owner as deploy_via_ssm / sync-*-via-ssm.
python3 "$REPO_ROOT/ops/stage0/ssm_wrap_bash_commands.py" "$PARAMS_FILE"

stdout_file="${SSM_STDOUT_FILE:-$WORKDIR/stdout.txt}"
stderr_file="${SSM_STDERR_FILE:-$WORKDIR/stderr.txt}"

if [[ -n "${STAGE0_SSM_OUTPUT_DIR:-}" || "${STAGE0_RENDER_ONLY:-}" == "1" ]]; then
  echo "[info] wrote $PARAMS_FILE (render-only; no remote)"
  exit 0
fi

export TOKENKEY_REMOTE_VIA="${TOKENKEY_REMOTE_VIA:-tat}"
if ! python3 "$REPO_ROOT/ops/stage0/execute_remote_params.py" \
  --params-file "$PARAMS_FILE" \
  --instance-id "$INSTANCE_ID" \
  --comment "deploy error_clustering binary" \
  --stdout-file "$stdout_file" \
  --stderr-file "$stderr_file"; then
  echo "deploy error_clustering binary: remote failed" >&2
  cat "$stderr_file" >&2 || true
  exit 1
fi
cat "$stdout_file"
