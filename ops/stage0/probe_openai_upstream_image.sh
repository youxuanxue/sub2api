#!/usr/bin/env bash
# Direct ChatGPT Codex upstream image generation probe for one OpenAI OAuth account.
# STRICT CONTRACT: Never logs, prints, or exposes credentials or access tokens.
set -euo pipefail

ACCOUNT_ID="${ACCOUNT_ID:?ACCOUNT_ID required}"
IMAGE_MODEL="${IMAGE_MODEL:-gpt-image-2}"
MAIN_MODEL="${MAIN_MODEL:-gpt-5.6-luna}"
PROMPT_TEXT="${PROMPT_TEXT:-A tiny yellow lemon on a clean white table, minimalist illustration}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-120}"
UPSTREAM_URL="${UPSTREAM_URL:-https://chatgpt.com/backend-api/codex/responses}"
DEFAULT_CODEX_UA="codex-tui/0.154.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"
CODEX_USER_AGENT="${CODEX_USER_AGENT:-$DEFAULT_CODEX_UA}"
CODEX_VERSION="${CODEX_VERSION:-0.154.0}"

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
  err="$(tr '
' ' ' < "$psql_err" | sed -E 's/(password|token|secret|key)[^ ]*/\1=<redacted>/Ig' | cut -c1-500)"
  rm -f "$psql_err"
  fail_json "account lookup failed: ${err:-psql exited non-zero}"
fi
rm -f "$psql_err"
account_json="$(printf '%s' "$account_json" | tr -d '
')"

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

payload="$(python3 - "$MAIN_MODEL" "$IMAGE_MODEL" "$PROMPT_TEXT" <<'PY'
import json, sys
main_model, image_model, prompt = sys.argv[1:4]
data = {
    "model": main_model,
    "instructions": "When invoking the image_generation tool, use the user's image prompt verbatim. Do not rewrite, expand, summarize, embellish, translate, normalize punctuation, or add or remove visual details or constraints. Preserve the original language, wording, capitalization, quotes, and punctuation exactly.",
    "stream": True,
    "reasoning": {"effort": "medium", "summary": "auto"},
    "parallel_tool_calls": True,
    "include": ["reasoning.encrypted_content"],
    "store": False,
    "tool_choice": {"type": "image_generation"},
    "tools": [
        {
            "type": "image_generation",
            "action": "generate",
            "model": image_model,
        }
    ],
    "input": [
        {
            "type": "message",
            "role": "user",
            "content": [
                {"type": "input_text", "text": prompt}
            ],
        }
    ],
}
print(json.dumps(data, ensure_ascii=False))
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
  -H "Originator: codex_cli"
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

python3 - "$ACCOUNT_ID" "$ACCOUNT_NAME" "$PLATFORM" "$ACCOUNT_TYPE" "$MAIN_MODEL" "$IMAGE_MODEL" "$UPSTREAM_URL" "$http_code" "$tmp_body" <<'PY'
import base64
import hashlib
import json
import struct
import sys

account_id, account_name, platform, account_type, main_model, image_model, upstream_url, http_code, body_file = sys.argv[1:10]

events = []
image_data = None
revised_prompt = ""
error_msg = ""
raw_lines = []

try:
    with open(body_file, "r", encoding="utf-8", errors="replace") as f:
        for line in f:
            raw_lines.append(line)
            line = line.strip()
            if line.startswith("data: "):
                data_str = line[6:].strip()
                if data_str == "[DONE]":
                    break
                try:
                    obj = json.loads(data_str)
                    etype = obj.get("type", "")
                    if etype:
                        events.append(etype)
                    # Check for image_generation_call in output_item
                    item = obj.get("item", {})
                    if item.get("type") == "image_generation_call":
                        image_data = item.get("result")
                        revised_prompt = item.get("revised_prompt", "")
                    # Also check output list in completed event
                    resp = obj.get("response", {})
                    for out in resp.get("output", []):
                        if out.get("type") == "image_generation_call" and out.get("result"):
                            image_data = out.get("result")
                            revised_prompt = out.get("revised_prompt", "")
                except Exception:
                    pass
except Exception as e:
    error_msg = str(e)

image_valid_png = False
image_bytes_len = 0
image_sha256 = ""
img_w, img_h = 0, 0

if image_data:
    try:
        decoded = base64.b64decode(image_data)
        image_bytes_len = len(decoded)
        image_sha256 = hashlib.sha256(decoded).hexdigest()
        # Verify PNG signature (\x89PNG\r\n\x1a\n)
        png_sig = bytes([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])
        if decoded.startswith(png_sig) and len(decoded) >= 24:
            image_valid_png = True
            img_w, img_h = struct.unpack(">II", decoded[16:24])
    except Exception as e:
        error_msg = f"base64 decode error: {e}"

if str(http_code).startswith("2") and image_valid_png:
    verdict = "servable_image_generated"
elif str(http_code).startswith("2") and image_data:
    verdict = "servable_image_returned"
elif str(http_code).startswith("2"):
    verdict = "upstream_ok_but_no_image"
elif http_code in {"401", "403"}:
    verdict = "upstream_auth_rejected"
elif http_code in {"400", "404", "422"}:
    verdict = "upstream_rejected"
elif http_code == "000":
    verdict = "setup_error"
else:
    verdict = "upstream_error"

result = {
    "verdict": verdict,
    "http_code": str(http_code),
    "probe": {
        "account_id": int(account_id),
        "account_name": account_name,
        "platform": platform,
        "account_type": account_type,
        "main_model": main_model,
        "image_model": image_model,
        "upstream_url": upstream_url,
    },
    "image_generation": {
        "image_received": bool(image_data),
        "is_valid_png": image_valid_png,
        "size_bytes": image_bytes_len,
        "dimensions": f"{img_w}x{img_h}" if image_valid_png else "",
        "sha256": image_sha256,
        "revised_prompt": revised_prompt,
    },
    "events_streamed": events[:15],
}

if not image_data and raw_lines:
    raw_snippet = "".join(raw_lines[:20])[:600]
    result["response_snippet"] = raw_snippet

print(json.dumps(result, ensure_ascii=False, indent=2))
PY

rm -f "$tmp_body" "$tmp_hdr"
