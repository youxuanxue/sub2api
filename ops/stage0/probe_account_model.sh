#!/usr/bin/env bash
# Canonical account-model gateway probe — also used by edge native oauth smoke.
# For modelops, this separates TokenKey local model_mapping/floor rejection
# (gateway_rejected, often "Unsupported model") from upstream account capability
# rejection (upstream_rejected; exact text is platform-specific).
# Batch Kiro Claude matrix: ops/stage0/probe_kiro_claude_models.sh (see tokenkey-account-model-probe skill).
# Skill wrapper: .cursor/skills/tokenkey-account-model-probe/scripts/probe_account_model.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SMOKE_ANTHROPIC_REALISTIC_PY="${SMOKE_ANTHROPIC_REALISTIC_PY:-${SCRIPT_DIR}/smoke_anthropic_realistic.py}"
TK_SMOKE_CLAUDE_USER_AGENT="${TK_SMOKE_CLAUDE_USER_AGENT:-claude-cli/2.1.205 (external, cli)}"
TK_SMOKE_ANTHROPIC_REALISTIC="${TK_SMOKE_ANTHROPIC_REALISTIC:-1}"

ACCOUNT_ID="${ACCOUNT_ID:-}"
MODEL="${MODEL:-}"
ENDPOINT="${ENDPOINT:-messages}"
APP_CONTAINER="${APP_CONTAINER:-auto}"
APP_URL="${APP_URL:-http://localhost:8080}"
MAX_TOKENS="${MAX_TOKENS:-32}"
PROMPT_TEXT="${PROMPT_TEXT:-hi}"
REQUEST_EXTRA_JSON="${REQUEST_EXTRA_JSON:-}"
KEEP_PROBE_ARTIFACTS="${KEEP_PROBE_ARTIFACTS:-0}"
PROBE_REUSE_MODE="${PROBE_REUSE_MODE:-1}"
PROBE_LOCK_TIMEOUT_SECONDS="${PROBE_LOCK_TIMEOUT_SECONDS:-120}"
USAGE_POLL_ATTEMPTS="${USAGE_POLL_ATTEMPTS:-12}"
USAGE_POLL_INTERVAL_SECONDS="${USAGE_POLL_INTERVAL_SECONDS:-1}"
LOG_WINDOW="${LOG_WINDOW:-3m}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-90}"
PROBE_USER_ID=1

PSQL=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -q -A -t -v ON_ERROR_STOP=1)
PSQL_ARRAY=("${PSQL[@]}")
PROBE_RESOURCES="${SCRIPT_DIR}/../pricing/probe_reserved_resources.sh"
if [ ! -f "$PROBE_RESOURCES" ]; then
  PROBE_RESOURCES="${SCRIPT_DIR}/probe_reserved_resources.sh"
fi
if [ ! -f "$PROBE_RESOURCES" ]; then
  fail_json "missing probe_reserved_resources.sh companion (deliver with run-probe --with ops/pricing/probe_reserved_resources.sh)"
fi
# shellcheck source=../pricing/probe_reserved_resources.sh
. "$PROBE_RESOURCES"

sql_escape() {
  printf "%s" "$1" | sed "s/'/''/g"
}

new_probe_api_key() {
  printf 'sk-%s-%s' "$PROBE_ID" "$(python3 - <<'PY'
import secrets
print(secrets.token_urlsafe(18).replace("-", "").replace("_", "")[:24])
PY
)"
}

fail_json() {
  local message="$1"
  python3 - "$message" <<'PY'
import json, sys
print(json.dumps({"verdict": "setup_error", "error": sys.argv[1]}, ensure_ascii=False))
PY
  exit 0
}

