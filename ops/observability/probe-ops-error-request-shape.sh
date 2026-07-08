#!/usr/bin/env bash
# probe-ops-error-request-shape.sh — read-only request_body top-level shape
# aggregation for ops_error_logs. It prints field names and booleans only; it
# deliberately does not print prompt/message contents or raw request bodies.
#
# Env:
#   WINDOW_MINUTES  lookback minutes, default 30
#   USER_ID         optional integer filter
#   API_KEY_ID      optional integer filter
#   ACCOUNT_ID      optional integer filter
#   MODEL           optional exact match on requested_model or model
#   REQUEST_PATH    optional exact match on request_path
#   STATUS_CODE     optional exact status filter, default 400
#   ERROR_MESSAGE_LIKE optional substring filter, SQL-escaped
#   LIMIT           sample row cap, default 12
set -u

PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'
WINDOW_MINUTES="${WINDOW_MINUTES:-30}"
USER_ID="${USER_ID:-}"
API_KEY_ID="${API_KEY_ID:-}"
ACCOUNT_ID="${ACCOUNT_ID:-}"
MODEL="${MODEL:-}"
REQUEST_PATH="${REQUEST_PATH:-}"
STATUS_CODE="${STATUS_CODE:-400}"
ERROR_MESSAGE_LIKE="${ERROR_MESSAGE_LIKE:-}"
LIMIT="${LIMIT:-12}"

for _name in WINDOW_MINUTES STATUS_CODE LIMIT; do
  _val=$(eval "printf '%s' \"\${$_name}\"")
  case "$_val" in
    ''|*[!0-9]*) echo "[probe-ops-error-request-shape] ERROR: $_name not positive int: '$_val'" >&2; exit 2 ;;
  esac
done
for _name in USER_ID API_KEY_ID ACCOUNT_ID; do
  _val=$(eval "printf '%s' \"\${$_name}\"")
  if [ -n "$_val" ]; then
    case "$_val" in
      *[!0-9]*) echo "[probe-ops-error-request-shape] ERROR: $_name not integer: '$_val'" >&2; exit 2 ;;
    esac
  fi
done

MODEL_SQL="${MODEL//\'/\'\'}"
REQUEST_PATH_SQL="${REQUEST_PATH//\'/\'\'}"
ERROR_MESSAGE_LIKE_SQL="${ERROR_MESSAGE_LIKE//\'/\'\'}"

echo "=== meta ==="
printf 'window_minutes=%s\nuser_id=%s\napi_key_id=%s\naccount_id=%s\nmodel=%s\nrequest_path=%s\nstatus_code=%s\nerror_message_like=%s\nlimit=%s\n' \
  "$WINDOW_MINUTES" "${USER_ID:-<none>}" "${API_KEY_ID:-<none>}" "${ACCOUNT_ID:-<none>}" \
  "${MODEL:-<none>}" "${REQUEST_PATH:-<none>}" "$STATUS_CODE" "${ERROR_MESSAGE_LIKE:-<none>}" "$LIMIT"

HAS_REQUEST_BODY=$($PSQL -c "
SELECT EXISTS (
  SELECT 1
  FROM information_schema.columns
  WHERE table_schema='public'
    AND table_name='ops_error_logs'
    AND column_name='request_body'
);
" 2>/dev/null | tr -d '[:space:]')

if [ "$HAS_REQUEST_BODY" != "t" ]; then
  echo
  echo "=== body_schema ==="
  $PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT false AS has_request_body,
         'ops_error_logs.request_body column is absent on this host; request parameter keys are unavailable from this table' AS note
) t;
" 2>&1

  echo
  echo "=== fallback_summary ==="
  $PSQL -c "
