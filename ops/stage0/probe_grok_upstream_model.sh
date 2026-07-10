#!/usr/bin/env bash
# Direct xAI upstream probe for one Grok OAuth account (bypasses TK gateway/catalog).
# Use via run-probe on edge when account model_mapping is empty and gateway probes
# return gateway_rejected for models not yet in SSOT.
set -euo pipefail

ACCOUNT_ID="${ACCOUNT_ID:?ACCOUNT_ID required}"
MODEL="${MODEL:?MODEL required}"
MAX_TOKENS="${MAX_TOKENS:-16}"
PROMPT_TEXT="${PROMPT_TEXT:-Reply OK only.}"
UPSTREAM_BASE="${UPSTREAM_BASE:-https://api.x.ai/v1}"

PSQL=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -q -A -t -v ON_ERROR_STOP=1)

fail_json() {
  python3 - "$1" <<'PY'
import json, sys
print(json.dumps({"verdict": "setup_error", "error": sys.argv[1]}, ensure_ascii=False))
PY
  exit 0
}

if [[ ! "$ACCOUNT_ID" =~ ^[0-9]+$ ]]; then
  fail_json "ACCOUNT_ID must be numeric"
fi
if [[ ! "$MAX_TOKENS" =~ ^[0-9]+$ ]] || [[ "$MAX_TOKENS" -lt 1 ]]; then
  fail_json "MAX_TOKENS must be a positive integer"
fi

psql_err="$(mktemp)"
if ! row="$("${PSQL[@]}" -c "
SELECT COALESCE(credentials->>'access_token', '') || E'\t' ||
       COALESCE(NULLIF(TRIM(credentials->>'base_url'), ''), 'https://api.x.ai/v1') || E'\t' ||
       COALESCE(name, '') || E'\t' ||
       COALESCE(platform, '')
FROM accounts
WHERE id = ${ACCOUNT_ID} AND deleted_at IS NULL;
" 2>"$psql_err")"; then
  err="$(tr '\n' ' ' < "$psql_err" | cut -c1-500)"
  rm -f "$psql_err"
  fail_json "account lookup failed: ${err:-psql exited non-zero}"
fi
rm -f "$psql_err"

if [[ -z "${row//$'\t'/}" ]]; then
  fail_json "account ${ACCOUNT_ID} not found"
fi

IFS=$'\t' read -r ACCESS_TOKEN BASE_URL ACCOUNT_NAME PLATFORM <<<"$row"
ACCESS_TOKEN="${ACCESS_TOKEN//$'\n'/}"
BASE_URL="${BASE_URL//$'\n'/}"
BASE_URL="${BASE_URL%/}"

if [[ -z "$ACCESS_TOKEN" ]]; then
  fail_json "account ${ACCOUNT_ID} missing access_token (refresh may be required)"
fi

if [[ -n "${UPSTREAM_BASE}" ]]; then
  BASE_URL="${UPSTREAM_BASE%/}"
fi

payload="$(python3 - "$MODEL" "$MAX_TOKENS" "$PROMPT_TEXT" <<'PY'
import json, sys
print(json.dumps({
    "model": sys.argv[1],
    "messages": [{"role": "user", "content": sys.argv[3]}],
    "max_tokens": int(sys.argv[2]),
    "stream": False,
}, ensure_ascii=False))
PY
)"

tmp_body="$(mktemp)"
tmp_hdr="$(mktemp)"
http_code="$(
  curl -sS -m 90 \
    -o "$tmp_body" -D "$tmp_hdr" -w '%{http_code}' \
    -H "Authorization: Bearer ${ACCESS_TOKEN}" \
    -H "Content-Type: application/json" \
    -H "Accept: application/json" \
    -X POST "${BASE_URL}/chat/completions" \
    --data-binary "$payload" || echo "000"
)"

body_excerpt="$(python3 - "$tmp_body" <<'PY'
import json, pathlib, sys
raw = pathlib.Path(sys.argv[1]).read_text(errors='replace').strip()
if not raw:
    print("")
    raise SystemExit
try:
    obj = json.loads(raw)
    print(json.dumps(obj, ensure_ascii=False)[:500])
except json.JSONDecodeError:
    print(raw[:500])
PY
)"

rm -f "$tmp_body" "$tmp_hdr"

python3 - "$ACCOUNT_ID" "$ACCOUNT_NAME" "$PLATFORM" "$MODEL" "$BASE_URL" "$http_code" "$body_excerpt" <<'PY'
import json, sys

account_id, account_name, platform, model, base_url, http_code, body_excerpt = sys.argv[1:8]
http_code = str(http_code)
verdict = "setup_error"
if http_code.startswith("2"):
    verdict = "servable"
elif http_code in {"401", "403"}:
    verdict = "upstream_auth_rejected"
elif http_code in {"400", "404", "422"}:
    verdict = "upstream_rejected"
elif http_code == "000":
    verdict = "setup_error"
else:
    verdict = "upstream_rejected"

print(json.dumps({
    "verdict": verdict,
    "http_code": http_code,
    "probe": {
        "kind": "grok_upstream_direct",
        "account_id": int(account_id),
        "account_name": account_name,
        "platform": platform,
        "model": model,
        "upstream_base": base_url,
        "endpoint": "chat/completions",
    },
    "response": {"body_excerpt": body_excerpt},
}, ensure_ascii=False, indent=2))
PY
