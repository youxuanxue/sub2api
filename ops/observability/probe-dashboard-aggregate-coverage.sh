#!/usr/bin/env bash
# probe-dashboard-aggregate-coverage.sh — read-only diagnostic for the
# "Token 使用趋势 only shows 2 days" bug. Confirms whether the pre-aggregated
# dashboard tables (usage_dashboard_daily / hourly) are under-covered relative
# to raw usage_logs, and reports the aggregation watermark.
#
# Runs INSIDE the TokenKey host (prod or edge) via run-probe.sh. Read-only.
# Output is row_to_json so downstream parsing is field-named, not column-index.
set -u

PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'

echo "=== meta ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT now() AT TIME ZONE 'UTC' AS now_utc) t;" 2>&1

echo
echo "=== usage_dashboard_daily coverage ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT
    count(*)                          AS row_count,
    min(bucket_date)                  AS min_date,
    max(bucket_date)                  AS max_date,
    (max(bucket_date) - min(bucket_date)) AS span_days
  FROM usage_dashboard_daily
) t;" 2>&1

echo
echo "=== usage_dashboard_daily last 16 rows ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT bucket_date, total_requests,
         (input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) AS total_tokens,
         computed_at
  FROM usage_dashboard_daily
  ORDER BY bucket_date DESC
  LIMIT 16
) t;" 2>&1

echo
echo "=== usage_dashboard_hourly coverage ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT count(*) AS row_count, min(bucket_start) AS min_ts, max(bucket_start) AS max_ts
  FROM usage_dashboard_hourly
) t;" 2>&1

echo
echo "=== aggregation watermark ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT id, last_aggregated_at,
         (now() - last_aggregated_at) AS staleness
  FROM usage_dashboard_aggregation_watermark
) t;" 2>&1

echo
echo "=== raw usage_logs coverage (contrast) ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT count(*)        AS row_count,
         min(created_at)  AS min_ts,
         max(created_at)  AS max_ts,
         count(DISTINCT (created_at AT TIME ZONE 'UTC')::date) AS distinct_days
  FROM usage_logs
) t;" 2>&1

echo
echo "=== raw usage_logs per-day request counts, last 16 UTC days ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT (created_at AT TIME ZONE 'UTC')::date AS day, count(*) AS requests
  FROM usage_logs
  WHERE created_at >= now() - interval '16 days'
  GROUP BY 1
  ORDER BY 1 DESC
) t;" 2>&1