WITH base AS (
  SELECT l.*
  FROM ops_error_logs l
  WHERE l.created_at >= now() - interval '${WINDOW_MINUTES} minutes'
    AND l.status_code = ${STATUS_CODE}
    AND ('${USER_ID}' = '' OR l.user_id = NULLIF('${USER_ID}','')::bigint)
    AND ('${API_KEY_ID}' = '' OR l.api_key_id = NULLIF('${API_KEY_ID}','')::bigint)
    AND ('${ACCOUNT_ID}' = '' OR l.account_id = NULLIF('${ACCOUNT_ID}','')::bigint)
    AND ('${MODEL_SQL}' = '' OR l.requested_model = '${MODEL_SQL}' OR l.model = '${MODEL_SQL}')
    AND ('${REQUEST_PATH_SQL}' = '' OR l.request_path = '${REQUEST_PATH_SQL}')
    AND ('${ERROR_MESSAGE_LIKE_SQL}' = '' OR l.error_message ILIKE '%' || '${ERROR_MESSAGE_LIKE_SQL}' || '%')
)
SELECT row_to_json(t) FROM (
  SELECT
    COUNT(*) AS rows,
    to_char(MIN(created_at) AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS first_at_utc,
    to_char(MAX(created_at) AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS last_at_utc
  FROM base
) t;
" 2>&1

  echo
  echo "=== fallback_error_messages ==="
  $PSQL -c "
WITH base AS (
  SELECT l.*
  FROM ops_error_logs l
  WHERE l.created_at >= now() - interval '${WINDOW_MINUTES} minutes'
    AND l.status_code = ${STATUS_CODE}
    AND ('${USER_ID}' = '' OR l.user_id = NULLIF('${USER_ID}','')::bigint)
    AND ('${API_KEY_ID}' = '' OR l.api_key_id = NULLIF('${API_KEY_ID}','')::bigint)
    AND ('${ACCOUNT_ID}' = '' OR l.account_id = NULLIF('${ACCOUNT_ID}','')::bigint)
    AND ('${MODEL_SQL}' = '' OR l.requested_model = '${MODEL_SQL}' OR l.model = '${MODEL_SQL}')
    AND ('${REQUEST_PATH_SQL}' = '' OR l.request_path = '${REQUEST_PATH_SQL}')
    AND ('${ERROR_MESSAGE_LIKE_SQL}' = '' OR l.error_message ILIKE '%' || '${ERROR_MESSAGE_LIKE_SQL}' || '%')
)
SELECT row_to_json(t) FROM (
  SELECT left(error_message, 160) AS error_message, COUNT(*) AS rows
  FROM base
  GROUP BY 1
  ORDER BY rows DESC, error_message
) t;
" 2>&1

  echo
  echo "=== fallback_samples ==="
  $PSQL -c "
WITH base AS (
  SELECT l.*
  FROM ops_error_logs l
  WHERE l.created_at >= now() - interval '${WINDOW_MINUTES} minutes'
    AND l.status_code = ${STATUS_CODE}
    AND ('${USER_ID}' = '' OR l.user_id = NULLIF('${USER_ID}','')::bigint)
    AND ('${API_KEY_ID}' = '' OR l.api_key_id = NULLIF('${API_KEY_ID}','')::bigint)
    AND ('${ACCOUNT_ID}' = '' OR l.account_id = NULLIF('${ACCOUNT_ID}','')::bigint)
    AND ('${MODEL_SQL}' = '' OR l.requested_model = '${MODEL_SQL}' OR l.model = '${MODEL_SQL}')
    AND ('${REQUEST_PATH_SQL}' = '' OR l.request_path = '${REQUEST_PATH_SQL}')
    AND ('${ERROR_MESSAGE_LIKE_SQL}' = '' OR l.error_message ILIKE '%' || '${ERROR_MESSAGE_LIKE_SQL}' || '%')
)
SELECT row_to_json(t) FROM (
  SELECT
    id,
    to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS created_at_utc,
    request_id,
    client_request_id,
    user_id,
    api_key_id,
    account_id,
    group_id,
    request_path,
    COALESCE(requested_model, model) AS model,
    left(error_message, 120) AS error_message
  FROM base
  ORDER BY created_at DESC
  LIMIT ${LIMIT}
) t;
" 2>&1
  exit 0
fi

echo
echo "=== summary ==="
$PSQL -c "
WITH base AS (
  SELECT l.*
  FROM ops_error_logs l
  WHERE l.created_at >= now() - interval '${WINDOW_MINUTES} minutes'
    AND l.status_code = ${STATUS_CODE}
    AND ('${USER_ID}' = '' OR l.user_id = NULLIF('${USER_ID}','')::bigint)
    AND ('${API_KEY_ID}' = '' OR l.api_key_id = NULLIF('${API_KEY_ID}','')::bigint)
    AND ('${ACCOUNT_ID}' = '' OR l.account_id = NULLIF('${ACCOUNT_ID}','')::bigint)
    AND ('${MODEL_SQL}' = '' OR l.requested_model = '${MODEL_SQL}' OR l.model = '${MODEL_SQL}')
    AND ('${REQUEST_PATH_SQL}' = '' OR l.request_path = '${REQUEST_PATH_SQL}')
    AND ('${ERROR_MESSAGE_LIKE_SQL}' = '' OR l.error_message ILIKE '%' || '${ERROR_MESSAGE_LIKE_SQL}' || '%')
)
SELECT row_to_json(t) FROM (
  SELECT
    COUNT(*) AS rows,
    COUNT(*) FILTER (WHERE jsonb_typeof(request_body) = 'object') AS rows_with_object_body,
    COUNT(*) FILTER (WHERE request_body_truncated) AS truncated_rows,
    to_char(MIN(created_at) AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS first_at_utc,
    to_char(MAX(created_at) AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS last_at_utc
  FROM base
) t;
" 2>&1

echo
echo "=== top_level_keys ==="
$PSQL -c "
WITH base AS (
  SELECT l.*
  FROM ops_error_logs l
  WHERE l.created_at >= now() - interval '${WINDOW_MINUTES} minutes'
    AND l.status_code = ${STATUS_CODE}
    AND ('${USER_ID}' = '' OR l.user_id = NULLIF('${USER_ID}','')::bigint)
    AND ('${API_KEY_ID}' = '' OR l.api_key_id = NULLIF('${API_KEY_ID}','')::bigint)
    AND ('${ACCOUNT_ID}' = '' OR l.account_id = NULLIF('${ACCOUNT_ID}','')::bigint)
    AND ('${MODEL_SQL}' = '' OR l.requested_model = '${MODEL_SQL}' OR l.model = '${MODEL_SQL}')
    AND ('${REQUEST_PATH_SQL}' = '' OR l.request_path = '${REQUEST_PATH_SQL}')
    AND ('${ERROR_MESSAGE_LIKE_SQL}' = '' OR l.error_message ILIKE '%' || '${ERROR_MESSAGE_LIKE_SQL}' || '%')
), keys AS (
  SELECT jsonb_object_keys(request_body) AS key
  FROM base
  WHERE jsonb_typeof(request_body) = 'object'
)
SELECT row_to_json(t) FROM (
  SELECT key, COUNT(*) AS rows
  FROM keys
  GROUP BY key
  ORDER BY rows DESC, key
) t;
" 2>&1

echo
echo "=== deprecated_sampling_keys ==="
$PSQL -c "
WITH base AS (
  SELECT l.*
  FROM ops_error_logs l
  WHERE l.created_at >= now() - interval '${WINDOW_MINUTES} minutes'
    AND l.status_code = ${STATUS_CODE}
    AND ('${USER_ID}' = '' OR l.user_id = NULLIF('${USER_ID}','')::bigint)
    AND ('${API_KEY_ID}' = '' OR l.api_key_id = NULLIF('${API_KEY_ID}','')::bigint)
    AND ('${ACCOUNT_ID}' = '' OR l.account_id = NULLIF('${ACCOUNT_ID}','')::bigint)
    AND ('${MODEL_SQL}' = '' OR l.requested_model = '${MODEL_SQL}' OR l.model = '${MODEL_SQL}')
    AND ('${REQUEST_PATH_SQL}' = '' OR l.request_path = '${REQUEST_PATH_SQL}')
    AND ('${ERROR_MESSAGE_LIKE_SQL}' = '' OR l.error_message ILIKE '%' || '${ERROR_MESSAGE_LIKE_SQL}' || '%')
), sampling AS (
  SELECT key, jsonb_typeof(request_body -> key) AS value_type
  FROM base
  CROSS JOIN (VALUES ('temperature'), ('top_p'), ('top_k')) AS s(key)
  WHERE jsonb_typeof(request_body) = 'object'
    AND request_body ? key
)
SELECT row_to_json(t) FROM (
  SELECT key, value_type, COUNT(*) AS rows
  FROM sampling
  GROUP BY key, value_type
  ORDER BY key, value_type
) t;
" 2>&1

echo
echo "=== keysets ==="
$PSQL -c "
WITH base AS (
  SELECT l.*
  FROM ops_error_logs l
  WHERE l.created_at >= now() - interval '${WINDOW_MINUTES} minutes'
    AND l.status_code = ${STATUS_CODE}
    AND ('${USER_ID}' = '' OR l.user_id = NULLIF('${USER_ID}','')::bigint)
    AND ('${API_KEY_ID}' = '' OR l.api_key_id = NULLIF('${API_KEY_ID}','')::bigint)
    AND ('${ACCOUNT_ID}' = '' OR l.account_id = NULLIF('${ACCOUNT_ID}','')::bigint)
    AND ('${MODEL_SQL}' = '' OR l.requested_model = '${MODEL_SQL}' OR l.model = '${MODEL_SQL}')
    AND ('${REQUEST_PATH_SQL}' = '' OR l.request_path = '${REQUEST_PATH_SQL}')
    AND ('${ERROR_MESSAGE_LIKE_SQL}' = '' OR l.error_message ILIKE '%' || '${ERROR_MESSAGE_LIKE_SQL}' || '%')
), shaped AS (
  SELECT (
    SELECT jsonb_agg(key ORDER BY key)
    FROM jsonb_object_keys(request_body) AS key
  ) AS keys
  FROM base
  WHERE jsonb_typeof(request_body) = 'object'
)
SELECT row_to_json(t) FROM (
  SELECT keys, COUNT(*) AS rows
  FROM shaped
  GROUP BY keys
  ORDER BY rows DESC, keys
  LIMIT ${LIMIT}
) t;
" 2>&1

echo
echo "=== samples ==="
$PSQL -c "
WITH base AS (
  SELECT l.*
  FROM ops_error_logs l
  WHERE l.created_at >= now() - interval '${WINDOW_MINUTES} minutes'
    AND l.status_code = ${STATUS_CODE}
    AND ('${USER_ID}' = '' OR l.user_id = NULLIF('${USER_ID}','')::bigint)
    AND ('${API_KEY_ID}' = '' OR l.api_key_id = NULLIF('${API_KEY_ID}','')::bigint)
    AND ('${ACCOUNT_ID}' = '' OR l.account_id = NULLIF('${ACCOUNT_ID}','')::bigint)
    AND ('${MODEL_SQL}' = '' OR l.requested_model = '${MODEL_SQL}' OR l.model = '${MODEL_SQL}')
    AND ('${REQUEST_PATH_SQL}' = '' OR l.request_path = '${REQUEST_PATH_SQL}')
    AND ('${ERROR_MESSAGE_LIKE_SQL}' = '' OR l.error_message ILIKE '%' || '${ERROR_MESSAGE_LIKE_SQL}' || '%')
)
SELECT row_to_json(t) FROM (
  SELECT
    id,
    to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS created_at_utc,
    request_id,
    client_request_id,
    user_id,
    api_key_id,
    account_id,
    group_id,
    request_path,
    COALESCE(requested_model, model) AS model,
    request_body_bytes,
    request_body_truncated,
    (request_body ? 'temperature') AS has_temperature,
    (request_body ? 'top_p') AS has_top_p,
    (request_body ? 'top_k') AS has_top_k,
    (
      SELECT jsonb_agg(key ORDER BY key)
      FROM jsonb_object_keys(request_body) AS key
    ) AS keys,
    left(error_message, 120) AS error_message
  FROM base
  WHERE jsonb_typeof(request_body) = 'object'
  ORDER BY created_at DESC
  LIMIT ${LIMIT}
) t;
" 2>&1