psql_capture_numeric() {
  local dest="$1"
  local message="$2"
  local query="$3"
  local errfile out value excerpt
  errfile="$(mktemp)"
  if ! out="$("${PSQL[@]}" -c "$query" 2>"$errfile")"; then
    local err
    err="$(tr '\n' ' ' <"$errfile" | sed -E 's/[[:space:]]+/ /g; s/(password|token|secret|key)[^ ]*/\1=<redacted>/Ig' | cut -c1-240)"
    rm -f "$errfile"
    fail_json "${message}: ${err:-psql failed}"
  fi
  rm -f "$errfile"
  value="$(printf '%s\n' "$out" | awk '/^[[:space:]]*[0-9]+[[:space:]]*$/ {gsub(/[[:space:]]/, ""); print; found=1; exit} END {exit found ? 0 : 1}' || true)"
  if [[ -z "$value" ]]; then
    excerpt="$(printf '%s' "$out" | tr '\n' ' ' | sed -E 's/[[:space:]]+/ /g' | cut -c1-240)"
    fail_json "${message}: no numeric id returned${excerpt:+ (stdout=${excerpt})}"
  fi
  printf -v "$dest" '%s' "$value"
}

resolve_app_container() {
  if [[ "$APP_CONTAINER" != "auto" ]]; then
    return 0
  fi
  if [[ -r /var/lib/tokenkey/active-color ]]; then
    local color
    color="$(sed -n '1p' /var/lib/tokenkey/active-color 2>/dev/null | tr -d '[:space:]')"
    case "$color" in
      blue|green)
        if sudo docker inspect "tokenkey-$color" >/dev/null 2>&1; then
          APP_CONTAINER="tokenkey-$color"
          return 0
        fi
        ;;
    esac
  fi
  for candidate in tokenkey tokenkey-blue tokenkey-green; do
    if sudo docker inspect "$candidate" >/dev/null 2>&1; then
      APP_CONTAINER="$candidate"
      return 0
    fi
  done
  APP_CONTAINER="tokenkey"
}

resolve_app_container
if ! sudo docker inspect "$APP_CONTAINER" >/dev/null 2>&1; then
  fail_json "app container not running: ${APP_CONTAINER}"
fi

if [[ ! "$ACCOUNT_ID" =~ ^[0-9]+$ ]]; then
  fail_json "ACCOUNT_ID must be numeric"
fi
if [[ -z "$MODEL" ]]; then
  fail_json "MODEL is required"
fi
if [[ ! "$REQUEST_TIMEOUT_SECONDS" =~ ^[0-9]+$ ]] || [[ "$REQUEST_TIMEOUT_SECONDS" -lt 1 ]]; then
  fail_json "REQUEST_TIMEOUT_SECONDS must be a positive integer"
fi
case "$PROBE_REUSE_MODE" in
  0|1) ;;
  *) fail_json "PROBE_REUSE_MODE must be 1 or 0" ;;
esac
if [[ ! "$PROBE_LOCK_TIMEOUT_SECONDS" =~ ^[0-9]+$ ]] || [[ "$PROBE_LOCK_TIMEOUT_SECONDS" -lt 1 ]]; then
  fail_json "PROBE_LOCK_TIMEOUT_SECONDS must be a positive integer"
fi
case "$ENDPOINT" in
  messages|count_tokens|chat|responses) ;;
  *) fail_json "ENDPOINT must be messages, count_tokens, chat, or responses" ;;
esac

PROBE_ID="tkprobe-${ACCOUNT_ID}-$(date -u +%Y%m%dT%H%M%SZ)-$$"

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

PROBE_SCOPE="$(python3 - "$PLATFORM" <<'PY'
import re
import sys

scope = re.sub(r"[^a-z0-9]+", "_", sys.argv[1].strip().lower()).strip("_")
print((scope or "platform")[:48])
PY
)"
if [[ -z "$PROBE_SCOPE" ]]; then
  fail_json "failed to derive probe scope"
fi

if [[ "$PROBE_REUSE_MODE" == "1" ]]; then
  GROUP_NAME="__tk_probe_${PROBE_SCOPE}_group"
  KEY_NAME="__tk_probe_${PROBE_SCOPE}_key"
else
  GROUP_NAME="__tk_probe_${PROBE_ID}"
  KEY_NAME="$GROUP_NAME"
fi

API_KEY=""
if [[ "$PROBE_REUSE_MODE" == "1" ]]; then
  if ! command -v flock >/dev/null 2>&1; then
    fail_json "flock is required when PROBE_REUSE_MODE=1"
  fi
  LOCK_PATH="/tmp/tokenkey-account-model-probe-${PROBE_SCOPE}.lock"
  exec 9>"$LOCK_PATH"
  if ! flock -w "$PROBE_LOCK_TIMEOUT_SECONDS" 9; then
    fail_json "timed out waiting for probe reuse lock"
  fi
