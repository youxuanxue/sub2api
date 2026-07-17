#!/usr/bin/env bash
# probe-admin-group-rollup-timing.sh — read-only timing/coverage probe for
# admin Dashboard/Usage group-distribution rollup candidates.
set -euo pipefail

PSQL="${PSQL:-docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t}"
INCLUDE_EXPLAIN="${INCLUDE_EXPLAIN:-0}"
EXPLAIN_SECTION="${EXPLAIN_SECTION:-all}"
REQUIRED_GROUP_DAILY_COLUMNS="total_requests,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,total_cost,actual_cost,account_cost"

should_explain() {
  [ "$INCLUDE_EXPLAIN" = "1" ] && { [ "$EXPLAIN_SECTION" = "all" ] || [ "$EXPLAIN_SECTION" = "$1" ]; }
}

echo "=== meta ==="
HAS_GROUP_DAILY_METRICS="$($PSQL -c "
SELECT CASE WHEN COUNT(*) = 8 THEN 'true' ELSE 'false' END
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = 'usage_dashboard_group_daily'
  AND column_name = ANY (string_to_array('$REQUIRED_GROUP_DAILY_COLUMNS', ','));
")"

$PSQL -c "
SELECT row_to_json(t)
FROM (
  SELECT
    now() AT TIME ZONE 'UTC' AS now_utc,
    (SELECT MIN(bucket_date) FROM usage_dashboard_group_daily WHERE bucket_date > DATE '1970-01-02') AS group_daily_min_date,
    (SELECT MAX(bucket_date) FROM usage_dashboard_group_daily WHERE bucket_date > DATE '1970-01-02') AS group_daily_max_date,
    (SELECT COUNT(*) FROM usage_dashboard_group_daily) AS group_daily_rows,
    ${HAS_GROUP_DAILY_METRICS}::boolean AS has_group_daily_metrics,
    EXISTS(
      SELECT 1
      FROM usage_dashboard_group_daily
      WHERE group_id = 0 AND bucket_date = DATE '1970-01-01'
    ) AS group_daily_backfilled,
    EXISTS(
      SELECT 1
      FROM usage_dashboard_group_daily
      WHERE group_id = 0 AND bucket_date = DATE '1970-01-02'
    ) AS metrics_backfilled,
    (SELECT last_aggregated_at FROM usage_dashboard_aggregation_watermark WHERE id = 1) AS aggregation_watermark
) t;
"

if should_explain raw_7d; then
  echo "=== raw_7d_group_explain ==="
  $PSQL -c "
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
SELECT
  COALESCE(ul.group_id, 0) AS group_id,
  COUNT(*) AS requests,
  COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) AS total_tokens,
  COALESCE(SUM(ul.total_cost), 0) AS cost,
  COALESCE(SUM(ul.actual_cost), 0) AS actual_cost,
  COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1)), 0) AS account_cost
FROM usage_logs ul
WHERE ul.created_at >= (((now() AT TIME ZONE 'Asia/Shanghai')::date - interval '6 days')::date)::timestamp AT TIME ZONE 'Asia/Shanghai'
  AND ul.created_at < now()
GROUP BY ul.group_id
ORDER BY total_tokens DESC
LIMIT 20;
"
fi

if should_explain rollup_completed_days; then
  echo "=== rollup_completed_days_explain ==="
  if [[ "$HAS_GROUP_DAILY_METRICS" != "true" ]]; then
    echo '{"skipped":true,"reason":"usage_dashboard_group_daily lacks metric columns; run tk_046 migration before rollup timing"}'
  else
    $PSQL -c "
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
SELECT
  gd.group_id,
  COALESCE(SUM(gd.total_requests), 0) AS requests,
  COALESCE(SUM(gd.input_tokens + gd.output_tokens + gd.cache_creation_tokens + gd.cache_read_tokens), 0) AS total_tokens,
  COALESCE(SUM(gd.total_cost), 0) AS cost,
  COALESCE(SUM(gd.actual_cost), 0) AS actual_cost,
  COALESCE(SUM(gd.account_cost), 0) AS account_cost
