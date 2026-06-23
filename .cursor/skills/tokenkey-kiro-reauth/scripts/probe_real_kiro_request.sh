#!/usr/bin/env bash
set -euo pipefail

GROUP_NAME="${GROUP_NAME:-kiro}"
MODEL="${MODEL:-claude-opus-4-8}"
APP_CONTAINER="${APP_CONTAINER:-tokenkey}"
APP_URL="${APP_URL:-http://localhost:8080}"
MAX_TOKENS="${MAX_TOKENS:-48}"
PROMPT_TEXT="${PROMPT_TEXT:-Say hello in one short sentence.}"
LOG_WINDOW="${LOG_WINDOW:-3m}"

PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'

key="$($PSQL -c "SELECT ak.key
FROM api_keys ak
JOIN groups g ON g.id = ak.group_id
WHERE g.name = '${GROUP_NAME//\'/\'\'}'
  AND g.deleted_at IS NULL
  AND ak.deleted_at IS NULL
ORDER BY ak.id
LIMIT 1" | tr -d '[:space:]')"

if [[ -z "${key}" ]]; then
  echo '{"error":"missing_group_api_key"}'
  exit 0
fi

payload="$(python3 - "${MODEL}" "${MAX_TOKENS}" "${PROMPT_TEXT}" <<'PY'
import json
import sys

model = sys.argv[1]
max_tokens = int(sys.argv[2])
prompt_text = sys.argv[3]

user_id = json.dumps({
    "device_id": "0000000000000000000000000000000000000000000000000000000000000001",
    "account_uuid": "",
    "session_id": "00000000-0000-0000-0000-000000000001",
}, ensure_ascii=False, separators=(",", ":"))

print(json.dumps({
    "model": model,
    "max_tokens": max_tokens,
    "messages": [{"role": "user", "content": prompt_text}],
    "metadata": {"user_id": user_id},
}, ensure_ascii=False, separators=(",", ":")))
PY
)"

tmp_body="$(mktemp)"
tmp_payload="$(mktemp)"
tmp_log="$(mktemp)"
tmp_err="$(mktemp)"
cleanup() {
  rm -f "${tmp_body}" "${tmp_payload}" "${tmp_log}" "${tmp_err}"
  docker exec "${APP_CONTAINER}" rm -f /tmp/kiro-real-request.json /tmp/kiro-real-response.json >/dev/null 2>&1 || true
}
trap cleanup EXIT

printf '%s' "${payload}" >"${tmp_payload}"

http_code=""
if ! http_code="$(docker exec -i "${APP_CONTAINER}" sh -lc '
  cat > /tmp/kiro-real-request.json
  curl -sS -o /tmp/kiro-real-response.json -w "%{http_code}" \
    -H "x-api-key: '"${key}"'" \
    -H "anthropic-version: 2023-06-01" \
    -H "anthropic-beta: claude-code-20250219" \
    -H "X-App: cli" \
    -H "Content-Type: application/json" \
    -H "User-Agent: Claude-Code/1.0.33 (macOS; arm64)" \
    --data-binary @/tmp/kiro-real-request.json \
    "'"${APP_URL}"'/v1/messages"
' <"${tmp_payload}" 2>"${tmp_err}")"; then
  http_code=""
fi

if docker exec "${APP_CONTAINER}" test -f /tmp/kiro-real-response.json >/dev/null 2>&1; then
  docker exec "${APP_CONTAINER}" cat /tmp/kiro-real-response.json >"${tmp_body}"
else
  : >"${tmp_body}"
fi

docker logs "${APP_CONTAINER}" --since "${LOG_WINDOW}" >"${tmp_log}" 2>&1 || true

python3 - "${tmp_body}" "${tmp_log}" "${tmp_err}" "${http_code}" "${GROUP_NAME}" "${MODEL}" "${LOG_WINDOW}" "${PROMPT_TEXT}" <<'PY'
import json
import sys
from datetime import datetime, timezone

body_path, log_path, err_path, http_code, group_name, model, log_window, prompt_text = sys.argv[1:9]

raw_body = open(body_path, encoding="utf-8", errors="replace").read()
curl_error = open(err_path, encoding="utf-8", errors="replace").read().strip()

request_summary = {
    "requested_at_utc": datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
    "group_name": group_name,
    "model": model,
    "prompt_text": prompt_text,
    "http_status": int(http_code) if http_code.isdigit() else None,
}
if curl_error:
    request_summary["curl_error"] = curl_error[:400]

try:
    parsed = json.loads(raw_body) if raw_body else None
except Exception:
    parsed = None

if isinstance(parsed, dict):
    request_summary["type"] = parsed.get("type")
    request_summary["role"] = parsed.get("role")
    if isinstance(parsed.get("content"), list):
        texts = [item.get("text", "") for item in parsed["content"] if isinstance(item, dict) and item.get("type") == "text"]
        request_summary["text_prefix"] = ("".join(texts))[:240]
    request_summary["stop_reason"] = parsed.get("stop_reason")
    if isinstance(parsed.get("usage"), dict):
        request_summary["usage_keys"] = sorted(parsed["usage"].keys())
    if isinstance(parsed.get("error"), dict):
        request_summary["error_type"] = parsed["error"].get("type")
        request_summary["error_message"] = parsed["error"].get("message")
else:
    request_summary["raw_prefix"] = raw_body[:240]

recent_rows = []
with open(log_path, encoding="utf-8", errors="replace") as handle:
    for raw in handle:
        raw = raw.strip()
        if not raw.startswith("{"):
            continue
        try:
            obj = json.loads(raw)
        except Exception:
            continue
        if (obj.get("msg") or obj.get("message")) != "http request completed":
            continue
        if obj.get("path") != "/v1/messages":
            continue
        if obj.get("platform") != "kiro" and obj.get("billing_platform") != "kiro":
            continue
        recent_rows.append({
            "account_id": obj.get("account_id"),
            "platform": obj.get("platform"),
            "billing_platform": obj.get("billing_platform"),
            "status_code": obj.get("status_code"),
            "model": obj.get("model"),
            "completed_at": obj.get("completed_at"),
            "latency_ms": obj.get("latency_ms"),
        })

print(json.dumps({
    "request": request_summary,
    "recent_kiro_access": {
        "window": log_window,
        "count": len(recent_rows),
        "rows": recent_rows[-10:],
    },
}, ensure_ascii=False, indent=2))
PY