else
  API_KEY="$(new_probe_api_key)"
fi

GROUP_ID=""
API_KEY_ID=""
cleanup() {
  if [[ "$KEEP_PROBE_ARTIFACTS" == "1" ]]; then
    return
  fi
  if [[ -n "${GROUP_ID:-}" ]]; then
    if [[ "$PROBE_REUSE_MODE" == "1" ]]; then
      "${PSQL[@]}" -c "
	DELETE FROM account_groups WHERE group_id = ${GROUP_ID};
	UPDATE api_keys SET status='disabled', updated_at=NOW() WHERE group_id = ${GROUP_ID} AND name = '$(sql_escape "$KEY_NAME")' AND deleted_at IS NULL;
	UPDATE groups SET status='disabled', updated_at=NOW() WHERE id = ${GROUP_ID} AND name = '$(sql_escape "$GROUP_NAME")' AND deleted_at IS NULL;
	" >/dev/null 2>&1 || true # preflight-allow: swallow
    else
      "${PSQL[@]}" -c "
	DELETE FROM account_groups WHERE group_id = ${GROUP_ID};
	DELETE FROM user_allowed_groups WHERE group_id = ${GROUP_ID};
	UPDATE api_keys SET status='disabled', deleted_at=NOW(), updated_at=NOW() WHERE group_id = ${GROUP_ID} AND name = '$(sql_escape "$KEY_NAME")' AND deleted_at IS NULL;
	UPDATE groups SET status='disabled', deleted_at=NOW(), updated_at=NOW() WHERE id = ${GROUP_ID} AND name = '$(sql_escape "$GROUP_NAME")' AND deleted_at IS NULL;
	" >/dev/null 2>&1 || true # preflight-allow: swallow
    fi
  fi
  sudo docker exec "$APP_CONTAINER" rm -f /tmp/tk-probe-request.json /tmp/tk-probe-response.json /tmp/tk-probe-headers.txt >/dev/null 2>&1 || true # preflight-allow: swallow
}
trap cleanup EXIT

if [[ "$PROBE_REUSE_MODE" == "1" ]]; then
  GROUP_ID="$("${PSQL[@]}" -c "
SELECT COALESCE((
  SELECT id::text
  FROM groups
  WHERE name = '$(sql_escape "$GROUP_NAME")'
    AND deleted_at IS NULL
  ORDER BY id
  LIMIT 1
), '');
" | tr -d '\n')"
  if [[ -n "$GROUP_ID" ]]; then
    psql_capture_numeric GROUP_ID "failed to update probe group name=${GROUP_NAME} platform=${PLATFORM}" "
UPDATE groups
SET
  description = 'reserved reusable account/model probe group; direct probe key only; excluded from universal routing',
  platform = '$(sql_escape "$PLATFORM")',
  rate_multiplier = 1.0,
  is_exclusive = true,
  status = 'active',
  subscription_type = 'standard',
  default_validity_days = 30,
  claude_code_only = false,
  model_routing_enabled = false,
  model_routing = '{}'::jsonb,
  allow_messages_dispatch = true,
  supported_model_scopes = '[\"claude\", \"gemini_text\", \"gemini_image\"]'::jsonb,
  messages_dispatch_model_config = '{}'::jsonb,
  models_list_config = '{}'::jsonb,
  sort_order = 2147483000,
  rpm_limit = 0,
  updated_at = NOW()
WHERE id = ${GROUP_ID}
  AND name = '$(sql_escape "$GROUP_NAME")'
  AND deleted_at IS NULL
RETURNING id;
"
  else
    psql_capture_numeric GROUP_ID "failed to insert probe group name=${GROUP_NAME} platform=${PLATFORM}" "
