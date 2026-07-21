#!/usr/bin/env bash
# probe-qa-error-evidence.sh - correlate final ops errors with QA captures.
#
# This probe never prints request/response bodies or blob URIs. It reports
# whether matching QA metadata and evidence blobs still exist, so operators can
# decide whether a privacy-scoped deep inspection is possible.
#
# Env:
#   WINDOW_MINUTES  lookback window, default 1440
#   STATUS_CODE     final client status, default 502
#   MODEL           optional exact requested model
#   REQUEST_PATH    optional exact request path
#   USER_AGENT_LIKE optional user-agent substring
set -eu

PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'
WINDOW_MINUTES="${WINDOW_MINUTES:-1440}"
STATUS_CODE="${STATUS_CODE:-502}"
MODEL="${MODEL:-}"
REQUEST_PATH="${REQUEST_PATH:-}"
USER_AGENT_LIKE="${USER_AGENT_LIKE:-}"

for name in WINDOW_MINUTES STATUS_CODE; do
  value="${!name}"
  case "$value" in
    ''|*[!0-9]*) echo "[probe-qa-error-evidence] ERROR: $name must be a non-negative integer" >&2; exit 2 ;;
  esac
done

sql_quote() {
  printf "%s" "$1" | sed "s/'/''/g"
}

MODEL_SQL=$(sql_quote "$MODEL")
REQUEST_PATH_SQL=$(sql_quote "$REQUEST_PATH")
USER_AGENT_LIKE_SQL=$(sql_quote "$USER_AGENT_LIKE")

HAS_TABLE=$($PSQL -c "SELECT to_regclass('public.qa_records') IS NOT NULL;" 2>/dev/null | tr -d '[:space:]')
if [ "$HAS_TABLE" != "t" ]; then
  printf '%s\n' '{"qa_records_available":false,"note":"qa_records table is absent"}'
  exit 0
fi

