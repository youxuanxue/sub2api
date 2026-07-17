#!/usr/bin/env bash
# probe-admin-model-rollup-timing.sh — read-only timing/consistency probe for
# admin dashboard model distribution rollup candidates.
set -euo pipefail

PSQL="${PSQL:-docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t}"
INCLUDE_EXPLAIN="${INCLUDE_EXPLAIN:-0}"
EXPLAIN_SECTION="${EXPLAIN_SECTION:-all}"

should_explain() {
  [ "$INCLUDE_EXPLAIN" = "1" ] && { [ "$EXPLAIN_SECTION" = "all" ] || [ "$EXPLAIN_SECTION" = "$1" ]; }
}

echo "=== meta ==="
$PSQL -c "SELECT row_to_json(t) FROM (
  SELECT
    now() AT TIME ZONE 'UTC' AS now_utc,
    EXISTS(SELECT 1 FROM usage_dashboard_model_daily WHERE bucket_date = DATE '1970-01-01' AND model = '__tk_model_daily_backfill_marker__') AS model_daily_backfilled,
    (SELECT min(bucket_date) FROM usage_dashboard_model_daily WHERE bucket_date > DATE '1970-01-01') AS model_daily_min_date,
    (SELECT max(bucket_date) FROM usage_dashboard_model_daily WHERE bucket_date > DATE '1970-01-01') AS model_daily_max_date,
    (SELECT count(*) FROM usage_dashboard_model_daily) AS model_daily_rows_total,
    (SELECT count(*) FROM usage_dashboard_model_daily WHERE bucket_date > DATE '1970-01-01') AS model_daily_rows_data,
    (SELECT last_aggregated_at FROM usage_dashboard_aggregation_watermark WHERE id = 1) AS aggregation_watermark
) t;"

echo "=== window_row_counts ==="
$PSQL -c "SELECT row_to_json(t) FROM (
  WITH bounds AS (
    SELECT
      ((now() AT TIME ZONE 'Asia/Shanghai')::date - interval '6 days')::date AS start_day,
      (now() AT TIME ZONE 'Asia/Shanghai')::date AS today_day,
      now() AS now_ts,
      (SELECT last_aggregated_at FROM usage_dashboard_aggregation_watermark WHERE id = 1) AS watermark
  ),
  planned AS (
    SELECT
      bounds.*,
      bounds.start_day::timestamp AT TIME ZONE 'Asia/Shanghai' AS window_start,
      bounds.today_day::timestamp AT TIME ZONE 'Asia/Shanghai' AS today_start,
      GREATEST(
        bounds.start_day,
        COALESCE((SELECT min(bucket_date) FROM usage_dashboard_model_daily WHERE bucket_date > DATE '1970-01-01'), bounds.today_day)
      ) AS rollup_start_day
    FROM bounds
  )
  SELECT
    (SELECT window_start FROM planned) AS window_start,
    (SELECT today_start FROM planned) AS today_start,
    (SELECT rollup_start_day FROM planned) AS rollup_start_day,
    (SELECT watermark FROM planned) AS watermark,
    (SELECT count(*) FROM usage_logs ul, planned p WHERE ul.created_at >= p.window_start AND ul.created_at < p.now_ts) AS raw_7d_rows_to_now,
    (SELECT count(*) FROM usage_logs ul, planned p WHERE ul.created_at >= p.window_start AND ul.created_at < p.today_start) AS raw_completed_day_rows,
    (SELECT count(*) FROM usage_logs ul, planned p WHERE ul.created_at >= p.window_start AND ul.created_at < (p.rollup_start_day::timestamp AT TIME ZONE 'Asia/Shanghai')) AS raw_prefloor_rows,
    (SELECT count(*) FROM usage_logs ul, planned p WHERE ul.created_at >= p.today_start AND ul.created_at < p.now_ts) AS raw_today_rows_to_now,
    (SELECT coalesce(sum(total_requests), 0) FROM usage_dashboard_model_daily d, planned p WHERE d.bucket_date > DATE '1970-01-01' AND d.bucket_date >= p.rollup_start_day AND d.bucket_date < p.today_day) AS model_daily_rollup_day_requests
) t;"

if should_explain raw_7d; then
  echo "=== raw_7d_model_group_explain ==="
  $PSQL -c "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