INSERT INTO groups (
  name, description, platform, rate_multiplier, is_exclusive, status,
  subscription_type, default_validity_days, claude_code_only,
  model_routing_enabled, model_routing, allow_messages_dispatch, supported_model_scopes,
  messages_dispatch_model_config, models_list_config,
  sort_order, rpm_limit, created_at, updated_at
) VALUES (
  '$(sql_escape "$GROUP_NAME")',
  'reserved reusable account/model probe group; direct probe key only; excluded from universal routing',
  '$(sql_escape "$PLATFORM")',
  1.0, true, 'active',
  'standard', 30, false,
  false, '{}'::jsonb, true, '[\"claude\", \"gemini_text\", \"gemini_image\"]'::jsonb,
  '{}'::jsonb, '{}'::jsonb,
  2147483000, 0, NOW(), NOW()
)
RETURNING id;
"
  fi
else
  psql_capture_numeric GROUP_ID "failed to insert one-off probe group name=${GROUP_NAME} platform=${PLATFORM}" "
INSERT INTO groups (
  name, description, platform, rate_multiplier, is_exclusive, status,
  subscription_type, default_validity_days, claude_code_only,
  model_routing_enabled, model_routing, allow_messages_dispatch, supported_model_scopes,
  messages_dispatch_model_config, models_list_config,
  sort_order, rpm_limit, created_at, updated_at
) VALUES (
	  '$(sql_escape "$GROUP_NAME")',
	  'exclusive temporary account/model probe; direct probe key only; excluded from universal routing',
	  '$(sql_escape "$PLATFORM")',
	  1.0, true, 'active',
	  'standard', 30, false,
	  false, '{}'::jsonb, true, '[\"claude\", \"gemini_text\", \"gemini_image\"]'::jsonb,
	  '{}'::jsonb, '{}'::jsonb,
	  2147483000, 0, NOW(), NOW()
	) RETURNING id;
"
fi

if [[ ! "$GROUP_ID" =~ ^[0-9]+$ ]]; then
  fail_json "failed to prepare probe group name=${GROUP_NAME} platform=${PLATFORM}"
fi

"${PSQL[@]}" -c "
INSERT INTO user_allowed_groups (user_id, group_id, created_at)
VALUES (${PROBE_USER_ID}, ${GROUP_ID}, NOW())
ON CONFLICT (user_id, group_id) DO NOTHING;
" >/dev/null

if [[ "$PROBE_REUSE_MODE" == "1" ]]; then
  tk_probe_unbind_account_from_stale_probe_groups "$ACCOUNT_ID" "$GROUP_NAME" || \
    fail_json "failed to unbind stale __tk_probe_* groups for account_id=${ACCOUNT_ID}"
fi

"${PSQL[@]}" -c "
DELETE FROM account_groups WHERE group_id = ${GROUP_ID};
INSERT INTO account_groups (account_id, group_id, priority, created_at)
VALUES (${ACCOUNT_ID}, ${GROUP_ID}, 1, NOW())
ON CONFLICT (account_id, group_id) DO NOTHING;
" >/dev/null

if [[ "$PROBE_REUSE_MODE" == "1" ]]; then
  NEW_API_KEY="$(new_probe_api_key)"
  API_KEY_ROW="$("${PSQL[@]}" -c "
WITH existing AS (
  SELECT id
  FROM api_keys
  WHERE group_id = ${GROUP_ID}
    AND name = '$(sql_escape "$KEY_NAME")'
    AND deleted_at IS NULL
  ORDER BY id
  LIMIT 1
)
SELECT COALESCE((SELECT id::text FROM existing), '');
" | tr -d '\n')"
  if [[ -n "$API_KEY_ROW" ]]; then
    API_KEY_ID="$API_KEY_ROW"
    API_KEY="$("${PSQL[@]}" -c "
UPDATE api_keys
SET
  user_id = ${PROBE_USER_ID},
  key = '$(sql_escape "$NEW_API_KEY")',
  status = 'active',
  routing_mode = 'direct',
  quota = 0,
  quota_used = 0,
  rate_limit_5h = 0,
  rate_limit_1d = 0,
  rate_limit_7d = 0,
  usage_5h = 0,
  usage_1d = 0,
  usage_7d = 0,
  updated_at = NOW()
WHERE id = ${API_KEY_ID}
  AND group_id = ${GROUP_ID}
  AND name = '$(sql_escape "$KEY_NAME")'
  AND deleted_at IS NULL
