#!/usr/bin/env bash
set -euo pipefail

ACCOUNT_ID="${ACCOUNT_ID:-}"
MODEL="${MODEL:-}"
ENDPOINT="${ENDPOINT:-messages}"
APP_CONTAINER="${APP_CONTAINER:-tokenkey}"
APP_URL="${APP_URL:-http://localhost:8080}"
MAX_TOKENS="${MAX_TOKENS:-16}"
PROMPT_TEXT="${PROMPT_TEXT:-Reply with exactly: OK}"
KEEP_PROBE_ARTIFACTS="${KEEP_PROBE_ARTIFACTS:-0}"
USAGE_POLL_ATTEMPTS="${USAGE_POLL_ATTEMPTS:-12}"
USAGE_POLL_INTERVAL_SECONDS="${USAGE_POLL_INTERVAL_SECONDS:-1}"
LOG_WINDOW="${LOG_WINDOW:-3m}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-90}"

PSQL=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

fail_json() {
  local message="$1"
  python3 - "$message" <<'PY'
import json, sys
print(json.dumps({"verdict": "setup_error", "error": sys.argv[1]}, ensure_ascii=False))
PY
  exit 0
}

if [[ ! "$ACCOUNT_ID" =~ ^[0-9]+$ ]]; then
  fail_json "ACCOUNT_ID must be numeric"
fi
if [[ -z "$MODEL" ]]; then
  fail_json "MODEL is required"
fi
if [[ ! "$REQUEST_TIMEOUT_SECONDS" =~ ^[0-9]+$ ]] || [[ "$REQUEST_TIMEOUT_SECONDS" -lt 1 ]]; then
  fail_json "REQUEST_TIMEOUT_SECONDS must be a positive integer"
fi
case "$ENDPOINT" in
  messages|chat|responses) ;;
  *) fail_json "ENDPOINT must be messages, chat, or responses" ;;
esac

PROBE_ID="tkprobe-${ACCOUNT_ID}-$(date -u +%Y%m%dT%H%M%SZ)-$$"
GROUP_NAME="__tk_probe_${PROBE_ID}"
KEY_NAME="$GROUP_NAME"
API_KEY="sk-${PROBE_ID}-$(python3 - <<'PY'
import secrets
print(secrets.token_urlsafe(18).replace("-", "").replace("_", "")[:24])
PY
)"

TARGET_JSON="$("${PSQL[@]}" -c "
SELECT COALESCE(row_to_json(t)::text, '')
FROM (
  SELECT
    a.id, a.name, a.platform, a.type, a.status, a.schedulable,
    a.concurrency,
    a.channel_type,
    a.temp_unschedulable_until AT TIME ZONE 'UTC' AS temp_unschedulable_until_utc,
    left(COALESCE(a.temp_unschedulable_reason,''), 240) AS temp_unschedulable_reason,
    left(COALESCE(a.error_message,''), 240) AS error_message,
    COALESCE(array_agg(ag.group_id ORDER BY ag.group_id) FILTER (WHERE ag.group_id IS NOT NULL), '{}') AS current_group_ids
  FROM accounts a
  LEFT JOIN account_groups ag ON ag.account_id = a.id
  WHERE a.id = ${ACCOUNT_ID}
    AND a.deleted_at IS NULL
  GROUP BY a.id
) t;
" | tr -d '\n')"

if [[ -z "$TARGET_JSON" ]]; then
  fail_json "target account not found"
fi

PLATFORM="$(python3 - "$TARGET_JSON" <<'PY'
import json, sys
print(json.loads(sys.argv[1]).get("platform") or "")
PY
)"
if [[ -z "$PLATFORM" ]]; then
  fail_json "target account platform is empty"
fi

