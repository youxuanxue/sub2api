#!/usr/bin/env bash
# probe-sla-breakdown.sh — Read-only SLA dashboard-equivalent breakdown (24h default).
# WINDOW_MINUTES, when set, overrides WINDOW_HOURS for release-boundary queries.
set -u
PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'
WINDOW_HOURS="${WINDOW_HOURS:-24}"
WINDOW_MINUTES="${WINDOW_MINUTES:-}"
case "$WINDOW_HOURS" in
  ''|*[!0-9]*) echo "[probe-sla-breakdown] ERROR: WINDOW_HOURS not positive int: '$WINDOW_HOURS'" >&2; exit 2 ;;
esac
if [ -n "$WINDOW_MINUTES" ]; then
  case "$WINDOW_MINUTES" in
    *[!0-9]*) echo "[probe-sla-breakdown] ERROR: WINDOW_MINUTES not positive int: '$WINDOW_MINUTES'" >&2; exit 2 ;;
  esac
  WINDOW_INTERVAL="${WINDOW_MINUTES} minute"
else
  WINDOW_INTERVAL="${WINDOW_HOURS} hour"
fi

echo "=== meta ==="
printf 'window=%s\n' "$WINDOW_INTERVAL"

echo
echo "=== sla_overview ==="
$PSQL -c "
WITH bounds AS (
  SELECT now() - interval '${WINDOW_INTERVAL}' AS since, now() AS until
), success AS (
  SELECT COUNT(*)::bigint AS success_count
  FROM usage_logs ul, bounds b
  WHERE ul.created_at >= b.since AND ul.created_at < b.until
), errors AS (
  SELECT
    COUNT(*) FILTER (WHERE COALESCE(status_code,0) >= 400)::bigint AS error_total,
    COUNT(*) FILTER (WHERE COALESCE(status_code,0) >= 400 AND COALESCE(error_owner,'') IN ('provider','platform'))::bigint AS error_sla,
    COUNT(*) FILTER (WHERE COALESCE(status_code,0) >= 400 AND COALESCE(error_owner,'') = 'client')::bigint AS client_faults
  FROM ops_error_logs l, bounds b
  WHERE l.created_at >= b.since AND l.created_at < b.until
    AND l.is_count_tokens = FALSE
)
SELECT row_to_json(t) FROM (
  SELECT
    s.success_count,
    e.error_total,
    e.client_faults,
    e.error_sla,
    (s.success_count + e.error_total) AS request_count_total,
    CASE WHEN (s.success_count + e.error_total) > 0
         THEN round(100.0 * (s.success_count + e.client_faults)::numeric / (s.success_count + e.error_total)::numeric, 3)
         ELSE 0 END AS sla_percent
  FROM success s CROSS JOIN errors e
) t;
" 2>&1

echo
echo "=== by_status_sla_scope ==="
$PSQL -c "
WITH bounds AS (
  SELECT now() - interval '${WINDOW_INTERVAL}' AS since, now() AS until
)
SELECT row_to_json(t) FROM (
  SELECT
    COALESCE(upstream_status_code, status_code, 0) AS status_code,
    COUNT(*)::bigint AS total,
    COUNT(*) FILTER (WHERE COALESCE(error_owner,'') IN ('provider','platform'))::bigint AS sla_faults,
    COUNT(*) FILTER (WHERE COALESCE(error_owner,'') = 'client')::bigint AS client_faults
  FROM ops_error_logs l, bounds b
  WHERE l.created_at >= b.since AND l.created_at < b.until
    AND COALESCE(status_code,0) >= 400
    AND l.is_count_tokens = FALSE
  GROUP BY 1
  ORDER BY sla_faults DESC, total DESC
  LIMIT 25
) t;
" 2>&1

echo
echo "=== by_phase_owner_sla ==="
$PSQL -c "
WITH bounds AS (
  SELECT now() - interval '${WINDOW_INTERVAL}' AS since, now() AS until
)
SELECT row_to_json(t) FROM (
  SELECT
    error_phase,
    error_owner,
    error_type,
    COUNT(*)::bigint AS n
  FROM ops_error_logs l, bounds b
  WHERE l.created_at >= b.since AND l.created_at < b.until
    AND COALESCE(status_code,0) >= 400
    AND COALESCE(error_owner,'') IN ('provider','platform')
    AND l.is_count_tokens = FALSE
  GROUP BY 1,2,3
  ORDER BY n DESC
  LIMIT 30
) t;
" 2>&1

echo
echo "=== top_error_messages_sla ==="
$PSQL -c "
WITH bounds AS (
  SELECT now() - interval '${WINDOW_INTERVAL}' AS since, now() AS until
)
SELECT row_to_json(t) FROM (
  SELECT
    status_code,
    error_phase,
    error_type,
    left(regexp_replace(COALESCE(error_message,''), E'[\\n\\r]+', ' ', 'g'), 120) AS msg_prefix,
    COUNT(*)::bigint AS n
  FROM ops_error_logs l, bounds b
  WHERE l.created_at >= b.since AND l.created_at < b.until
    AND COALESCE(status_code,0) >= 400
    AND COALESCE(error_owner,'') IN ('provider','platform')
    AND l.is_count_tokens = FALSE
  GROUP BY 1,2,3,4
  ORDER BY n DESC
  LIMIT 20
) t;
" 2>&1

echo
echo "=== client_faults_by_status ==="
$PSQL -c "
WITH bounds AS (
  SELECT now() - interval '${WINDOW_INTERVAL}' AS since, now() AS until
)
SELECT row_to_json(t) FROM (
  SELECT
    status_code,
    error_phase,
    left(regexp_replace(COALESCE(error_message,''), E'[\\n\\r]+', ' ', 'g'), 100) AS msg_prefix,
    COUNT(*)::bigint AS n
  FROM ops_error_logs l, bounds b
  WHERE l.created_at >= b.since AND l.created_at < b.until
    AND COALESCE(status_code,0) >= 400
    AND COALESCE(error_owner,'') = 'client'
    AND l.is_count_tokens = FALSE
  GROUP BY 1,2,3
  ORDER BY n DESC
  LIMIT 20
) t;
" 2>&1