echo "=== coverage ==="
$PSQL -c "
WITH errors AS (
  SELECT DISTINCT request_id
  FROM ops_error_logs
  WHERE created_at >= now() - interval '${WINDOW_MINUTES} minutes'
    AND status_code = ${STATUS_CODE}
    AND ('${MODEL_SQL}' = '' OR requested_model = '${MODEL_SQL}' OR model = '${MODEL_SQL}')
    AND ('${REQUEST_PATH_SQL}' = '' OR request_path = '${REQUEST_PATH_SQL}')
    AND ('${USER_AGENT_LIKE_SQL}' = '' OR user_agent ILIKE '%' || '${USER_AGENT_LIKE_SQL}' || '%')
), joined AS (
  SELECT e.request_id, q.id AS qa_id, q.blob_uri,
         q.request_blob_uri, q.response_blob_uri, q.stream_blob_uri,
         q.capture_status,
         q.retention_until, q.created_at, q.request_sha256
  FROM errors e
  LEFT JOIN LATERAL (
    SELECT id, blob_uri, request_blob_uri, response_blob_uri, stream_blob_uri,
           capture_status, retention_until, created_at, request_sha256
    FROM qa_records q
    WHERE q.request_id = e.request_id
    ORDER BY q.created_at DESC
    LIMIT 1
  ) q ON true
)
SELECT row_to_json(t) FROM (
  SELECT
    COUNT(*) AS error_requests,
    COUNT(qa_id) AS qa_records,
    COUNT(*) FILTER (WHERE NULLIF(blob_uri, '') IS NOT NULL
                          OR NULLIF(request_blob_uri, '') IS NOT NULL
                          OR NULLIF(response_blob_uri, '') IS NOT NULL
                          OR NULLIF(stream_blob_uri, '') IS NOT NULL) AS qa_blob_refs,
    COUNT(*) FILTER (WHERE request_sha256 <> '') AS request_hashes,
    COUNT(DISTINCT NULLIF(request_sha256, '')) AS distinct_request_hashes,
    COALESCE((
      SELECT MAX(repeats)
      FROM (
        SELECT COUNT(*) AS repeats
        FROM joined
        WHERE request_sha256 <> ''
        GROUP BY request_sha256
      ) grouped_hashes
    ), 0) AS max_same_request_repeats,
    COUNT(*) FILTER (WHERE retention_until > now()) AS retention_active,
    COUNT(*) FILTER (WHERE retention_until <= now()) AS retention_expired,
    to_char(MIN(created_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS first_qa_at_utc,
    to_char(MAX(created_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS last_qa_at_utc
  FROM joined
) t;
"

echo "=== capture_status ==="
$PSQL -c "
WITH errors AS (
  SELECT DISTINCT request_id
  FROM ops_error_logs
  WHERE created_at >= now() - interval '${WINDOW_MINUTES} minutes'
    AND status_code = ${STATUS_CODE}
    AND ('${MODEL_SQL}' = '' OR requested_model = '${MODEL_SQL}' OR model = '${MODEL_SQL}')
    AND ('${REQUEST_PATH_SQL}' = '' OR request_path = '${REQUEST_PATH_SQL}')
    AND ('${USER_AGENT_LIKE_SQL}' = '' OR user_agent ILIKE '%' || '${USER_AGENT_LIKE_SQL}' || '%')
), latest AS (
  SELECT e.request_id, q.capture_status
  FROM errors e
  LEFT JOIN LATERAL (
    SELECT capture_status
    FROM qa_records q
    WHERE q.request_id = e.request_id
    ORDER BY q.created_at DESC
    LIMIT 1
  ) q ON true
)
SELECT row_to_json(t) FROM (
  SELECT COALESCE(capture_status, 'missing') AS capture_status, COUNT(*) AS rows
  FROM latest
  GROUP BY 1
  ORDER BY rows DESC, capture_status
) t;
"

echo "=== replay_outcomes ==="
$PSQL -c "
WITH error_requests AS (
  SELECT DISTINCT request_id
  FROM ops_error_logs
  WHERE created_at >= now() - interval '${WINDOW_MINUTES} minutes'
    AND status_code = ${STATUS_CODE}
    AND ('${MODEL_SQL}' = '' OR requested_model = '${MODEL_SQL}' OR model = '${MODEL_SQL}')
    AND ('${REQUEST_PATH_SQL}' = '' OR request_path = '${REQUEST_PATH_SQL}')
    AND ('${USER_AGENT_LIKE_SQL}' = '' OR user_agent ILIKE '%' || '${USER_AGENT_LIKE_SQL}' || '%')
), error_hashes AS (
  SELECT q.request_sha256, COUNT(*) AS matching_error_rows
  FROM error_requests e
  JOIN qa_records q ON q.request_id = e.request_id
  WHERE q.request_sha256 <> ''
  GROUP BY q.request_sha256
), outcomes AS (
  SELECT
    h.request_sha256,
    h.matching_error_rows,
    COUNT(q.*) FILTER (WHERE q.status_code >= 200 AND q.status_code < 400) AS success_rows,
    COUNT(q.*) FILTER (WHERE q.status_code = ${STATUS_CODE}) AS same_status_rows,
    COUNT(q.*) FILTER (WHERE q.status_code >= 400 AND q.status_code <> ${STATUS_CODE}) AS other_error_rows
  FROM error_hashes h
  LEFT JOIN qa_records q
    ON q.request_sha256 = h.request_sha256
   AND q.created_at >= now() - interval '${WINDOW_MINUTES} minutes'
  GROUP BY h.request_sha256, h.matching_error_rows
)
SELECT row_to_json(t) FROM (
  SELECT
    COUNT(*) AS distinct_error_request_hashes,
    COUNT(*) FILTER (WHERE success_rows > 0) AS hashes_with_success,
    COALESCE(SUM(success_rows), 0) AS success_rows,
    COALESCE(SUM(same_status_rows), 0) AS same_status_rows,
    COALESCE(SUM(other_error_rows), 0) AS other_error_rows,
    MAX(matching_error_rows) AS max_matching_error_repeats
  FROM outcomes
) t;
"

echo "=== blob_storage ==="
TMP_ROWS=$(mktemp)
TMP_PATHS=$(mktemp)
trap 'rm -f "$TMP_ROWS" "$TMP_PATHS"' EXIT
$PSQL -F $'\t' -c "
WITH errors AS (
  SELECT DISTINCT request_id
  FROM ops_error_logs
  WHERE created_at >= now() - interval '${WINDOW_MINUTES} minutes'
    AND status_code = ${STATUS_CODE}
    AND ('${MODEL_SQL}' = '' OR requested_model = '${MODEL_SQL}' OR model = '${MODEL_SQL}')
    AND ('${REQUEST_PATH_SQL}' = '' OR request_path = '${REQUEST_PATH_SQL}')
    AND ('${USER_AGENT_LIKE_SQL}' = '' OR user_agent ILIKE '%' || '${USER_AGENT_LIKE_SQL}' || '%')
), latest AS (
  SELECT e.request_id, q.blob_uri, q.request_blob_uri,
         q.response_blob_uri, q.stream_blob_uri
  FROM errors e
  JOIN LATERAL (
    SELECT blob_uri, request_blob_uri, response_blob_uri, stream_blob_uri
    FROM qa_records q
    WHERE q.request_id = e.request_id
    ORDER BY q.created_at DESC
    LIMIT 1
  ) q ON true
), refs AS (
  SELECT latest.request_id, ref.blob_uri
  FROM latest
  CROSS JOIN LATERAL (VALUES
    (latest.blob_uri),
    (latest.request_blob_uri),
    (latest.response_blob_uri),
    (latest.stream_blob_uri)
  ) AS ref(blob_uri)
  WHERE NULLIF(ref.blob_uri, '') IS NOT NULL
)
SELECT DISTINCT refs.request_id, refs.blob_uri
FROM refs;
" > "$TMP_ROWS"

local_refs=0
local_present=0
local_missing=0
remote_refs=0
app_container=""
if [ -r /var/lib/tokenkey/active-color ]; then
  color=$(tr -d '[:space:]' < /var/lib/tokenkey/active-color 2>/dev/null || true)  # preflight-allow: swallow -- fallback below
  case "$color" in
    blue|green)
      if docker inspect "tokenkey-$color" >/dev/null 2>&1; then
        app_container="tokenkey-$color"
      fi
      ;;
  esac
fi
if [ -z "$app_container" ]; then
  for candidate in tokenkey tokenkey-blue tokenkey-green; do
    if docker inspect "$candidate" >/dev/null 2>&1; then
      app_container="$candidate"
      break
    fi
  done
fi
while IFS=$'\t' read -r _request_id blob_uri; do
  [ -n "${blob_uri:-}" ] || continue
  case "$blob_uri" in
    file://*)
      local_refs=$((local_refs + 1))
      printf '%s\n' "${blob_uri#file://}" >> "$TMP_PATHS"
      ;;
    *) remote_refs=$((remote_refs + 1)) ;;
  esac
done < "$TMP_ROWS"
if [ "$local_refs" -gt 0 ] && [ -n "$app_container" ]; then
  counts=$(docker exec -i "$app_container" sh -c '
    present=0
    missing=0
    while IFS= read -r path; do
      if [ -f "$path" ]; then
        present=$((present + 1))
      else
        missing=$((missing + 1))
      fi
    done
    printf "%s\t%s\n" "$present" "$missing"
  ' < "$TMP_PATHS")
  IFS=$'\t' read -r local_present local_missing <<EOF
$counts
EOF
else
  local_missing=$local_refs
fi
printf '{"local_refs":%d,"local_present":%d,"local_missing":%d,"remote_refs":%d}\n' \
  "$local_refs" "$local_present" "$local_missing" "$remote_refs"