WITH bounds AS (
  SELECT (((now() AT TIME ZONE 'Asia/Shanghai')::date - interval '6 days')::date)::timestamp AT TIME ZONE 'Asia/Shanghai' AS start_at,
         now() AS end_at
)
SELECT
  COALESCE(NULLIF(TRIM(ul.requested_model), ''), ul.model) AS model,
  COUNT(*) AS requests,
  COALESCE(SUM(ul.input_tokens), 0) AS input_tokens,
  COALESCE(SUM(ul.output_tokens), 0) AS output_tokens,
  COALESCE(SUM(ul.cache_creation_tokens), 0) AS cache_creation_tokens,
  COALESCE(SUM(ul.cache_read_tokens), 0) AS cache_read_tokens,
  COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) AS total_tokens,
  COALESCE(SUM(ul.total_cost), 0) AS total_cost,
  COALESCE(SUM(ul.actual_cost), 0) AS actual_cost,
  COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1)), 0) AS account_cost
FROM usage_logs ul, bounds b
WHERE ul.created_at >= b.start_at AND ul.created_at < b.end_at
GROUP BY 1
ORDER BY total_tokens DESC
LIMIT 20;"
fi

if should_explain rollup_completed_days; then
  echo "=== rollup_completed_days_explain ==="
  $PSQL -c "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
  WITH bounds AS (
    SELECT
      ((now() AT TIME ZONE 'Asia/Shanghai')::date - interval '6 days')::date AS start_day,
      (now() AT TIME ZONE 'Asia/Shanghai')::date AS today_day
  ),
  planned AS (
    SELECT
      bounds.*,
      GREATEST(
        bounds.start_day,
        COALESCE((SELECT min(bucket_date) FROM usage_dashboard_model_daily WHERE bucket_date > DATE '1970-01-01'), bounds.today_day)
      ) AS rollup_start_day
    FROM bounds
)
SELECT
  model,
  COALESCE(SUM(total_requests), 0) AS requests,
  COALESCE(SUM(input_tokens), 0) AS input_tokens,
  COALESCE(SUM(output_tokens), 0) AS output_tokens,
  COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
  COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
  COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS total_tokens,
  COALESCE(SUM(total_cost), 0) AS total_cost,
  COALESCE(SUM(actual_cost), 0) AS actual_cost,
  COALESCE(SUM(account_cost), 0) AS account_cost
FROM usage_dashboard_model_daily, planned
WHERE bucket_date > DATE '1970-01-01'
  AND bucket_date >= planned.rollup_start_day AND bucket_date < planned.today_day
GROUP BY model
ORDER BY total_tokens DESC
LIMIT 20;"
fi

if should_explain raw_today; then
  echo "=== raw_today_model_group_explain ==="
  $PSQL -c "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
WITH bounds AS (
  SELECT date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai') AT TIME ZONE 'Asia/Shanghai' AS start_at,
         now() AS end_at
)
SELECT
  COALESCE(NULLIF(TRIM(ul.requested_model), ''), ul.model) AS model,
  COUNT(*) AS requests,
  COALESCE(SUM(ul.input_tokens), 0) AS input_tokens,
  COALESCE(SUM(ul.output_tokens), 0) AS output_tokens,
  COALESCE(SUM(ul.cache_creation_tokens), 0) AS cache_creation_tokens,
  COALESCE(SUM(ul.cache_read_tokens), 0) AS cache_read_tokens,
  COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) AS total_tokens,
  COALESCE(SUM(ul.total_cost), 0) AS total_cost,
  COALESCE(SUM(ul.actual_cost), 0) AS actual_cost,
  COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1)), 0) AS account_cost
FROM usage_logs ul, bounds b
WHERE ul.created_at >= b.start_at AND ul.created_at < b.end_at
GROUP BY 1
ORDER BY total_tokens DESC
LIMIT 20;"
fi

