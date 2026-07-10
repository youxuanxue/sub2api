#!/usr/bin/env bash
# Direct ChatGPT Codex upstream probe for one OpenAI OAuth account.
# This bypasses the TokenKey gateway/catalog/model_mapping floor. Use it only to
# prove raw upstream account capability; gateway probes remain TokenKey-path proof.
set -euo pipefail

ACCOUNT_ID="${ACCOUNT_ID:?ACCOUNT_ID required}"
MODEL="${MODEL:?MODEL required}"
PROMPT_TEXT="${PROMPT_TEXT:-Reply OK only.}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-90}"
UPSTREAM_URL="${UPSTREAM_URL:-https://chatgpt.com/backend-api/codex/responses}"
DEFAULT_CODEX_UA="codex-tui/0.143.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.11 (codex-tui; 0.143.0)"
CODEX_USER_AGENT="${CODEX_USER_AGENT:-$DEFAULT_CODEX_UA}"
CODEX_VERSION="${CODEX_VERSION:-0.143.0}"

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
if [[ ! "$REQUEST_TIMEOUT_SECONDS" =~ ^[0-9]+$ ]] || [[ "$REQUEST_TIMEOUT_SECONDS" -lt 1 ]]; then
  fail_json "REQUEST_TIMEOUT_SECONDS must be a positive integer"
fi

psql_err="$(mktemp)"
if ! account_json="$("${PSQL[@]}" -c "
SELECT COALESCE(row_to_json(t)::text, '')
FROM (
  SELECT
    id,
    COALESCE(name, '') AS name,
    COALESCE(platform, '') AS platform,
    COALESCE(type, '') AS type,
    COALESCE(credentials->>'access_token', '') AS access_token,
    COALESCE(credentials->>'user_agent', '') AS user_agent,
    COALESCE(credentials->>'chatgpt_account_id', '') AS chatgpt_account_id,
    COALESCE(credentials->>'chatgpt_account_is_fedramp', '') AS chatgpt_account_is_fedramp
  FROM accounts
  WHERE id = ${ACCOUNT_ID} AND deleted_at IS NULL
) t;
" 2>"$psql_err")"; then
  err="$(tr '\n' ' ' < "$psql_err" | sed -E 's/(password|token|secret|key)[^ ]*/\1=<redacted>/Ig' | cut -c1-500)"
  rm -f "$psql_err"
  fail_json "account lookup failed: ${err:-psql exited non-zero}"
fi
rm -f "$psql_err"
account_json="$(printf '%s' "$account_json" | tr -d '\n')"

if [[ -z "$account_json" ]]; then
  fail_json "account ${ACCOUNT_ID} not found"
fi

mapfile -t account_fields < <(python3 - "$account_json" <<'PY'
import json, sys
obj = json.loads(sys.argv[1])
for key in (
    "access_token",
    "name",
    "platform",
    "type",
    "user_agent",
    "chatgpt_account_id",
    "chatgpt_account_is_fedramp",
):
    print(obj.get(key) or "")
PY
)

ACCESS_TOKEN="${account_fields[0]}"
ACCOUNT_NAME="${account_fields[1]}"
PLATFORM="${account_fields[2]}"
ACCOUNT_TYPE="${account_fields[3]}"
CUSTOM_USER_AGENT="${account_fields[4]}"
CHATGPT_ACCOUNT_ID="${account_fields[5]}"
CHATGPT_ACCOUNT_IS_FEDRAMP="${account_fields[6]}"

if [[ -z "$ACCESS_TOKEN" ]]; then
  fail_json "account ${ACCOUNT_ID} missing access_token (refresh may be required)"
fi
if [[ -n "$CUSTOM_USER_AGENT" ]]; then
  CODEX_USER_AGENT="$CUSTOM_USER_AGENT"
fi

payload="$(python3 - "$MODEL" "$PROMPT_TEXT" <<'PY'
import json, sys
print(json.dumps({
    "model": sys.argv[1],
    "input": [{
        "role": "user",
        "content": [{"type": "input_text", "text": sys.argv[2]}],
    }],
    "instructions": "You are ChatGPT, a large language model trained by OpenAI.",
    "stream": True,
    "store": False,
}, ensure_ascii=False))
PY
)"

tmp_body="$(mktemp)"
tmp_hdr="$(mktemp)"
curl_args=(
  -sS
  -m "$REQUEST_TIMEOUT_SECONDS"
  -o "$tmp_body"
  -D "$tmp_hdr"
  -w '%{http_code}'
  -H "Authorization: Bearer ${ACCESS_TOKEN}"
  -H "Content-Type: application/json"
  -H "Accept: text/event-stream"
  -H "OpenAI-Beta: responses=experimental"
  -H "Originator: codex_cli_rs"
  -H "Version: ${CODEX_VERSION}"
  -H "User-Agent: ${CODEX_USER_AGENT}"
  -X POST "$UPSTREAM_URL"
  --data-binary "$payload"
)
if [[ -n "$CHATGPT_ACCOUNT_ID" ]]; then
  curl_args+=(-H "chatgpt-account-id: ${CHATGPT_ACCOUNT_ID}")
fi
case "$(printf '%s' "$CHATGPT_ACCOUNT_IS_FEDRAMP" | tr '[:upper:]' '[:lower:]')" in
  true|t|1|yes|y) curl_args+=(-H "x-openai-fedramp: true") ;;
esac

if ! http_code="$(curl "${curl_args[@]}")"; then
  http_code="000"
fi

body_excerpt="$(python3 - "$tmp_body" <<'PY'
import json, pathlib, sys
raw = pathlib.Path(sys.argv[1]).read_text(errors="replace").strip()
if not raw:
    print("")
    raise SystemExit
try:
    obj = json.loads(raw)
    print(json.dumps(obj, ensure_ascii=False)[:700])
except json.JSONDecodeError:
    print(raw[:700])
PY
)"

rm -f "$tmp_body" "$tmp_hdr"

python3 - "$ACCOUNT_ID" "$ACCOUNT_NAME" "$PLATFORM" "$ACCOUNT_TYPE" "$MODEL" "$UPSTREAM_URL" "$http_code" "$body_excerpt" <<'PY'
import json, sys

account_id, account_name, platform, account_type, model, upstream_url, http_code, body_excerpt = sys.argv[1:9]
http_code = str(http_code)
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
        "kind": "openai_upstream_direct",
        "account_id": int(account_id),
        "account_name": account_name,
        "platform": platform,
        "account_type": account_type,
        "model": model,
        "upstream_url": upstream_url,
        "endpoint": "chatgpt_codex_responses",
    },
    "response": {"body_excerpt": body_excerpt},
}, ensure_ascii=False, indent=2))
PY
