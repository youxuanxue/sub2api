#!/usr/bin/env bash
# ops-error-triage.sh — Read-only ops_error_logs aggregation, shipped to the
# remote TokenKey host via run-probe.sh. Replaces the §4.2-§4.3 prose SQL
# in the tokenkey-online-log-troubleshooting skill.
#
# Determinism contract (matches dev-rules-convention.mdc §"skill / command 确定性基线"):
#   - All result rows are emitted as row_to_json(t) — field names embedded next
#     to values. Downstream parsers must use jq/json.loads, never column index.
#   - Section headers are stable text markers (=== <name> ===).
#   - All time values are UTC ISO with a 'Z' suffix.
#
# Env (consumed inside the remote shell):
#   WINDOW_HOURS    lookback hours, default 24
#   PATH_FILTER     ops_error_logs.request_path exact match; empty = no filter
#   MODEL_FILTER    ops_error_logs.requested_model exact match; empty = no filter
#   STATUS_MIN      minimum final status_code to include in error counts; default 400
#   TOP_KIND_LIMIT  max rows in upstream_events aggregation; default 50
#   TOP_MIN_LIMIT   max rows in per-minute 429 aggregation; default 30
#
# Container name is hardcoded to `tokenkey-postgres` per Stage0 compose; if it
# drifts, the operator must update the compose file (and this script) together.
set -u

PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'
WINDOW_HOURS="${WINDOW_HOURS:-24}"
PATH_FILTER="${PATH_FILTER:-}"
MODEL_FILTER="${MODEL_FILTER:-}"
STATUS_MIN="${STATUS_MIN:-400}"
TOP_KIND_LIMIT="${TOP_KIND_LIMIT:-50}"
TOP_MIN_LIMIT="${TOP_MIN_LIMIT:-30}"

# Sanity: all numeric envs must be positive int (fail-fast: malformed input must
# not silently inject into the SQL templates below).
for _name in WINDOW_HOURS STATUS_MIN TOP_KIND_LIMIT TOP_MIN_LIMIT; do
  _val=$(eval "printf '%s' \"\${$_name}\"")
  case "$_val" in
    ''|*[!0-9]*) echo "[ops-error-triage] ERROR: $_name not positive int: '$_val'" >&2; exit 2 ;;
  esac
done

# SQL-escape PATH_FILTER / MODEL_FILTER: double every embedded single quote so the
# string literal we interpolate as '${VAR}' can survive an operator passing a
# value containing apostrophes (and refuses to "break out" of the quote).
PATH_FILTER_SQL="${PATH_FILTER//\'/\'\'}"
MODEL_FILTER_SQL="${MODEL_FILTER//\'/\'\'}"

echo "=== meta ==="
printf 'window_hours=%s\npath_filter=%s\nmodel_filter=%s\nstatus_min=%s\n' \
  "$WINDOW_HOURS" "${PATH_FILTER:-<none>}" "${MODEL_FILTER:-<none>}" "$STATUS_MIN"

echo
echo "=== schema (ops_error_logs columns) ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT column_name, data_type, is_nullable
  FROM information_schema.columns
  WHERE table_schema='public' AND table_name='ops_error_logs'
  ORDER BY ordinal_position
) t;
" 2>&1

