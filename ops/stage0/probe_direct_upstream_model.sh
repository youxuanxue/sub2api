#!/usr/bin/env bash
# Dispatch to a platform-specific direct upstream probe. This intentionally never
# falls back to the TokenKey gateway, because gateway model floors can create
# false negatives for raw provider account capability.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLATFORM="${PLATFORM:-}"
ACCOUNT_ID="${ACCOUNT_ID:?ACCOUNT_ID required}"
MODEL="${MODEL:?MODEL required}"

PSQL=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -q -A -t -v ON_ERROR_STOP=1)

fail_json() {
  python3 - "$1" "$PLATFORM" "$ACCOUNT_ID" "$MODEL" <<'PY'
import json, sys
error, platform, account_id, model = sys.argv[1:5]
probe = {
    "kind": "direct_upstream_dispatch",
    "platform": platform,
    "account_id": account_id,
    "model": model,
}
print(json.dumps({"verdict": "setup_error", "error": error, "probe": probe}, ensure_ascii=False))
PY
  exit 0
}

if [[ ! "$ACCOUNT_ID" =~ ^[0-9]+$ ]]; then
  fail_json "ACCOUNT_ID must be numeric"
fi

if [[ -z "$PLATFORM" ]]; then
  psql_err="$(mktemp)"
  if ! PLATFORM="$("${PSQL[@]}" -c "
SELECT COALESCE(platform, '')
FROM accounts
WHERE id = ${ACCOUNT_ID} AND deleted_at IS NULL;
" 2>"$psql_err")"; then
    err="$(tr '\n' ' ' < "$psql_err" | sed -E 's/(password|token|secret|key)[^ ]*/\1=<redacted>/Ig' | cut -c1-500)"
    rm -f "$psql_err"
    fail_json "account platform lookup failed: ${err:-psql exited non-zero}"
  fi
  rm -f "$psql_err"
  PLATFORM="$(printf '%s' "$PLATFORM" | tr -d '[:space:]')"
fi

normalized_platform="$(printf '%s' "$PLATFORM" | tr '[:upper:]' '[:lower:]' | tr '_' '-')"
case "$normalized_platform" in
  openai|chatgpt|codex|openai-oauth)
    target="${SCRIPT_DIR}/probe_openai_upstream_model.sh"
    ;;
  grok|xai|x-ai)
    target="${SCRIPT_DIR}/probe_grok_upstream_model.sh"
    ;;
  *)
    fail_json "no direct upstream probe implemented for platform ${PLATFORM:-<empty>}; add a platform-specific script instead of falling back to TokenKey gateway"
    ;;
esac

if [[ ! -f "$target" ]]; then
  fail_json "direct upstream probe script is missing: ${target}"
fi

exec bash "$target"
