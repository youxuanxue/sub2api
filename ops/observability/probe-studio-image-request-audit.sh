#!/usr/bin/env bash
# probe-studio-image-request-audit.sh - read-only Studio image request audit:
# prompt/source/size/model body fingerprints for Image Studio and BakeOff.
# Runs INSIDE the TokenKey host (prod or edge) via run-probe.sh. Output is
# row_to_json so parsing is field-named, not column-index.
#
#   bash ops/observability/run-probe.sh --target prod \
#     --script ops/observability/probe-studio-image-request-audit.sh \
#     [--env WINDOW_MINUTES=60] [--env LIMIT=20] \
#     [--env USER_ID=1] [--env API_KEY_ID=123] [--env MODEL=imagen-4.0-generate-001] \
#     [--env STUDIO_RUN_ID=studio-bakeoff-image-...] [--env PROMPT_SHA256=...]
#
# Filters are optional and are ANDed together:
#   USER_ID, API_KEY_ID, ACCOUNT_ID, MODEL, STUDIO_SOURCE, STUDIO_RUN_ID,
#   STUDIO_PANEL_ID, PROMPT_SHA256, REQUEST_ID, CLIENT_REQUEST_ID.
#
# Interpret:
#   - Same studio_run_id + multiple prompt_sha256 values => different prompt was
#     submitted for panels in the same UI run.
#   - Same prompt_sha256 but wrong output => inspect model/size/forward_model and
#     upstream errors; this audit does not prove image semantic correctness.
#   - No rows can mean the deployment predates this audit hook, the request was
#     outside WINDOW_MINUTES, or the caller did not hit a Studio image path.
set -u

WINDOW_MINUTES="${WINDOW_MINUTES:-60}"
LIMIT="${LIMIT:-20}"
USER_ID="${USER_ID:-}"
API_KEY_ID="${API_KEY_ID:-}"
ACCOUNT_ID="${ACCOUNT_ID:-}"
MODEL="${MODEL:-}"
STUDIO_SOURCE="${STUDIO_SOURCE:-}"
STUDIO_RUN_ID="${STUDIO_RUN_ID:-}"
STUDIO_PANEL_ID="${STUDIO_PANEL_ID:-}"
PROMPT_SHA256="${PROMPT_SHA256:-}"
REQUEST_ID="${REQUEST_ID:-}"
CLIENT_REQUEST_ID="${CLIENT_REQUEST_ID:-}"

validate_int() {
  local name="$1"
  local value="$2"
  case "$value" in ''|*[!0-9]*) echo "bad ${name} (want positive integer)" >&2; exit 2;; esac
  if [ "$value" -le 0 ]; then
    echo "bad ${name} (want positive integer)" >&2
    exit 2
  fi
}

validate_optional_int() {
  local name="$1"
  local value="$2"
  [ -z "$value" ] && return 0
  case "$value" in *[!0-9]*) echo "bad ${name} (want integer)" >&2; exit 2;; esac
}

validate_text_filter() {
  local name="$1"
  local value="$2"
  [ -z "$value" ] && return 0
  if [ "${#value}" -gt 512 ]; then
    echo "bad ${name} (too long; max 512 bytes for probe filter)" >&2
    exit 2
  fi
  if [[ "$value" =~ [[:cntrl:]] ]]; then
    echo "bad ${name} (control characters are not allowed)" >&2
    exit 2
  fi
}

sql_quote() {
  local escaped
  escaped=$(printf '%s' "$1" | sed "s/'/''/g")
  printf "'%s'" "$escaped"
}

add_filter() {
  FILTER_SQL="${FILTER_SQL}
  AND $1"
}

validate_int "WINDOW_MINUTES" "$WINDOW_MINUTES"
validate_int "LIMIT" "$LIMIT"
if [ "$LIMIT" -gt 200 ]; then
  echo "bad LIMIT (max 200 to keep SSM stdout bounded)" >&2
  exit 2