GROUP_ID=""
API_KEY_ID=""
cleanup() {
  if [[ "$KEEP_PROBE_ARTIFACTS" == "1" ]]; then
    return
  fi
  if [[ -n "${GROUP_ID:-}" ]]; then
    "${PSQL[@]}" -c "
	DELETE FROM account_groups WHERE group_id = ${GROUP_ID};
	DELETE FROM user_allowed_groups WHERE group_id = ${GROUP_ID};
	UPDATE api_keys SET status='disabled', deleted_at=NOW(), updated_at=NOW() WHERE group_id = ${GROUP_ID} AND name = '$(printf "%s" "$KEY_NAME" | sed "s/'/''/g")';
	UPDATE groups SET status='disabled', deleted_at=NOW(), updated_at=NOW() WHERE id = ${GROUP_ID} AND name = '$(printf "%s" "$GROUP_NAME" | sed "s/'/''/g")';
	" >/dev/null 2>&1 || true # preflight-allow: swallow
  fi
  sudo docker exec "$APP_CONTAINER" rm -f /tmp/tk-probe-request.json /tmp/tk-probe-response.json /tmp/tk-probe-headers.txt >/dev/null 2>&1 || true # preflight-allow: swallow
}
trap cleanup EXIT

GROUP_ID="$("${PSQL[@]}" -c "
INSERT INTO groups (
  name, description, platform, rate_multiplier, is_exclusive, status,
  subscription_type, default_validity_days, claude_code_only,
  model_routing_enabled, model_routing, sort_order, rpm_limit, created_at, updated_at
) VALUES (
	  '$(printf "%s" "$GROUP_NAME" | sed "s/'/''/g")',
	  'exclusive temporary account/model probe; direct probe key only; excluded from universal routing',
	  '$(printf "%s" "$PLATFORM" | sed "s/'/''/g")',
	  1.0, true, 'active',
	  'standard', 30, false,
	  false, '{}'::jsonb, 2147483000, 0, NOW(), NOW()
	) RETURNING id;
" | tr -d '[:space:]')"

if [[ ! "$GROUP_ID" =~ ^[0-9]+$ ]]; then
  fail_json "failed to create temporary group"
fi

"${PSQL[@]}" -c "DELETE FROM user_allowed_groups WHERE group_id = ${GROUP_ID};" >/dev/null

"${PSQL[@]}" -c "
INSERT INTO account_groups (account_id, group_id, priority, created_at)
VALUES (${ACCOUNT_ID}, ${GROUP_ID}, 1, NOW())
ON CONFLICT (account_id, group_id) DO NOTHING;
" >/dev/null

API_KEY_ID="$("${PSQL[@]}" -c "
INSERT INTO api_keys (
  user_id, key, name, group_id, status, routing_mode,
  quota, quota_used, rate_limit_5h, rate_limit_1d, rate_limit_7d,
  usage_5h, usage_1d, usage_7d, created_at, updated_at
) VALUES (
  1,
  '$(printf "%s" "$API_KEY" | sed "s/'/''/g")',
  '$(printf "%s" "$KEY_NAME" | sed "s/'/''/g")',
  ${GROUP_ID},
  'active',
  'direct',
  0, 0, 0, 0, 0,
  0, 0, 0, NOW(), NOW()
) RETURNING id;
" | tr -d '[:space:]')"

if [[ ! "$API_KEY_ID" =~ ^[0-9]+$ ]]; then
  fail_json "failed to create temporary API key"
fi

payload="$(
  python3 - "$ENDPOINT" "$MODEL" "$MAX_TOKENS" "$PROMPT_TEXT" "$PROBE_ID" <<'PY'
import json, sys

endpoint, model, max_tokens, prompt, probe_id = sys.argv[1:6]
max_tokens = int(max_tokens)

if endpoint == "messages":
    user_id = json.dumps({
        "device_id": "0000000000000000000000000000000000000000000000000000000000000001",
        "account_uuid": "",
        "session_id": probe_id,
    }, separators=(",", ":"))
    payload = {
        "model": model,
        "max_tokens": max_tokens,
        "messages": [{"role": "user", "content": prompt}],
        "metadata": {"user_id": user_id},
    }
elif endpoint == "chat":
    payload = {
        "model": model,
        "max_tokens": max_tokens,
        "messages": [{"role": "user", "content": prompt}],
    }
else:
    payload = {
        "model": model,
        "instructions": "You are a terse probe responder.",
        "input": [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": prompt}]}],
        "stream": False,
        "max_output_tokens": max_tokens,
    }
print(json.dumps(payload, ensure_ascii=False, separators=(",", ":")))
PY
)"