echo
echo "=== summary ==="
# Build a parameterized CTE; PATH/MODEL filters skip the constraint when empty.
$PSQL -c "
WITH bounds AS (
  SELECT now() - interval '${WINDOW_HOURS} hour' AS since,
         now()                                   AS until
), base AS (
  SELECT l.*
  FROM ops_error_logs l, bounds b
  WHERE l.created_at >= b.since AND l.created_at < b.until
    AND ('${PATH_FILTER_SQL}'  = '' OR l.request_path     = '${PATH_FILTER_SQL}')
    AND ('${MODEL_FILTER_SQL}' = '' OR l.requested_model  = '${MODEL_FILTER_SQL}')
)
SELECT row_to_json(t) FROM (
  SELECT
    COUNT(*)                                        AS total_rows,
    COUNT(*) FILTER (WHERE status_code >= ${STATUS_MIN}) AS final_error_rows,
    to_char(MIN(created_at) AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS first_at_utc,
    to_char(MAX(created_at) AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS last_at_utc
  FROM base
) t;
" 2>&1

echo
echo "=== by_status ==="
$PSQL -c "
WITH bounds AS (
  SELECT now() - interval '${WINDOW_HOURS} hour' AS since,
         now()                                   AS until
), base AS (
  SELECT l.*
  FROM ops_error_logs l, bounds b
  WHERE l.created_at >= b.since AND l.created_at < b.until
    AND ('${PATH_FILTER_SQL}'  = '' OR l.request_path     = '${PATH_FILTER_SQL}')
    AND ('${MODEL_FILTER_SQL}' = '' OR l.requested_model  = '${MODEL_FILTER_SQL}')
)
SELECT row_to_json(t) FROM (
  SELECT status_code, COUNT(*) AS n,
         to_char(MIN(created_at) AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS first_at_utc,
         to_char(MAX(created_at) AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS last_at_utc
  FROM base
  GROUP BY status_code
  ORDER BY n DESC, status_code
) t;
" 2>&1

echo
echo "=== upstream_events ==="
$PSQL -c "
WITH bounds AS (
  SELECT now() - interval '${WINDOW_HOURS} hour' AS since,
         now()                                   AS until
), base AS (
  SELECT l.*
  FROM ops_error_logs l, bounds b
  WHERE l.created_at >= b.since AND l.created_at < b.until
    AND ('${PATH_FILTER_SQL}'  = '' OR l.request_path     = '${PATH_FILTER_SQL}')
    AND ('${MODEL_FILTER_SQL}' = '' OR l.requested_model  = '${MODEL_FILTER_SQL}')
), events AS (
  SELECT l.id AS log_id, l.created_at, l.status_code,
         e.value AS ev
  FROM base l
  CROSS JOIN LATERAL jsonb_array_elements(COALESCE(l.upstream_errors, '[]'::jsonb)) AS e(value)
)
SELECT row_to_json(t) FROM (
  SELECT
    COALESCE(ev->>'kind','')                  AS kind,
    COALESCE(ev->>'platform','')              AS platform,
    COALESCE(ev->>'account_id','')            AS account_id,
    COALESCE(ev->>'account_name','')          AS account_name,
    COALESCE(ev->>'upstream_status_code','')  AS upstream_status,
    status_code                               AS final_status,
    COUNT(*) AS n,
    to_char(MIN(created_at) AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS first_at_utc,
    to_char(MAX(created_at) AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS last_at_utc
  FROM events
  GROUP BY kind, platform, account_id, account_name, upstream_status, final_status
  ORDER BY n DESC, kind, final_status
  LIMIT ${TOP_KIND_LIMIT}
) t;
" 2>&1

echo
echo "=== by_minute_429 ==="
$PSQL -c "
WITH bounds AS (
  SELECT now() - interval '${WINDOW_HOURS} hour' AS since,
         now()                                   AS until
), base AS (
  SELECT l.*
  FROM ops_error_logs l, bounds b
  WHERE l.created_at >= b.since AND l.created_at < b.until
    AND ('${PATH_FILTER_SQL}'  = '' OR l.request_path     = '${PATH_FILTER_SQL}')
    AND ('${MODEL_FILTER_SQL}' = '' OR l.requested_model  = '${MODEL_FILTER_SQL}')
)
SELECT row_to_json(t) FROM (
  SELECT
    to_char(date_trunc('minute', created_at) AT TIME ZONE 'UTC',
            'YYYY-MM-DD\"T\"HH24:MI:00\"Z\"')      AS minute_utc,
    status_code,
    COUNT(*) AS n
  FROM base
  WHERE status_code = 429
  GROUP BY 1, status_code
  ORDER BY n DESC, 1
  LIMIT ${TOP_MIN_LIMIT}
) t;
" 2>&1