fi
validate_optional_int "USER_ID" "$USER_ID"
validate_optional_int "API_KEY_ID" "$API_KEY_ID"
validate_optional_int "ACCOUNT_ID" "$ACCOUNT_ID"
validate_text_filter "MODEL" "$MODEL"
validate_text_filter "STUDIO_SOURCE" "$STUDIO_SOURCE"
validate_text_filter "STUDIO_RUN_ID" "$STUDIO_RUN_ID"
validate_text_filter "STUDIO_PANEL_ID" "$STUDIO_PANEL_ID"
validate_text_filter "PROMPT_SHA256" "$PROMPT_SHA256"
validate_text_filter "REQUEST_ID" "$REQUEST_ID"
validate_text_filter "CLIENT_REQUEST_ID" "$CLIENT_REQUEST_ID"
if [ -n "$PROMPT_SHA256" ] && [[ ! "$PROMPT_SHA256" =~ ^[0-9a-fA-F]{64}$ ]]; then
  echo "bad PROMPT_SHA256 (want 64 hex chars)" >&2
  exit 2
fi

PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'
FILTER_SQL=""

if [ -n "$USER_ID" ]; then add_filter "user_id = ${USER_ID}"; fi
if [ -n "$API_KEY_ID" ]; then add_filter "api_key_id = ${API_KEY_ID}"; fi
if [ -n "$ACCOUNT_ID" ]; then add_filter "account_id = ${ACCOUNT_ID}"; fi
if [ -n "$MODEL" ]; then
  Q=$(sql_quote "$MODEL")
  add_filter "(model = ${Q} OR extra->>'requested_model' = ${Q} OR extra->>'forward_model' = ${Q})"
fi
if [ -n "$STUDIO_SOURCE" ]; then
  Q=$(sql_quote "$STUDIO_SOURCE")
  add_filter "extra->>'studio_source' = ${Q}"
fi
if [ -n "$STUDIO_RUN_ID" ]; then
  Q=$(sql_quote "$STUDIO_RUN_ID")
  add_filter "extra->>'studio_run_id' = ${Q}"
fi
if [ -n "$STUDIO_PANEL_ID" ]; then
  Q=$(sql_quote "$STUDIO_PANEL_ID")
  add_filter "extra->>'studio_panel_id' = ${Q}"
fi
if [ -n "$PROMPT_SHA256" ]; then
  Q=$(sql_quote "$PROMPT_SHA256")
  add_filter "extra->>'prompt_sha256' = ${Q}"
fi
if [ -n "$REQUEST_ID" ]; then
  Q=$(sql_quote "$REQUEST_ID")
  add_filter "(request_id = ${Q} OR extra->>'request_id' = ${Q})"
fi
if [ -n "$CLIENT_REQUEST_ID" ]; then
  Q=$(sql_quote "$CLIENT_REQUEST_ID")
  add_filter "(client_request_id = ${Q} OR extra->>'client_request_id' = ${Q})"
fi

BASE_CTE="WITH base AS (
  SELECT
    id, created_at, level, component, message, request_id, client_request_id,
    user_id, api_key_id, account_id, platform, model, extra
  FROM ops_system_logs
  WHERE created_at >= now() - interval '${WINDOW_MINUTES} minutes'
    AND component = 'audit.openai_image_request'
    AND message = 'openai_image.request_payload'
    ${FILTER_SQL}
)"

LAST_SEEN_CTE="WITH base AS (
  SELECT created_at, user_id, api_key_id, account_id, model, extra
  FROM ops_system_logs
  WHERE component = 'audit.openai_image_request'
    AND message = 'openai_image.request_payload'
    ${FILTER_SQL}
)"

echo "=== meta ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  now() AT TIME ZONE 'UTC'                         AS now_utc,
  now() AT TIME ZONE 'Asia/Shanghai'              AS now_cst,
  ${WINDOW_MINUTES}::int                          AS window_minutes,
  ${LIMIT}::int                                   AS limit_rows,
  NULLIF($(sql_quote "$USER_ID"), '')             AS user_id_filter,
  NULLIF($(sql_quote "$API_KEY_ID"), '')          AS api_key_id_filter,
  NULLIF($(sql_quote "$ACCOUNT_ID"), '')          AS account_id_filter,
  NULLIF($(sql_quote "$MODEL"), '')               AS model_filter,
  NULLIF($(sql_quote "$STUDIO_SOURCE"), '')       AS studio_source_filter,
  NULLIF($(sql_quote "$STUDIO_RUN_ID"), '')       AS studio_run_id_filter,
  NULLIF($(sql_quote "$STUDIO_PANEL_ID"), '')     AS studio_panel_id_filter,
  NULLIF($(sql_quote "$PROMPT_SHA256"), '')       AS prompt_sha256_filter,
  NULLIF($(sql_quote "$REQUEST_ID"), '')          AS request_id_filter,
  NULLIF($(sql_quote "$CLIENT_REQUEST_ID"), '')   AS client_request_id_filter) t;" 2>&1