case "$ENDPOINT" in
  messages) PATH_SUFFIX="/v1/messages"; AUTH_HEADER_NAME="x-api-key";;
  chat) PATH_SUFFIX="/v1/chat/completions"; AUTH_HEADER_NAME="Authorization";;
  responses) PATH_SUFFIX="/v1/responses"; AUTH_HEADER_NAME="Authorization";;
esac

tmp_payload="$(mktemp)"
tmp_body="$(mktemp)"
tmp_headers="$(mktemp)"
tmp_err="$(mktemp)"
tmp_logs="$(mktemp)"
printf '%s' "$payload" >"$tmp_payload"

CLIENT_REQUEST_ID="$PROBE_ID"
http_code=""
http_output=""
if [[ "$AUTH_HEADER_NAME" == "x-api-key" ]]; then
  if http_output="$(sudo docker exec -i \
    -e TK_PROBE_KEY="$API_KEY" \
    -e TK_PROBE_URL="${APP_URL}${PATH_SUFFIX}" \
    -e TK_PROBE_REQUEST_ID="$CLIENT_REQUEST_ID" \
    -e TK_PROBE_TIMEOUT_SECONDS="$REQUEST_TIMEOUT_SECONDS" \
    "$APP_CONTAINER" sh -lc '
      cat >/tmp/tk-probe-request.json
      curl -sS --connect-timeout 5 --max-time "$TK_PROBE_TIMEOUT_SECONDS" \
        -D /tmp/tk-probe-headers.txt -o /tmp/tk-probe-response.json -w "%{http_code}" \
        -H "x-api-key: $TK_PROBE_KEY" \
        -H "anthropic-version: 2023-06-01" \
        -H "anthropic-beta: claude-code-20250219" \
        -H "X-App: cli" \
        -H "X-Request-ID: $TK_PROBE_REQUEST_ID" \
        -H "Content-Type: application/json" \
        -H "User-Agent: claude-cli/2.1.165 (external, sdk-cli)" \
        --data-binary @/tmp/tk-probe-request.json \
        "$TK_PROBE_URL"
    ' <"$tmp_payload" 2>"$tmp_err")"; then
    http_code="$http_output"
  else
    http_code="$http_output"
  fi
else
  if http_output="$(sudo docker exec -i \
    -e TK_PROBE_KEY="$API_KEY" \
    -e TK_PROBE_URL="${APP_URL}${PATH_SUFFIX}" \
    -e TK_PROBE_REQUEST_ID="$CLIENT_REQUEST_ID" \
    -e TK_PROBE_TIMEOUT_SECONDS="$REQUEST_TIMEOUT_SECONDS" \
    "$APP_CONTAINER" sh -lc '
      cat >/tmp/tk-probe-request.json
      curl -sS --connect-timeout 5 --max-time "$TK_PROBE_TIMEOUT_SECONDS" \
        -D /tmp/tk-probe-headers.txt -o /tmp/tk-probe-response.json -w "%{http_code}" \
        -H "Authorization: Bearer $TK_PROBE_KEY" \
        -H "X-Request-ID: $TK_PROBE_REQUEST_ID" \
        -H "Content-Type: application/json" \
        -H "User-Agent: tokenkey-account-model-probe/1" \
        --data-binary @/tmp/tk-probe-request.json \
        "$TK_PROBE_URL"
    ' <"$tmp_payload" 2>"$tmp_err")"; then
    http_code="$http_output"
  else
    http_code="$http_output"
  fi
fi
http_code="$(printf '%s' "$http_code" | tr -cd '0-9' | tail -c 3)"

sudo docker exec "$APP_CONTAINER" test -f /tmp/tk-probe-response.json >/dev/null 2>&1 &&
  sudo docker exec "$APP_CONTAINER" cat /tmp/tk-probe-response.json >"$tmp_body" || : >"$tmp_body"
sudo docker exec "$APP_CONTAINER" test -f /tmp/tk-probe-headers.txt >/dev/null 2>&1 &&
  sudo docker exec "$APP_CONTAINER" cat /tmp/tk-probe-headers.txt >"$tmp_headers" || : >"$tmp_headers"
sudo docker logs "$APP_CONTAINER" --since "$LOG_WINDOW" >"$tmp_logs" 2>&1 || true # preflight-allow: swallow