FROM usage_dashboard_group_daily gd
WHERE gd.bucket_date > DATE '1970-01-02'
  AND gd.bucket_date >= ((now() AT TIME ZONE 'Asia/Shanghai')::date - interval '6 days')::date
  AND gd.bucket_date < (now() AT TIME ZONE 'Asia/Shanghai')::date
GROUP BY gd.group_id
ORDER BY total_tokens DESC
LIMIT 20;
"
  fi
fi

if should_explain raw_today; then
  echo "=== raw_today_group_explain ==="
  $PSQL -c "
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
SELECT
  COALESCE(ul.group_id, 0) AS group_id,
  COUNT(*) AS requests,
  COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) AS total_tokens,
  COALESCE(SUM(ul.total_cost), 0) AS cost,
  COALESCE(SUM(ul.actual_cost), 0) AS actual_cost,
  COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1)), 0) AS account_cost
FROM usage_logs ul
WHERE ul.created_at >= date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai') AT TIME ZONE 'Asia/Shanghai'
  AND ul.created_at < now()
GROUP BY ul.group_id
ORDER BY total_tokens DESC
LIMIT 20;
"
fi

echo "=== raw_vs_rollup_completed_days_delta ==="
if [[ "$HAS_GROUP_DAILY_METRICS" != "true" ]]; then
  echo '{"skipped":true,"reason":"usage_dashboard_group_daily lacks metric columns; run tk_046 migration before consistency diff"}'
else
  $PSQL -c "
WITH bounds AS (
  SELECT
    ((now() AT TIME ZONE 'Asia/Shanghai')::date - interval '6 days')::date AS start_day,
    (now() AT TIME ZONE 'Asia/Shanghai')::date AS today_day,
    (((now() AT TIME ZONE 'Asia/Shanghai')::date - interval '6 days')::date)::timestamp AT TIME ZONE 'Asia/Shanghai' AS start_ts,
    ((now() AT TIME ZONE 'Asia/Shanghai')::date)::timestamp AT TIME ZONE 'Asia/Shanghai' AS today_ts
),
raw AS (
  SELECT
    COALESCE(group_id, 0) AS group_id,
    COUNT(*)::bigint AS requests,
    COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0)::bigint AS tokens,
    COALESCE(SUM(actual_cost), 0)::numeric AS actual_cost
  FROM usage_logs, bounds
  WHERE created_at >= bounds.start_ts AND created_at < bounds.today_ts
  GROUP BY COALESCE(group_id, 0)
),
rollup AS (
  SELECT
    group_id,
    COALESCE(SUM(total_requests), 0)::bigint AS requests,
    COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0)::bigint AS tokens,
    COALESCE(SUM(actual_cost), 0)::numeric AS actual_cost
  FROM usage_dashboard_group_daily, bounds
  WHERE bucket_date > DATE '1970-01-02'
    AND bucket_date >= bounds.start_day
    AND bucket_date < bounds.today_day
  GROUP BY group_id
),
joined AS (
  SELECT
    COALESCE(raw.group_id, rollup.group_id) AS group_id,
    COALESCE(raw.requests, 0) - COALESCE(rollup.requests, 0) AS request_delta,
    COALESCE(raw.tokens, 0) - COALESCE(rollup.tokens, 0) AS token_delta,
    COALESCE(raw.actual_cost, 0) - COALESCE(rollup.actual_cost, 0) AS actual_cost_delta
  FROM raw
  FULL OUTER JOIN rollup USING (group_id)
)
SELECT row_to_json(t)
FROM (
  SELECT
    COUNT(*) FILTER (
      WHERE request_delta <> 0
        OR token_delta <> 0
        OR ABS(actual_cost_delta) > 0.0000001
    ) AS changed_groups,
    COALESCE(SUM(ABS(request_delta)), 0) AS request_delta_sum,
    COALESCE(SUM(ABS(token_delta)), 0) AS token_delta_sum,
    COALESCE(SUM(ABS(actual_cost_delta)), 0) AS actual_cost_delta_sum,
    COALESCE(MAX(ABS(request_delta)), 0) AS max_abs_request_delta,
    COALESCE(MAX(ABS(token_delta)), 0) AS max_abs_token_delta
  FROM joined
) t;
"
fi
