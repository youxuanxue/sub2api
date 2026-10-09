#!/usr/bin/env bash
# Direct ChatGPT Codex upstream image generation probe for one OpenAI OAuth account.
# STRICT CONTRACT: Never logs, prints, or exposes credentials or access tokens.
#
# Companion matrix probes (same Responses + local fidelity path):
#   ops/stage0/probe_openai_image_fidelity_matrix.sh
#   ops/stage0/probe_openai_image_ar_07_09.sh
set -euo pipefail

ACCOUNT_ID="${ACCOUNT_ID:?ACCOUNT_ID required}"
IMAGE_MODEL="${IMAGE_MODEL:-gpt-image-2}"
MAIN_MODEL="${MAIN_MODEL:-gpt-5.6-luna}"
PROMPT_TEXT="${PROMPT_TEXT:-A tiny yellow lemon on a clean white table, minimalist illustration}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-120}"
UPSTREAM_URL="${UPSTREAM_URL:-https://chatgpt.com/backend-api/codex/responses}"
# Optional image_generation tool fields (codex2api / TK Responses path).
SIZE="${SIZE:-}"
OUTPUT_FORMAT="${OUTPUT_FORMAT:-}"
QUALITY="${QUALITY:-}"
BACKGROUND="${BACKGROUND:-}"
OUTPUT_COMPRESSION="${OUTPUT_COMPRESSION:-}"
# Defaults must match DefaultOpenAICodexVersion (setting_gateway_runtime.go).
DEFAULT_CODEX_UA="codex-tui/0.162.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.11 (codex-tui; 0.162.0)"
CODEX_USER_AGENT="${CODEX_USER_AGENT:-$DEFAULT_CODEX_UA}"
CODEX_VERSION="${CODEX_VERSION:-0.162.0}"

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

payload="$(python3 - "$MAIN_MODEL" "$IMAGE_MODEL" "$PROMPT_TEXT" "$SIZE" "$OUTPUT_FORMAT" "$QUALITY" "$BACKGROUND" "$OUTPUT_COMPRESSION" <<'PY'
import json, sys
main_model, image_model, prompt, size, output_format, quality, background, output_compression = sys.argv[1:9]
tool = {
    "type": "image_generation",
    "action": "generate",
    "model": image_model,
}
for key, value in (
    ("size", size),
    ("output_format", output_format),
    ("quality", quality),
    ("background", background),
):
    if value.strip():
        tool[key] = value.strip()
if output_compression.strip():
    tool["output_compression"] = int(output_compression.strip())
# Match TokenKey / codex2api: empty instructions; fidelity lives in tool fields.
data = {
    "model": main_model,
    "instructions": "",
    "stream": True,
    "reasoning": {"effort": "medium", "summary": "auto"},
    "parallel_tool_calls": True,
    "include": ["reasoning.encrypted_content"],
    "store": False,
    "tool_choice": {"type": "image_generation"},
    "tools": [tool],
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
  -H "Originator: codex-tui"
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
call_meta = {}
tool_meta = {}
usage = {}
tool_usage = {}

def absorb_call_meta(src):
    if not isinstance(src, dict):
        return
    for key in (
        "size",
        "quality",
        "output_format",
        "background",
        "model",
        "action",
        "status",
        "revised_prompt",
    ):
        val = src.get(key)
        if val is not None and val != "":
            call_meta[key] = val
    if src.get("result"):
        global image_data
        image_data = src.get("result")
    if src.get("revised_prompt"):
        global revised_prompt
        revised_prompt = src.get("revised_prompt")

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
                        absorb_call_meta(item)
                    # Also check output list in completed event
                    resp = obj.get("response", {})
                    if resp.get("usage"):
                        usage = resp.get("usage") or usage
                    if resp.get("tool_usage"):
                        tool_usage = resp.get("tool_usage") or tool_usage
                    for tool in resp.get("tools") or []:
                        if isinstance(tool, dict) and tool.get("type") == "image_generation":
                            for key in ("size", "quality", "output_format", "background", "model"):
                                val = tool.get(key)
                                if val is not None and val != "":
                                    tool_meta[key] = val
                    for out in resp.get("output", []):
                        if out.get("type") == "image_generation_call":
                            absorb_call_meta(out)
                except Exception:
                    pass
except Exception as e:
    error_msg = str(e)

image_valid_png = False
image_container = ""
image_bytes_len = 0
image_sha256 = ""
img_w, img_h = 0, 0

if image_data:
    try:
        decoded = base64.b64decode(image_data)
        image_bytes_len = len(decoded)
        image_sha256 = hashlib.sha256(decoded).hexdigest()
        png_sig = bytes([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])
        if decoded.startswith(png_sig) and len(decoded) >= 24:
            image_valid_png = True
            image_container = "png"
            img_w, img_h = struct.unpack(">II", decoded[16:24])
        elif len(decoded) >= 3 and decoded[0] == 0xFF and decoded[1] == 0xD8 and decoded[2] == 0xFF:
            image_container = "jpeg"
            # Soft parse SOF0/SOF2 for dimensions
            i = 2
            while i + 9 < len(decoded):
                if decoded[i] != 0xFF:
                    i += 1
                    continue
                marker = decoded[i + 1]
                if marker in (0xC0, 0xC2) and i + 9 < len(decoded):
                    img_h, img_w = struct.unpack(">HH", decoded[i + 5 : i + 9])
                    break
                if marker == 0xD9 or marker == 0xDA:
                    break
                if i + 3 >= len(decoded):
                    break
                seglen = struct.unpack(">H", decoded[i + 2 : i + 4])[0]
                i += 2 + seglen
        elif len(decoded) >= 12 and decoded[:4] == b"RIFF" and decoded[8:12] == b"WEBP":
            image_container = "webp"
            # VP8X / VP8 / VP8L dimension parse (best-effort)
            if decoded[12:16] == b"VP8X" and len(decoded) >= 30:
                w = 1 + decoded[24] + (decoded[25] << 8) + (decoded[26] << 16)
                h = 1 + decoded[27] + (decoded[28] << 8) + (decoded[29] << 16)
                img_w, img_h = w, h
            elif decoded[12:16] == b"VP8 " and len(decoded) >= 30:
                img_w = struct.unpack("<H", decoded[26:28])[0] & 0x3FFF
                img_h = struct.unpack("<H", decoded[28:30])[0] & 0x3FFF
            elif decoded[12:16] == b"VP8L" and len(decoded) >= 25:
                bits = struct.unpack("<I", decoded[21:25])[0]
                img_w = (bits & 0x3FFF) + 1
                img_h = ((bits >> 14) & 0x3FFF) + 1
    except Exception as e:
        error_msg = f"base64 decode error: {e}"

dims = f"{img_w}x{img_h}" if img_w and img_h else ""
if str(http_code).startswith("2") and (image_valid_png or image_container in {"jpeg", "webp"}):
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
        "container": image_container,
        "size_bytes": image_bytes_len,
        "dimensions": dims,
        "width": img_w if img_w else 0,
        "height": img_h if img_h else 0,
        "sha256": image_sha256,
        "revised_prompt": revised_prompt,
        "call_meta": call_meta,
        "tool_meta": tool_meta,
        "usage": usage,
        "tool_usage": tool_usage,
    },
    "events_streamed": events[:15],
}

if not image_data and raw_lines:
    raw_snippet = "".join(raw_lines[:20])[:600]
    result["response_snippet"] = raw_snippet

print(json.dumps(result, ensure_ascii=False, indent=2))
PY

rm -f "$tmp_body" "$tmp_hdr"