usage_row=""
for ((attempt=1; attempt<=USAGE_POLL_ATTEMPTS; attempt++)); do
  usage_row="$("${PSQL[@]}" -c "
SELECT COALESCE(row_to_json(t)::text, '')
FROM (
  SELECT id, account_id, api_key_id, group_id, request_id, model, requested_model,
         upstream_model, duration_ms, stream, created_at AT TIME ZONE 'UTC' AS created_at_utc
  FROM usage_logs
  WHERE api_key_id = ${API_KEY_ID}
    AND created_at > NOW() - interval '5 minutes'
  ORDER BY id DESC
  LIMIT 1
) t;
" | tr -d '\n')"
  [[ -n "$usage_row" ]] && break
  sleep "$USAGE_POLL_INTERVAL_SECONDS"
done

python3 - \
  "$TARGET_JSON" "$PLATFORM" "$GROUP_ID" "$GROUP_NAME" "$API_KEY_ID" "$KEY_NAME" \
  "$MODEL" "$ENDPOINT" "$http_code" "$tmp_body" "$tmp_headers" "$tmp_err" "$tmp_logs" \
  "${usage_row:-null}" "$KEEP_PROBE_ARTIFACTS" "$LOG_WINDOW" "$REQUEST_TIMEOUT_SECONDS" <<'PY'
import json
import re
import sys
from pathlib import Path

(
    target_raw, platform, group_id, group_name, api_key_id, key_name,
    model, endpoint, http_code, body_path, headers_path, err_path, logs_path,
    usage_raw, keep_raw, log_window, request_timeout_seconds,
) = sys.argv[1:18]

target = json.loads(target_raw)
body = Path(body_path).read_text(encoding="utf-8", errors="replace")
headers = Path(headers_path).read_text(encoding="utf-8", errors="replace")
curl_error = Path(err_path).read_text(encoding="utf-8", errors="replace").strip()
logs = Path(logs_path).read_text(encoding="utf-8", errors="replace")
try:
    usage = json.loads(usage_raw) if usage_raw and usage_raw != "null" else None
except Exception:
    usage = None

def classify(code: str, body_text: str, usage_row, curl_err: str):
    if not code or code == "000":
        if curl_err:
            return "setup_error"
        return "gateway_rejected"
    n = int(code)
    low = body_text.lower()
    if 200 <= n < 300:
        if usage_row and int(usage_row.get("account_id") or 0) == int(target["id"]):
            return "servable"
        if usage_row:
            return "wrong_account"
        return "uncorrelated_success"
    if n in (401, 403):
        return "upstream_rejected"
    if n == 429 and "no available accounts" in low:
        return "gateway_rejected"
    if n in (400, 404) and any(s in low for s in ["invalid model", "model_not_found", "not supported", "does not exist", "not a valid"]):
        return "upstream_rejected"
    if n >= 500:
        return "gateway_rejected"
    return "gateway_rejected"

log_lines = []
for line in logs.splitlines():
    if str(target["id"]) in line or group_name in line or str(api_key_id) in line:
        log_lines.append(line[:600])
    if len(log_lines) >= 12:
        break

body_excerpt = re.sub(r"\s+", " ", body).strip()[:1200]
out = {
    "verdict": classify(http_code, body, usage, curl_error),
    "http_code": http_code or "000",
    "target_account": target,
    "probe": {
        "platform": platform,
        "model": model,
        "endpoint": endpoint,
        "temporary_group_id": int(group_id),
        "temporary_group_name": group_name,
        "temporary_api_key_id": int(api_key_id),
        "temporary_api_key_name": key_name,
        "kept_artifacts": keep_raw == "1",
        "exclusive_group": True,
        "universal_routing_excluded": True,
        "request_timeout_seconds": int(request_timeout_seconds),
    },
    "usage_match": usage,
    "response": {
        "headers_excerpt": headers[:1200],
        "body_excerpt": body_excerpt,
        "curl_error": curl_error[:600],
    },
    "recent_log_excerpt": {
        "window": log_window,
        "lines": log_lines,
    },
}
print(json.dumps(out, ensure_ascii=False, indent=2, sort_keys=True))
PY

rm -f "$tmp_payload" "$tmp_body" "$tmp_headers" "$tmp_err" "$tmp_logs"