RETURNING key;
" | tr -d '\n')"
  else
    API_KEY="$NEW_API_KEY"
    psql_capture_numeric API_KEY_ID "failed to insert probe API key name=${KEY_NAME} group_id=${GROUP_ID}" "
INSERT INTO api_keys (
  user_id, key, name, group_id, status, routing_mode,
  quota, quota_used, rate_limit_5h, rate_limit_1d, rate_limit_7d,
  usage_5h, usage_1d, usage_7d, created_at, updated_at
) VALUES (
  ${PROBE_USER_ID},
  '$(sql_escape "$API_KEY")',
  '$(sql_escape "$KEY_NAME")',
  ${GROUP_ID},
  'active',
  'direct',
  0, 0, 0, 0, 0,
  0, 0, 0, NOW(), NOW()
) RETURNING id;
"
  fi
else
  psql_capture_numeric API_KEY_ID "failed to insert one-off probe API key name=${KEY_NAME} group_id=${GROUP_ID}" "
INSERT INTO api_keys (
  user_id, key, name, group_id, status, routing_mode,
  quota, quota_used, rate_limit_5h, rate_limit_1d, rate_limit_7d,
  usage_5h, usage_1d, usage_7d, created_at, updated_at
) VALUES (
  ${PROBE_USER_ID},
  '$(sql_escape "$API_KEY")',
  '$(sql_escape "$KEY_NAME")',
  ${GROUP_ID},
  'active',
  'direct',
  0, 0, 0, 0, 0,
  0, 0, 0, NOW(), NOW()
) RETURNING id;
"
fi

if [[ ! "$API_KEY_ID" =~ ^[0-9]+$ ]]; then
  fail_json "failed to prepare probe API key"
fi
if [[ -z "$API_KEY" ]]; then
  fail_json "failed to read probe API key"
fi

if [[ "$ENDPOINT" == "messages" && "$TK_SMOKE_ANTHROPIC_REALISTIC" == "1" ]]; then
  if [[ ! -f "$SMOKE_ANTHROPIC_REALISTIC_PY" ]]; then
    fail_json "missing smoke_anthropic_realistic.py at ${SMOKE_ANTHROPIC_REALISTIC_PY}"
  fi
  payload="$(python3 "$SMOKE_ANTHROPIC_REALISTIC_PY" \
    --model "$MODEL" \
    --max-tokens "$MAX_TOKENS" \
    --prompt "$PROMPT_TEXT" \
    --session-id "$PROBE_ID")"
else
  payload="$(
    python3 - "$ENDPOINT" "$MODEL" "$MAX_TOKENS" "$PROMPT_TEXT" <<'PY'
import json, sys

endpoint, model, max_tokens, prompt = sys.argv[1:5]
max_tokens = int(max_tokens)

if endpoint == "chat":
    payload = {
        "model": model,
        "max_tokens": max_tokens,
        "messages": [{"role": "user", "content": prompt}],
    }
elif endpoint == "count_tokens":
    payload = {
        "model": model,
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
fi

if [[ -n "$REQUEST_EXTRA_JSON" ]]; then
  if ! payload="$(REQUEST_EXTRA_JSON_VALUE="$REQUEST_EXTRA_JSON" python3 - "$payload" <<'PY'
import json
import os
import sys

payload = json.loads(sys.argv[1])
extra = json.loads(os.environ["REQUEST_EXTRA_JSON_VALUE"])
if not isinstance(payload, dict) or not isinstance(extra, dict):
    raise SystemExit("payload and REQUEST_EXTRA_JSON must both be JSON objects")
payload.update(extra)
print(json.dumps(payload, ensure_ascii=False, separators=(",", ":")))
PY
  )"; then
    fail_json "REQUEST_EXTRA_JSON must be a JSON object"
  fi
fi

case "$ENDPOINT" in
  messages) PATH_SUFFIX="/v1/messages"; AUTH_HEADER_NAME="x-api-key";;
  count_tokens) PATH_SUFFIX="/v1/messages/count_tokens"; AUTH_HEADER_NAME="x-api-key";;
  chat) PATH_SUFFIX="/v1/chat/completions"; AUTH_HEADER_NAME="Authorization";;
  responses) PATH_SUFFIX="/v1/responses"; AUTH_HEADER_NAME="Authorization";;
