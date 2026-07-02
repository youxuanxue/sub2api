#!/usr/bin/env bash
# Batch-probe Claude model IDs against one Kiro account via the gateway probe harness.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ACCOUNT_ID="${ACCOUNT_ID:?ACCOUNT_ID required}"
MODELS="${MODELS:-claude-haiku-4-5-20251001 claude-haiku-4-5 claude-sonnet-4-5 claude-sonnet-4-6 claude-sonnet-5 claude-opus-4-5 claude-opus-4-6 claude-opus-4-7 claude-opus-4-8 claude-opus-5}"
MAX_TOKENS="${MAX_TOKENS:-8}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-60}"

for model in $MODELS; do
  err_file="$(mktemp)"
  probe_error=""
  if ! out="$(ACCOUNT_ID="$ACCOUNT_ID" MODEL="$model" ENDPOINT=messages MAX_TOKENS="$MAX_TOKENS" \
    REQUEST_TIMEOUT_SECONDS="$REQUEST_TIMEOUT_SECONDS" PROBE_REUSE_MODE=1 \
    bash "$SCRIPT_DIR/probe_account_model.sh" 2>"$err_file")"; then
    probe_error="$(tr '\n' ' ' <"$err_file" | sed -E 's/[[:space:]]+/ /g' | cut -c1-240)"
  fi
  rm -f "$err_file"
  TK_PROBE_RESULT_JSON="$out" TK_PROBE_ERROR="$probe_error" python3 - "$model" <<'PY'
import json
import os
import re
import sys

model = sys.argv[1]
raw = os.environ.get("TK_PROBE_RESULT_JSON", "")
probe_error = os.environ.get("TK_PROBE_ERROR", "")

try:
    data = json.loads(raw)
except Exception as exc:
    detail = re.sub(r"\s+", " ", probe_error or str(exc)).strip()
    row = {
        "model": model,
        "verdict": "parse_error",
        "http_code": "",
        "detail": detail[:120],
    }
else:
    response = data.get("response") or {}
    detail = response.get("body_excerpt") or data.get("error") or ""
    detail = re.sub(r"\s+", " ", str(detail)).strip()
    row = {
        "model": model,
        "verdict": data.get("verdict", "parse_error"),
        "http_code": data.get("http_code", ""),
        "detail": detail[:120],
    }

print(json.dumps(row, ensure_ascii=False, separators=(",", ":")))
PY
done
