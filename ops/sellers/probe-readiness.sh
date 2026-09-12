#!/bin/bash
# Run through ops/observability/run-probe.sh on prod. Aggregate evidence only.
set -euo pipefail
docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 <<'SQL'
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '30s';
SET LOCAL TIME ZONE 'UTC';
SELECT json_build_object('section','snapshot','at',now(),'window_end',date_trunc('minute',now()) - interval '5 minutes');
SELECT json_build_object('section','seller_limits','data',row_to_json(t)) FROM (
 SELECT id,status,concurrency,rpm_limit,(balance-frozen_balance>0) AS has_spendable_balance
 FROM users WHERE id=32 AND deleted_at IS NULL
) t;
SELECT json_build_object('section','group_limits','data',row_to_json(t)) FROM (
 SELECT id,platform,status,rpm_limit FROM groups WHERE id IN (1,2,19) AND deleted_at IS NULL
) t;
SELECT json_build_object('section','key_limits','data',row_to_json(t)) FROM (
 SELECT id,status,quota,quota_used,rate_limit_5h,rate_limit_1d,rate_limit_7d,expires_at
 FROM api_keys WHERE user_id=32 AND id=548 AND deleted_at IS NULL
) t;
SELECT json_build_object('section','payment_settings','data',row_to_json(t)) FROM (
 SELECT key,value FROM settings WHERE key IN ('payment_enabled','ENABLED_PAYMENT_TYPES','MIN_RECHARGE_AMOUNT','MAX_RECHARGE_AMOUNT','RECHARGE_FEE_RATE')
) t;
SELECT json_build_object('section','payment_instances','data',row_to_json(t)) FROM (
 SELECT provider_key,enabled,supported_types,payment_mode FROM payment_provider_instances
) t;
WITH bounds AS (
 SELECT date_trunc('minute',now()) - interval '5 minutes' AS ending
), windows AS (
 SELECT minutes,ending-make_interval(mins=>minutes) AS beginning,ending
 FROM bounds CROSS JOIN (VALUES (60),(1440)) AS w(minutes)
), data AS (
 SELECT w.minutes,u.group_id,u.user_id,u.model,u.created_at,
        u.input_tokens::bigint+u.output_tokens+u.cache_creation_tokens+u.cache_read_tokens AS tokens,
        u.input_tokens,u.output_tokens,u.cache_read_tokens,u.cache_creation_tokens,u.duration_ms
 FROM windows w JOIN usage_logs u ON u.created_at>=w.beginning AND u.created_at<w.ending
 WHERE u.group_id IN (1,2,19)
), minute_stats AS (
 SELECT minutes,group_id,date_trunc('minute',created_at) AS minute,count(*) AS rpm,sum(tokens) AS tpm
 FROM data GROUP BY 1,2,3
), peaks AS (
 SELECT minutes,group_id,max(rpm) AS peak_completed_rpm,max(tpm) AS peak_accounted_tpm
 FROM minute_stats GROUP BY 1,2
), totals AS (
 SELECT minutes,group_id,count(*) AS requests,
        round(count(*)::numeric/minutes,2) AS avg_completed_rpm,
        round(sum(tokens)::numeric/minutes,0) AS avg_accounted_tpm,
        round(sum(input_tokens)::numeric/minutes,0) AS uncached_input_tpm,
        round(sum(output_tokens)::numeric/minutes,0) AS output_tpm,
        round(sum(cache_read_tokens)::numeric/minutes,0) AS cache_read_tpm,
        round(sum(cache_creation_tokens)::numeric/minutes,0) AS cache_write_tpm,
        round(avg(duration_ms) FILTER (WHERE duration_ms>0)/1000,2) AS mean_duration_seconds,
        percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms/1000.0) FILTER (WHERE duration_ms>0) AS p95_duration_seconds,
        count(*) FILTER (WHERE duration_ms IS NULL OR duration_ms<=0) AS invalid_duration_rows,
        count(*) FILTER (WHERE user_id=32) AS seller_requests
 FROM data GROUP BY 1,2
)
SELECT json_build_object('section','traffic','data',row_to_json(t)) FROM (
 SELECT totals.*,peaks.peak_completed_rpm,peaks.peak_accounted_tpm
 FROM totals JOIN peaks USING (minutes,group_id) ORDER BY minutes,group_id
) t;
WITH ending AS (SELECT date_trunc('minute',now())-interval '5 minutes' AS at)
SELECT json_build_object('section','model_24h','data',row_to_json(t)) FROM (
 SELECT model,group_id,count(*) AS requests,round(avg(duration_ms) FILTER (WHERE duration_ms>0)/1000,2) AS mean_duration_seconds
 FROM usage_logs CROSS JOIN ending
 WHERE created_at>=ending.at-interval '24 hours' AND created_at<ending.at AND group_id IN (1,2,19)
 GROUP BY model,group_id ORDER BY requests DESC LIMIT 30
) t;
COMMIT;
SQL