echo
echo "=== summary ==="
$PSQL -c "${BASE_CTE}
SELECT row_to_json(t) FROM (SELECT
  count(*)                                                          AS rows,
  min(created_at) AT TIME ZONE 'UTC'                                AS first_at_utc,
  max(created_at) AT TIME ZONE 'UTC'                                AS last_at_utc,
  count(DISTINCT user_id) FILTER (WHERE user_id IS NOT NULL)        AS distinct_users,
  count(DISTINCT api_key_id) FILTER (WHERE api_key_id IS NOT NULL)  AS distinct_api_keys,
  count(DISTINCT account_id) FILTER (WHERE account_id IS NOT NULL)  AS distinct_accounts,
  count(DISTINCT NULLIF(extra->>'studio_run_id', ''))               AS distinct_studio_runs,
  count(DISTINCT NULLIF(extra->>'prompt_sha256', ''))               AS distinct_prompt_hashes,
  count(*) FILTER (WHERE NULLIF(extra->>'prompt_preview', '') IS NULL) AS missing_prompt_rows
  FROM base) t;" 2>&1

echo
echo "=== by source/model/size ==="
$PSQL -c "${BASE_CTE}
SELECT row_to_json(t) FROM (SELECT
  COALESCE(NULLIF(extra->>'studio_source', ''), '<missing>') AS studio_source,
  COALESCE(NULLIF(extra->>'surface', ''), '<missing>')       AS surface,
  COALESCE(NULLIF(model, ''), NULLIF(extra->>'requested_model', ''), '<missing>') AS model,
  COALESCE(NULLIF(extra->>'forward_model', ''), '<missing>') AS forward_model,
  COALESCE(NULLIF(extra->>'size', ''), '<missing>')          AS size,
  COALESCE(NULLIF(extra->>'forward_size', ''), '<missing>')  AS forward_size,
  count(*)                                                   AS rows,
  count(DISTINCT NULLIF(extra->>'prompt_sha256', ''))        AS prompt_hashes,
  min(created_at) AT TIME ZONE 'UTC'                         AS first_at_utc,
  max(created_at) AT TIME ZONE 'UTC'                         AS last_at_utc
  FROM base
  GROUP BY 1,2,3,4,5,6
  ORDER BY rows DESC, last_at_utc DESC
  LIMIT 50) t;" 2>&1

echo
echo "=== by studio run ==="
$PSQL -c "${BASE_CTE}
SELECT row_to_json(t) FROM (SELECT
  COALESCE(NULLIF(extra->>'studio_run_id', ''), '<missing>') AS studio_run_id,
  COALESCE(NULLIF(extra->>'studio_source', ''), '<missing>') AS studio_source,
  count(*)                                                   AS rows,
  string_agg(DISTINCT COALESCE(NULLIF(model, ''), NULLIF(extra->>'requested_model', ''), '<missing>'), ', ') AS models,
  count(DISTINCT NULLIF(extra->>'studio_panel_id', ''))      AS panels,
  count(DISTINCT NULLIF(extra->>'prompt_sha256', ''))        AS prompt_hashes,
  min(created_at) AT TIME ZONE 'UTC'                         AS first_at_utc,
  max(created_at) AT TIME ZONE 'UTC'                         AS last_at_utc
  FROM base
  GROUP BY 1,2
  ORDER BY last_at_utc DESC
  LIMIT 40) t;" 2>&1