echo "=== raw_vs_rollup_plus_today_delta ==="
$PSQL -c "SELECT row_to_json(t) FROM (
  WITH bounds AS (
    SELECT
      ((now() AT TIME ZONE 'Asia/Shanghai')::date - interval '6 days')::date AS start_day,
      (now() AT TIME ZONE 'Asia/Shanghai')::date AS today_day,
      (((now() AT TIME ZONE 'Asia/Shanghai')::date - interval '6 days')::date)::timestamp AT TIME ZONE 'Asia/Shanghai' AS start_ts,
      ((now() AT TIME ZONE 'Asia/Shanghai')::date)::timestamp AT TIME ZONE 'Asia/Shanghai' AS today_ts,
      now() AS now_ts
  ),
  planned AS (
    SELECT
      bounds.*,
      GREATEST(
        bounds.start_day,
        COALESCE((SELECT min(bucket_date) FROM usage_dashboard_model_daily WHERE bucket_date > DATE '1970-01-01'), bounds.today_day)
      ) AS rollup_start_day
    FROM bounds
  ),
  raw AS (
    SELECT
      COALESCE(NULLIF(TRIM(ul.requested_model), ''), ul.model) AS model,
      COUNT(*)::bigint AS requests,
      COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0)::bigint AS total_tokens,
      COALESCE(SUM(ul.actual_cost), 0)::numeric AS actual_cost
    FROM usage_logs ul, planned b
    WHERE ul.created_at >= b.start_ts AND ul.created_at < b.now_ts
    GROUP BY 1
  ),
  raw_head AS (
    SELECT
      COALESCE(NULLIF(TRIM(ul.requested_model), ''), ul.model) AS model,
      COUNT(*)::bigint AS requests,
      COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0)::bigint AS total_tokens,
      COALESCE(SUM(ul.actual_cost), 0)::numeric AS actual_cost
    FROM usage_logs ul, planned b
    WHERE ul.created_at >= b.start_ts AND ul.created_at < (b.rollup_start_day::timestamp AT TIME ZONE 'Asia/Shanghai')
    GROUP BY 1
  ),
  rollup AS (
    SELECT
      d.model,
      COALESCE(SUM(d.total_requests), 0)::bigint AS requests,
      COALESCE(SUM(d.input_tokens + d.output_tokens + d.cache_creation_tokens + d.cache_read_tokens), 0)::bigint AS total_tokens,
      COALESCE(SUM(d.actual_cost), 0)::numeric AS actual_cost
    FROM usage_dashboard_model_daily d, planned b
    WHERE d.bucket_date > DATE '1970-01-01'
      AND d.bucket_date >= b.rollup_start_day AND d.bucket_date < b.today_day
    GROUP BY d.model
  ),
  today AS (
    SELECT
      COALESCE(NULLIF(TRIM(ul.requested_model), ''), ul.model) AS model,
      COUNT(*)::bigint AS requests,
      COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0)::bigint AS total_tokens,
      COALESCE(SUM(ul.actual_cost), 0)::numeric AS actual_cost
    FROM usage_logs ul, planned b
    WHERE ul.created_at >= b.today_ts AND ul.created_at < b.now_ts
    GROUP BY 1
  ),
  fast_path AS (
    SELECT
      COALESCE(raw_head.model, rollup.model, today.model) AS model,
      COALESCE(raw_head.requests, 0) + COALESCE(rollup.requests, 0) + COALESCE(today.requests, 0) AS requests,
      COALESCE(raw_head.total_tokens, 0) + COALESCE(rollup.total_tokens, 0) + COALESCE(today.total_tokens, 0) AS total_tokens,
      COALESCE(raw_head.actual_cost, 0) + COALESCE(rollup.actual_cost, 0) + COALESCE(today.actual_cost, 0) AS actual_cost
    FROM raw_head
    FULL OUTER JOIN rollup ON rollup.model = raw_head.model
    FULL OUTER JOIN today ON today.model = COALESCE(raw_head.model, rollup.model)
  ),
  deltas AS (
    SELECT
      COALESCE(raw.model, fast_path.model) AS model,
      COALESCE(raw.requests, 0) - COALESCE(fast_path.requests, 0) AS request_delta,
      COALESCE(raw.total_tokens, 0) - COALESCE(fast_path.total_tokens, 0) AS token_delta,
      COALESCE(raw.actual_cost, 0) - COALESCE(fast_path.actual_cost, 0) AS actual_cost_delta
    FROM raw
    FULL OUTER JOIN fast_path ON fast_path.model = raw.model
  )
  SELECT
    COUNT(*) FILTER (
      WHERE request_delta <> 0
        OR token_delta <> 0
        OR ABS(actual_cost_delta) > 0.0000001
    ) AS changed_models,
    COALESCE(SUM(ABS(request_delta)), 0) AS request_delta_sum,
    COALESCE(SUM(ABS(token_delta)), 0) AS token_delta_sum,
    COALESCE(SUM(ABS(actual_cost_delta)), 0) AS actual_cost_delta_sum,
    COALESCE(MAX(abs(request_delta)), 0) AS max_abs_request_delta,
    COALESCE(MAX(abs(token_delta)), 0) AS max_abs_token_delta
  FROM deltas
) t;"
