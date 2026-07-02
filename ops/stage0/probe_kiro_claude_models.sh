#!/usr/bin/env bash
# Batch-probe Claude model IDs against one Kiro account via the gateway probe harness.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ACCOUNT_ID="${ACCOUNT_ID:?ACCOUNT_ID required}"
MODELS="${MODELS:-claude-haiku-4-5-20251001 claude-haiku-4-5 claude-sonnet-4-5 claude-sonnet-4-6 claude-sonnet-5 claude-opus-4-5 claude-opus-4-6 claude-opus-4-7 claude-opus-4-8 claude-opus-5}"
MAX_TOKENS="${MAX_TOKENS:-8}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-60}"

results=()
for model in $MODELS; do
  out="$(ACCOUNT_ID="$ACCOUNT_ID" MODEL="$model" ENDPOINT=messages MAX_TOKENS="$MAX_TOKENS" \
    REQUEST_TIMEOUT_SECONDS="$REQUEST_TIMEOUT_SECONDS" PROBE_REUSE_MODE=1 \
    bash "$SCRIPT_DIR/probe_account_model.sh" 2>/dev/null || true)"
  verdict="$(printf '%s' "$out" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("verdict","parse_error"))' 2>/dev/null || echo parse_error)"
  http_code="$(printf '%s' "$out" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("http_status",""))' 2>/dev/null || echo "")"
  err_msg="$(printf '%s' "$out" | python3 -c 'import json,sys; d=json.load(sys.stdin); print((d.get("response_body_excerpt") or d.get("error") or "")[:120])' 2>/dev/null || echo "")"
  printf '{"model":"%s","verdict":"%s","http_status":"%s","detail":"%s"}\n' \
    "$model" "$verdict" "$http_code" "$(printf '%s' "$err_msg" | sed 's/"/\\"/g')"
done