echo
echo "=== prompt consistency by run ==="
$PSQL -c "${BASE_CTE}
SELECT row_to_json(t) FROM (SELECT
  COALESCE(NULLIF(extra->>'studio_run_id', ''), '<missing>') AS studio_run_id,
  NULLIF(extra->>'prompt_sha256', '')                        AS prompt_sha256,
  left(COALESCE(extra->>'prompt_preview', ''), 200)          AS prompt_preview_200,
  count(*)                                                   AS rows,
  string_agg(DISTINCT COALESCE(NULLIF(model, ''), NULLIF(extra->>'requested_model', ''), '<missing>'), ', ') AS models,
  min(created_at) AT TIME ZONE 'UTC'                         AS first_at_utc,
  max(created_at) AT TIME ZONE 'UTC'                         AS last_at_utc
  FROM base
  GROUP BY 1,2,3
  ORDER BY studio_run_id DESC, rows DESC, last_at_utc DESC
  LIMIT 80) t;" 2>&1

echo
echo "=== samples ==="
$PSQL -c "${BASE_CTE}
SELECT row_to_json(t) FROM (SELECT
  id,
  created_at AT TIME ZONE 'UTC'           AS ts_utc,
  created_at AT TIME ZONE 'Asia/Shanghai' AS ts_cst,
  request_id,
  client_request_id,
  user_id,
  api_key_id,
  extra->>'group_id' AS group_id,
  account_id,
  platform,
  model,
  extra->>'requested_model' AS requested_model,
  extra->>'forward_model' AS forward_model,
  extra->>'surface' AS surface,
  extra->>'request_path' AS request_path,
  extra->>'inbound_endpoint' AS inbound_endpoint,
  extra->>'upstream_endpoint' AS upstream_endpoint,
  extra->>'studio_source' AS studio_source,
  extra->>'studio_run_id' AS studio_run_id,
  extra->>'studio_panel_id' AS studio_panel_id,
  extra->>'prompt_source' AS prompt_source,
  extra->>'prompt_preview' AS prompt_preview,
  extra->>'prompt_preview_truncated' AS prompt_preview_truncated,
  extra->>'prompt_sha256' AS prompt_sha256,
  extra->>'prompt_bytes' AS prompt_bytes,
  extra->>'prompt_runes' AS prompt_runes,
  extra->>'size' AS size,
  extra->>'forward_size' AS forward_size,
  extra->>'n' AS n,
  extra->>'quality' AS quality,
  extra->>'style' AS style,
  extra->>'response_format' AS response_format,
  extra->>'background' AS background,
  extra->>'output_format' AS output_format,
  extra->>'moderation' AS moderation,
  extra->>'request_body_sha256' AS request_body_sha256,
  extra->>'request_body_bytes' AS request_body_bytes,
  extra->>'forward_body_sha256' AS forward_body_sha256
  FROM base
  ORDER BY created_at DESC, id DESC
  LIMIT ${LIMIT}) t;" 2>&1

echo
echo "=== related ops_error_logs by request_id ==="
$PSQL -c "${BASE_CTE},
ids AS (
  SELECT DISTINCT request_id FROM base WHERE NULLIF(request_id, '') IS NOT NULL
)
SELECT row_to_json(t) FROM (SELECT
  e.created_at AT TIME ZONE 'UTC' AS ts_utc,
  e.request_id,
  e.client_request_id,
  e.user_id,
  e.api_key_id,
  e.account_id,
  e.platform,
  e.model,
  e.request_path,
  e.error_phase,
  e.error_type,
  e.error_owner,
  e.status_code,
  e.upstream_status_code,
  left(COALESCE(e.upstream_error_message, e.error_message, ''), 220) AS msg
  FROM ops_error_logs e
  JOIN ids ON ids.request_id = e.request_id
  WHERE e.created_at >= now() - interval '${WINDOW_MINUTES} minutes'
  ORDER BY e.created_at DESC
  LIMIT 30) t;" 2>&1

echo
echo "=== last-seen with filters (any time) ==="
$PSQL -c "${LAST_SEEN_CTE}
SELECT row_to_json(t) FROM (SELECT
  max(created_at) AT TIME ZONE 'UTC' AS last_at_utc,
  max(created_at) AT TIME ZONE 'Asia/Shanghai' AS last_at_cst
  FROM base) t;" 2>&1
