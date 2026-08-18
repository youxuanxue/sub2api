#!/usr/bin/env bash
# Read-only: diagnose admin dashboard gateway_latency_ms vs duration_ms (post 1.8.139).
set -euo pipefail

APP_CONTAINER="${APP_CONTAINER:-tokenkey-blue}"
PG_CONTAINER="${PG_CONTAINER:-tokenkey-postgres}"

psql_q() {
  docker exec "$PG_CONTAINER" psql -U tokenkey -d tokenkey -Atqc "$1"
}

echo '=== gateway_latency_last_24h ==='
psql_q "
SELECT row_to_json(t) FROM (
  SELECT
    COUNT(*) FILTER (WHERE gateway_latency_ms IS NOT NULL) AS samples,
    COUNT(*) AS total_requests,
    ROUND(AVG(gateway_latency_ms) FILTER (WHERE gateway_latency_ms IS NOT NULL)) AS avg_gateway_ms,
    ROUND(AVG(duration_ms) FILTER (WHERE duration_ms IS NOT NULL)) AS avg_duration_ms,
    ROUND(AVG(gateway_latency_ms - duration_ms) FILTER (WHERE gateway_latency_ms IS NOT NULL AND duration_ms IS NOT NULL)) AS avg_gateway_minus_duration,
    ROUND((AVG(gateway_latency_ms::numeric / NULLIF(duration_ms,0)) FILTER (WHERE gateway_latency_ms IS NOT NULL AND duration_ms > 0))::numeric, 3) AS avg_gateway_over_duration,
    ROUND(AVG(CASE WHEN stream THEN gateway_latency_ms END)) AS avg_gateway_stream,
    ROUND(AVG(CASE WHEN NOT stream THEN gateway_latency_ms END)) AS avg_gateway_non_stream,
    ROUND(100.0 * COUNT(*) FILTER (WHERE stream) / NULLIF(COUNT(*),0), 1) AS stream_pct
  FROM usage_logs
  WHERE created_at >= NOW() - INTERVAL '24 hours'
) t;
"

echo '=== gateway_latency_p95_last_24h ==='
psql_q "
SELECT row_to_json(t) FROM (
  SELECT
    percentile_cont(0.5) WITHIN GROUP (ORDER BY gateway_latency_ms) AS p50,
    percentile_cont(0.95) WITHIN GROUP (ORDER BY gateway_latency_ms) AS p95,
    percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms) AS duration_p50,
    percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms) AS duration_p95
  FROM usage_logs
  WHERE created_at >= NOW() - INTERVAL '24 hours'
    AND gateway_latency_ms IS NOT NULL
) t;
"

echo '=== dashboard_daily_alltime_gateway ==='
psql_q "
SELECT row_to_json(t) FROM (
  SELECT
    COALESCE(SUM(total_gateway_latency_ms),0) AS sum_gateway_ms,
    COALESCE(SUM(gateway_latency_samples),0) AS samples,
    CASE WHEN COALESCE(SUM(gateway_latency_samples),0) > 0
      THEN ROUND(COALESCE(SUM(total_gateway_latency_ms),0)::numeric / SUM(gateway_latency_samples))
      ELSE NULL END AS avg_from_daily,
    COALESCE(SUM(total_duration_ms),0) AS sum_duration_ms,
    COALESCE(SUM(total_requests),0) AS total_requests
  FROM usage_dashboard_daily
) t;
"

echo '=== dashboard_daily_today_gateway ==='
psql_q "
SELECT row_to_json(t) FROM (
  SELECT
    bucket_date,
    COALESCE(total_gateway_latency_ms,0) AS sum_gateway_ms,
    COALESCE(gateway_latency_samples,0) AS samples,
    CASE WHEN COALESCE(gateway_latency_samples,0) > 0
      THEN ROUND(COALESCE(total_gateway_latency_ms,0)::numeric / gateway_latency_samples)
      ELSE NULL END AS avg_today_ms,
    COALESCE(total_requests,0) AS today_requests
  FROM usage_dashboard_daily
  WHERE bucket_date = (timezone('Asia/Shanghai', now()))::date
) t;
"

echo '=== user_16_last_24h ==='
psql_q "
SELECT row_to_json(t) FROM (
  SELECT
    COUNT(*) AS total_requests,
    COUNT(*) FILTER (WHERE gateway_latency_ms IS NOT NULL) AS gateway_samples,
    ROUND(AVG(duration_ms) FILTER (WHERE duration_ms IS NOT NULL)) AS avg_duration_ms,
    ROUND(AVG(gateway_latency_ms) FILTER (WHERE gateway_latency_ms IS NOT NULL)) AS avg_gateway_ms
  FROM usage_logs
  WHERE user_id = 16
    AND created_at >= NOW() - INTERVAL '24 hours'
) t;
"

echo '=== sample_outliers_last_6h ==='
psql_q "
SELECT row_to_json(t) FROM (
  SELECT request_id, model, stream, duration_ms, gateway_latency_ms,
         gateway_latency_ms - duration_ms AS gw_minus_dur
  FROM usage_logs
  WHERE created_at >= NOW() - INTERVAL '6 hours'
    AND gateway_latency_ms IS NOT NULL
  ORDER BY gateway_latency_ms DESC
  LIMIT 8
) t;
"