esac

tmp_payload="$(mktemp)"
tmp_body="$(mktemp)"
tmp_headers="$(mktemp)"
tmp_err="$(mktemp)"
tmp_logs="$(mktemp)"
printf '%s' "$payload" >"$tmp_payload"

PROBE_STARTED_AT="$("${PSQL[@]}" -c "SELECT to_char(NOW() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"');" | tr -d '\n')"
CLIENT_REQUEST_ID="$PROBE_ID"
http_code=""
http_output=""
if [[ "$AUTH_HEADER_NAME" == "x-api-key" ]]; then
  if http_output="$(sudo docker exec -i \
    -e TK_PROBE_KEY="$API_KEY" \
    -e TK_PROBE_URL="${APP_URL}${PATH_SUFFIX}" \
    -e TK_PROBE_REQUEST_ID="$CLIENT_REQUEST_ID" \
    -e TK_PROBE_TIMEOUT_SECONDS="$REQUEST_TIMEOUT_SECONDS" \
    -e TK_PROBE_CLAUDE_UA="$TK_SMOKE_CLAUDE_USER_AGENT" \
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
        -H "User-Agent: $TK_PROBE_CLAUDE_UA" \
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
    AND created_at >= TIMESTAMPTZ '$(sql_escape "$PROBE_STARTED_AT")'
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
  "${usage_row:-null}" "$KEEP_PROBE_ARTIFACTS" "$LOG_WINDOW" "$REQUEST_TIMEOUT_SECONDS" \
  "$PROBE_REUSE_MODE" "$PROBE_STARTED_AT" "$REQUEST_EXTRA_JSON" <<'PY'
import json
import re
import sys
from pathlib import Path

(
    target_raw, platform, group_id, group_name, api_key_id, key_name,
    model, endpoint, http_code, body_path, headers_path, err_path, logs_path,
    usage_raw, keep_raw, log_window, request_timeout_seconds,
    reuse_mode, probe_started_at, request_extra_raw,
) = sys.argv[1:21]

target = json.loads(target_raw)
body = Path(body_path).read_text(encoding="utf-8", errors="replace")
headers = Path(headers_path).read_text(encoding="utf-8", errors="replace")
curl_error = Path(err_path).read_text(encoding="utf-8", errors="replace").strip()
logs = Path(logs_path).read_text(encoding="utf-8", errors="replace")
try:
    usage = json.loads(usage_raw) if usage_raw and usage_raw != "null" else None
except Exception:
    usage = None
try:
    request_extra = json.loads(request_extra_raw) if request_extra_raw else {}
except Exception:
    request_extra = {}

def classify(code: str, body_text: str, usage_row, curl_err: str):
    if not code or code == "000":
        if curl_err:
            return "setup_error"
        return "gateway_rejected"
    n = int(code)
    low = body_text.lower()
    if 200 <= n < 300:
        if endpoint == "count_tokens":
            return "servable"
        if usage_row and int(usage_row.get("account_id") or 0) == int(target["id"]):
            return "servable"
        if usage_row:
            return "wrong_account"
        return "uncorrelated_success"
    if n in (401, 403):
        return "upstream_rejected"
    if n == 429 and "no available accounts" in low:
        return "gateway_rejected"
    # Keep "Unsupported model: X" (TokenKey local floor/model_mapping rejection)
    # as gateway_rejected. Platform-specific "not supported" errors mean the
    # upstream path was reached and rejected the account/model/request.
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
        "group_id": int(group_id),
        "group_name": group_name,
        "api_key_id": int(api_key_id),
        "api_key_name": key_name,
        "reuse_mode": reuse_mode == "1",
        "probe_started_at_utc": probe_started_at,
        "kept_artifacts": keep_raw == "1",
        "exclusive_group": True,
        "universal_routing_excluded": True,
        "request_timeout_seconds": int(request_timeout_seconds),
        "request_extra_keys": sorted(request_extra.keys()) if isinstance(request_extra, dict) else [],
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
