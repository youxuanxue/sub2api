#!/usr/bin/env bash
set -euo pipefail

ACCOUNT_ID="${ACCOUNT_ID:-}"
GROUP_NAME="${GROUP_NAME:-kiro}"
MODEL="${MODEL:-claude-opus-4-8}"
APP_CONTAINER="${APP_CONTAINER:-tokenkey}"
APP_URL="${APP_URL:-http://localhost:8080}"
MAX_TOKENS="${MAX_TOKENS:-48}"
PROMPT_TEXT="${PROMPT_TEXT:-Say hello in one short sentence.}"
LOG_WINDOW="${LOG_WINDOW:-3m}"
USAGE_POLL_ATTEMPTS="${USAGE_POLL_ATTEMPTS:-10}"
USAGE_POLL_INTERVAL_SECONDS="${USAGE_POLL_INTERVAL_SECONDS:-1}"

if [[ ! "${ACCOUNT_ID}" =~ ^[0-9]+$ ]]; then
  echo '{"error":"ACCOUNT_ID must be a numeric account id"}'
  exit 0
fi

sql_b64() {
  python3 - "$1" <<'PY'
import base64
import sys

print(base64.b64encode(sys.argv[1].encode("utf-8")).decode("ascii"), end="")
PY
}

GROUP_NAME_B64="$(sql_b64 "${GROUP_NAME}")"
PSQL=(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

context_json="$("${PSQL[@]}" -c "
WITH target AS (
  SELECT a.id, a.name, a.status, a.schedulable, ag.group_id
  FROM accounts a
  JOIN account_groups ag ON ag.account_id = a.id
  JOIN groups g ON g.id = ag.group_id
  WHERE a.id = ${ACCOUNT_ID}
    AND g.name = convert_from(decode('${GROUP_NAME_B64}','base64'),'utf8')
    AND a.platform = 'kiro'
    AND a.type = 'oauth'
    AND a.deleted_at IS NULL
    AND g.deleted_at IS NULL
  ORDER BY g.id
  LIMIT 1
)
SELECT json_build_object(
  'group_id', t.group_id,
  'group_name', convert_from(decode('${GROUP_NAME_B64}','base64'),'utf8'),
  'target_account', json_build_object(
    'account_id', t.id,
    'account_name', t.name,
    'status', t.status,
    'schedulable', t.schedulable
  ),
  'group_api_key', (
    SELECT row_to_json(ak_row)
    FROM (
      SELECT ak.id AS api_key_id, ak.name AS api_key_name
      FROM api_keys ak
      WHERE ak.group_id = t.group_id
        AND ak.deleted_at IS NULL
        AND ak.status = 'active'
      ORDER BY ak.id
      LIMIT 1
    ) ak_row
  ),
  'group_accounts', COALESCE((
    SELECT json_agg(json_build_object(
      'account_id', a2.id,
      'account_name', a2.name,
      'status', a2.status,
      'schedulable', a2.schedulable,
      'priority', ag2.priority,
      'temp_unschedulable_until_utc', a2.temp_unschedulable_until AT TIME ZONE 'UTC',
      'temp_unschedulable_reason', left(COALESCE(a2.temp_unschedulable_reason,''),240),
      'error_message', left(COALESCE(a2.error_message,''),240)
    ) ORDER BY ag2.priority DESC, a2.id)
    FROM account_groups ag2
    JOIN accounts a2 ON a2.id = ag2.account_id
    WHERE ag2.group_id = t.group_id
      AND a2.platform = 'kiro'
      AND a2.type = 'oauth'
      AND a2.deleted_at IS NULL
  ), '[]'::json)
)
FROM target t;
")"
context_json="$(printf '%s' "${context_json}" | tr -d '\n')"

if [[ -z "${context_json}" ]]; then
  python3 - "${ACCOUNT_ID}" "${GROUP_NAME}" <<'PY'
import json
import sys

print(json.dumps({
    "error": "target_group_context_not_found",
    "account_id": int(sys.argv[1]),
    "group_name": sys.argv[2],
}, ensure_ascii=False))
PY
  exit 0
fi

key="$("${PSQL[@]}" -c "
WITH target AS (
  SELECT ag.group_id
  FROM accounts a
  JOIN account_groups ag ON ag.account_id = a.id
  JOIN groups g ON g.id = ag.group_id
  WHERE a.id = ${ACCOUNT_ID}
    AND g.name = convert_from(decode('${GROUP_NAME_B64}','base64'),'utf8')
    AND a.platform = 'kiro'
    AND a.type = 'oauth'
    AND a.deleted_at IS NULL
    AND g.deleted_at IS NULL
  ORDER BY g.id
  LIMIT 1
)
SELECT ak.key
FROM target t
JOIN api_keys ak ON ak.group_id = t.group_id
WHERE ak.deleted_at IS NULL
  AND ak.status = 'active'
ORDER BY ak.id
LIMIT 1;
")"
key="$(printf '%s' "${key}" | tr -d '\n')"

if [[ -z "${key}" ]]; then
  python3 - "${ACCOUNT_ID}" "${GROUP_NAME}" <<'PY'
import json
import sys

print(json.dumps({
    "error": "missing_group_api_key",
    "account_id": int(sys.argv[1]),
    "group_name": sys.argv[2],
}, ensure_ascii=False))
PY
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
tmp_headers="$(mktemp)"
tmp_context="$(mktemp)"
tmp_usage="$(mktemp)"
cleanup() {
  rm -f "${tmp_body}" "${tmp_payload}" "${tmp_log}" "${tmp_err}" "${tmp_headers}" "${tmp_context}" "${tmp_usage}"
  docker exec "${APP_CONTAINER}" rm -f /tmp/kiro-real-request.json /tmp/kiro-real-response.json /tmp/kiro-real-response-headers.txt >/dev/null 2>&1 || true # preflight-allow: swallow
}
trap cleanup EXIT

printf '%s' "${payload}" >"${tmp_payload}"
printf '%s' "${context_json}" >"${tmp_context}"

probe_request_id="kiro-probe-${ACCOUNT_ID}-$(date -u +%Y%m%dT%H%M%SZ)-$$"

http_code=""
if ! http_code="$(docker exec -i \
  -e KIRO_GROUP_API_KEY="${key}" \
  -e KIRO_APP_URL="${APP_URL}" \
  -e KIRO_PROBE_REQUEST_ID="${probe_request_id}" \
  "${APP_CONTAINER}" sh -lc '
  cat > /tmp/kiro-real-request.json
  curl -sS -D /tmp/kiro-real-response-headers.txt -o /tmp/kiro-real-response.json -w "%{http_code}" \
    -H "x-api-key: $KIRO_GROUP_API_KEY" \
    -H "anthropic-version: 2023-06-01" \
    -H "anthropic-beta: claude-code-20250219" \
    -H "X-App: cli" \
    -H "X-Request-ID: $KIRO_PROBE_REQUEST_ID" \
    -H "Content-Type: application/json" \
    -H "User-Agent: Claude-Code/1.0.33 (macOS; arm64)" \
    --data-binary @/tmp/kiro-real-request.json \
    "$KIRO_APP_URL/v1/messages"
' <"${tmp_payload}" 2>"${tmp_err}")"; then
  http_code=""
fi

if docker exec "${APP_CONTAINER}" test -f /tmp/kiro-real-response.json >/dev/null 2>&1; then
  docker exec "${APP_CONTAINER}" cat /tmp/kiro-real-response.json >"${tmp_body}"
else
  : >"${tmp_body}"
fi

if docker exec "${APP_CONTAINER}" test -f /tmp/kiro-real-response-headers.txt >/dev/null 2>&1; then
  docker exec "${APP_CONTAINER}" cat /tmp/kiro-real-response-headers.txt >"${tmp_headers}"
else
  : >"${tmp_headers}"
fi

mapfile -t response_ids < <(python3 - "${tmp_headers}" "${probe_request_id}" <<'PY'
import sys
from pathlib import Path

path, fallback_request_id = sys.argv[1:3]
request_id = fallback_request_id
client_request_id = ""

text = Path(path).read_text(encoding="utf-8", errors="replace") if Path(path).exists() else ""
for raw in text.splitlines():
    line = raw.rstrip("\r")
    if ":" not in line:
        continue
    name, value = line.split(":", 1)
    lname = name.strip().lower()
    if lname == "x-request-id":
        request_id = value.strip() or request_id
    elif lname == "x-client-request-id":
        client_request_id = value.strip()

print(request_id)
print(client_request_id)
PY
)
response_request_id="${response_ids[0]:-${probe_request_id}}"
response_client_request_id="${response_ids[1]:-}"
usage_lookup_request_id=""
if [[ -n "${response_client_request_id}" ]]; then
  usage_lookup_request_id="client:${response_client_request_id}"
fi

fetch_usage_row() {
  local lookup_request_id="$1"
  local lookup_request_id_b64
  lookup_request_id_b64="$(sql_b64 "${lookup_request_id}")"
  "${PSQL[@]}" -c "
SELECT row_to_json(t)
FROM (
  SELECT
    ul.id,
    ul.account_id,
    ul.request_id,
    ul.model,
    ul.stream,
    ul.duration_ms,
    ul.created_at AT TIME ZONE 'UTC' AS created_at_utc
  FROM usage_logs ul
  WHERE ul.request_id = convert_from(decode('${lookup_request_id_b64}','base64'),'utf8')
  ORDER BY ul.id DESC
  LIMIT 1
) t;
"
}

usage_row_json=""
if [[ -n "${usage_lookup_request_id}" ]]; then
  for ((attempt = 1; attempt <= USAGE_POLL_ATTEMPTS; attempt++)); do
    usage_row_json="$(fetch_usage_row "${usage_lookup_request_id}")"
    usage_row_json="$(printf '%s' "${usage_row_json}" | tr -d '\n')"
    if [[ -n "${usage_row_json}" ]]; then
      break
    fi
    sleep "${USAGE_POLL_INTERVAL_SECONDS}"
  done
fi

if [[ -n "${usage_row_json}" ]]; then
  printf '%s' "${usage_row_json}" >"${tmp_usage}"
else
  printf 'null' >"${tmp_usage}"
fi

docker logs "${APP_CONTAINER}" --since "${LOG_WINDOW}" >"${tmp_log}" 2>&1 || true # preflight-allow: swallow

python3 - "${tmp_body}" "${tmp_headers}" "${tmp_log}" "${tmp_err}" "${http_code}" "${GROUP_NAME}" "${MODEL}" "${LOG_WINDOW}" "${PROMPT_TEXT}" "${ACCOUNT_ID}" "${tmp_context}" "${tmp_usage}" "${probe_request_id}" <<'PY'
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

(
    body_path,
    headers_path,
    log_path,
    err_path,
    http_code,
    group_name,
    model,
    log_window,
    prompt_text,
    target_account_id_raw,
    context_path,
    usage_path,
    probe_request_id,
) = sys.argv[1:14]

target_account_id = int(target_account_id_raw)

raw_body = open(body_path, encoding="utf-8", errors="replace").read()
curl_error = open(err_path, encoding="utf-8", errors="replace").read().strip()
context = json.loads(Path(context_path).read_text(encoding="utf-8"))
usage_row = json.loads(Path(usage_path).read_text(encoding="utf-8"))

response_headers: dict[str, str] = {}
for raw in Path(headers_path).read_text(encoding="utf-8", errors="replace").splitlines():
    line = raw.rstrip("\r")
    if ":" not in line:
        continue
    name, value = line.split(":", 1)
    response_headers[name.strip().lower()] = value.strip()

response_request_id = response_headers.get("x-request-id") or probe_request_id
response_client_request_id = response_headers.get("x-client-request-id", "")
usage_lookup_request_id = f"client:{response_client_request_id}" if response_client_request_id else ""

request_summary = {
    "requested_at_utc": datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
    "group_name": group_name,
    "model": model,
    "prompt_text": prompt_text,
    "http_status": int(http_code) if http_code.isdigit() else None,
    "request_id": response_request_id,
    "client_request_id": response_client_request_id,
    "usage_lookup_request_id": usage_lookup_request_id,
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
matching_access_row = None
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
        row = {
            "account_id": obj.get("account_id"),
            "platform": obj.get("platform"),
            "billing_platform": obj.get("billing_platform"),
            "status_code": obj.get("status_code"),
            "model": obj.get("model"),
            "completed_at": obj.get("completed_at"),
            "latency_ms": obj.get("latency_ms"),
            "request_id": obj.get("request_id"),
            "client_request_id": obj.get("client_request_id"),
        }
        recent_rows.append(row)
        request_id_match = response_request_id and row.get("request_id") == response_request_id
        client_request_id_match = response_client_request_id and row.get("client_request_id") == response_client_request_id
        if request_id_match or client_request_id_match:
            matching_access_row = row

group_accounts = context.get("group_accounts") or []
eligible_accounts = [
    row for row in group_accounts
    if row.get("status") == "active" and row.get("schedulable") is True
]

target_account = context.get("target_account") or {
    "account_id": target_account_id,
}
usage_account_id = usage_row.get("account_id") if isinstance(usage_row, dict) else None
access_account_id = matching_access_row.get("account_id") if matching_access_row else None
usage_matches_target = usage_account_id == target_account_id
access_matches_target = access_account_id == target_account_id if access_account_id is not None else None

if (
    usage_account_id is not None
    and access_account_id is not None
    and usage_account_id != access_account_id
):
    verdict = "observability_conflict"
elif request_summary.get("http_status") != 200:
    verdict = "request_failed"
elif request_summary.get("type") != "message" or request_summary.get("role") != "assistant":
    verdict = "unexpected_response_shape"
elif not isinstance(usage_row, dict):
    verdict = "usage_row_missing"
elif usage_matches_target:
    verdict = "exact_target_verified"
else:
    verdict = "wrong_account_served"

print(json.dumps({
    "request": request_summary,
    "target": target_account,
    "routing_pool": {
        "group_id": context.get("group_id"),
        "group_name": context.get("group_name"),
        "eligible_account_count": len(eligible_accounts),
        "accounts": group_accounts,
    },
    "usage_correlation": {
        "matched": isinstance(usage_row, dict),
        "row": usage_row,
        "account_matches_target": usage_matches_target if isinstance(usage_row, dict) else False,
    },
    "access_log_correlation": {
        "matched": matching_access_row is not None,
        "row": matching_access_row,
        "account_matches_target": access_matches_target,
    },
    "verification": {
        "verdict": verdict,
        "target_account_id": target_account_id,
        "target_account_matched": usage_matches_target,
        "response_ok": request_summary.get("http_status") == 200
        and request_summary.get("type") == "message"
        and request_summary.get("role") == "assistant",
    },
    "recent_kiro_access": {
        "window": log_window,
        "count": len(recent_rows),
        "rows": recent_rows[-10:],
    },
}, ensure_ascii=False, indent=2))
PY
