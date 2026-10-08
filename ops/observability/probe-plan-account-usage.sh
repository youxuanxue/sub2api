#!/usr/bin/env bash
# probe-plan-account-usage.sh — read-only plan-account metering for a period.
#
# Env (required):
#   PERIOD_START  Asia/Shanghai-bound instant, e.g. 2026-09-01 00:00:00+08
#   PERIOD_END    exclusive end, e.g. 2026-10-01 00:00:00+08
#
# Delivered via run-probe.sh. Never prints credentials/api keys.
#
# Per matching account:
#   - period totals (month_period)
#   - max non-overlapping 5h / 7d tiles aligned to PERIOD_START
#   - peak RPM / TPM (per-minute) inside the period
set -euo pipefail

PERIOD_START="${PERIOD_START:?PERIOD_START required (e.g. 2026-09-01 00:00:00+08)}"
PERIOD_END="${PERIOD_END:?PERIOD_END required (e.g. 2026-10-01 00:00:00+08)}"

# Reject anything that is not a safe timestamptz literal fragment.
if [[ ! "$PERIOD_START" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}([ T][0-9]{2}:[0-9]{2}(:[0-9]{2})?)?([ ]?[+][0-9]{2}(:?[0-9]{2})?)?$ ]]; then
  echo "invalid PERIOD_START=$PERIOD_START" >&2
  exit 2
fi
if [[ ! "$PERIOD_END" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}([ T][0-9]{2}:[0-9]{2}(:[0-9]{2})?)?([ ]?[+][0-9]{2}(:?[0-9]{2})?)?$ ]]; then
  echo "invalid PERIOD_END=$PERIOD_END" >&2
  exit 2
fi

PSQL=(docker exec -i
  -e 'PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=2s -c statement_timeout=300s'
  tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1
  -v "period_start=${PERIOD_START}"
  -v "period_end=${PERIOD_END}")

echo "=== probe_plan_account_usage db_now_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ) host=$(hostname -s) period_start=${PERIOD_START} period_end=${PERIOD_END} ==="

"${PSQL[@]}" <<'SQL'
WITH bounds AS (
  SELECT
    :'period_start'::timestamptz AS period_start,
    :'period_end'::timestamptz AS period_end
),
scoped AS (
  SELECT
    a.id,
    a.name,
    a.platform,
    a.status,
    a.schedulable,
    a.channel_type,
    a.created_at AT TIME ZONE 'UTC' AS created_at_utc,
    a.last_used_at AT TIME ZONE 'UTC' AS last_used_at_utc,
    lower(rtrim(COALESCE(a.credentials->>'base_url', ''), '/')) AS base_url,
    CASE
      WHEN a.channel_type = 45
        AND (
          lower(rtrim(COALESCE(a.credentials->>'base_url', ''), '/'))
            LIKE 'https://ark.cn-beijing.volces.com/api/plan/v3%'
          OR lower(COALESCE(a.credentials->>'base_url', '')) IN ('doubao-agent-plan')
        ) THEN 'volcengine_agent_plan'
      WHEN a.channel_type = 17
        AND lower(rtrim(COALESCE(a.credentials->>'base_url', ''), '/'))
            LIKE 'https://token-plan.cn-beijing.maas.aliyuncs.com%'
        THEN 'ali_token_plan'
      WHEN a.channel_type = 46
        AND (
          lower(rtrim(COALESCE(a.credentials->>'base_url', ''), '/'))
            LIKE 'https://qianfan.baidubce.com/v2/tokenplan/personal%'
          OR lower(rtrim(COALESCE(a.credentials->>'base_url', ''), '/'))
            LIKE 'https://qianfan.baidubce.com/anthropic/tokenplan/personal%'
        ) THEN 'qianfan_token_plan'
      WHEN a.channel_type = 1
        AND lower(rtrim(COALESCE(a.credentials->>'base_url', ''), '/'))
            = 'https://integrate.api.nvidia.com'
        THEN 'nvidia_build'
      ELSE NULL
    END AS plan_kind
  FROM accounts a
  WHERE a.deleted_at IS NULL
),
targets AS (
  SELECT * FROM scoped WHERE plan_kind IS NOT NULL
),
month_usage AS (
  SELECT
    ul.account_id,
    count(*)::bigint AS reqs,
    coalesce(sum(ul.input_tokens + ul.output_tokens
      + ul.cache_creation_tokens + ul.cache_read_tokens), 0)::bigint AS tokens,
    round(coalesce(sum(ul.total_cost), 0)::numeric, 6) AS total_cost,
    round(coalesce(sum(ul.actual_cost), 0)::numeric, 6) AS actual_cost
  FROM usage_logs ul, bounds b
  WHERE ul.account_id IN (SELECT id FROM targets)
    AND ul.created_at >= b.period_start
    AND ul.created_at < b.period_end
  GROUP BY ul.account_id
),
-- Non-overlapping tiles aligned to period_start (not rolling 1h/1d steps).
slots_5h AS (
  SELECT
    gs AS window_start,
    gs + interval '5 hours' AS window_end
  FROM bounds b,
       generate_series(
         b.period_start,
         b.period_end - interval '5 hours',
         interval '5 hours'
       ) AS gs
),
cost_5h_slots AS (
  SELECT
    t.id AS account_id,
    s.window_start,
    s.window_end,
    count(ul.*)::bigint AS reqs,
    coalesce(sum(ul.input_tokens + ul.output_tokens
      + ul.cache_creation_tokens + ul.cache_read_tokens), 0)::bigint AS tokens,
    round(coalesce(sum(ul.total_cost), 0)::numeric, 6) AS total_cost,
    round(coalesce(sum(ul.actual_cost), 0)::numeric, 6) AS actual_cost
  FROM targets t
  CROSS JOIN slots_5h s
  LEFT JOIN usage_logs ul
    ON ul.account_id = t.id
   AND ul.created_at >= s.window_start
   AND ul.created_at < s.window_end
  GROUP BY t.id, s.window_start, s.window_end
),
max_5h AS (
  SELECT DISTINCT ON (account_id)
    account_id, window_start, window_end, reqs, tokens, total_cost, actual_cost
  FROM cost_5h_slots
  ORDER BY account_id, total_cost DESC, tokens DESC, window_end DESC
),
slots_7d AS (
  SELECT
    gs AS window_start,
    gs + interval '7 days' AS window_end
  FROM bounds b,
       generate_series(
         b.period_start,
         b.period_end - interval '7 days',
         interval '7 days'
       ) AS gs
),
cost_7d_slots AS (
  SELECT
    t.id AS account_id,
    s.window_start,
    s.window_end,
    count(ul.*)::bigint AS reqs,
    coalesce(sum(ul.input_tokens + ul.output_tokens
      + ul.cache_creation_tokens + ul.cache_read_tokens), 0)::bigint AS tokens,
    round(coalesce(sum(ul.total_cost), 0)::numeric, 6) AS total_cost,
    round(coalesce(sum(ul.actual_cost), 0)::numeric, 6) AS actual_cost
  FROM targets t
  CROSS JOIN slots_7d s
  LEFT JOIN usage_logs ul
    ON ul.account_id = t.id
   AND ul.created_at >= s.window_start
   AND ul.created_at < s.window_end
  GROUP BY t.id, s.window_start, s.window_end
),
max_7d AS (
  SELECT DISTINCT ON (account_id)
    account_id, window_start, window_end, reqs, tokens, total_cost, actual_cost
  FROM cost_7d_slots
  ORDER BY account_id, total_cost DESC, tokens DESC, window_end DESC
),
peaks AS (
  SELECT
    account_id,
    max(rpm)::bigint AS peak_rpm,
    max(tpm)::bigint AS peak_tpm,
    (array_agg(minute_utc ORDER BY rpm DESC, tpm DESC, minute_utc DESC))[1] AS peak_rpm_at,
    (array_agg(minute_utc ORDER BY tpm DESC, rpm DESC, minute_utc DESC))[1] AS peak_tpm_at
  FROM (
    SELECT
      ul.account_id,
      date_trunc('minute', ul.created_at) AS minute_utc,
      count(*)::bigint AS rpm,
      coalesce(sum(ul.input_tokens + ul.output_tokens
        + ul.cache_creation_tokens + ul.cache_read_tokens), 0)::bigint AS tpm
    FROM usage_logs ul, bounds b
    WHERE ul.account_id IN (SELECT id FROM targets)
      AND ul.created_at >= b.period_start
      AND ul.created_at < b.period_end
    GROUP BY 1, 2
  ) m
  GROUP BY account_id
)
SELECT row_to_json(t)
FROM (
  SELECT
    tg.plan_kind,
    tg.id AS account_id,
    tg.name,
    tg.platform,
    tg.status,
    tg.schedulable,
    tg.channel_type,
    tg.base_url,
    tg.created_at_utc,
    tg.last_used_at_utc,
    'Asia/Shanghai'::text AS period_tz,
    b.period_start AS period_start,
    b.period_end AS period_end,
    jsonb_build_object(
      'reqs', coalesce(mu.reqs, 0),
      'tokens', coalesce(mu.tokens, 0),
      'total_cost', coalesce(mu.total_cost, 0),
      'actual_cost', coalesce(mu.actual_cost, 0)
    ) AS month_period,
    jsonb_build_object(
      'selection', 'max_cost',
      'window_start', m5.window_start,
      'window_end', m5.window_end,
      'reqs', coalesce(m5.reqs, 0),
      'tokens', coalesce(m5.tokens, 0),
      'total_cost', coalesce(m5.total_cost, 0),
      'actual_cost', coalesce(m5.actual_cost, 0)
    ) AS max_5h,
    jsonb_build_object(
      'selection', 'max_cost',
      'window_start', m7.window_start,
      'window_end', m7.window_end,
      'reqs', coalesce(m7.reqs, 0),
      'tokens', coalesce(m7.tokens, 0),
      'total_cost', coalesce(m7.total_cost, 0),
      'actual_cost', coalesce(m7.actual_cost, 0)
    ) AS max_7d,
    jsonb_build_object(
      'peak_rpm', coalesce(p.peak_rpm, 0),
      'peak_tpm', coalesce(p.peak_tpm, 0),
      'peak_rpm_at', p.peak_rpm_at,
      'peak_tpm_at', p.peak_tpm_at
    ) AS peaks
  FROM targets tg
  CROSS JOIN bounds b
  LEFT JOIN month_usage mu ON mu.account_id = tg.id
  LEFT JOIN max_5h m5 ON m5.account_id = tg.id
  LEFT JOIN max_7d m7 ON m7.account_id = tg.id
  LEFT JOIN peaks p ON p.account_id = tg.id
  ORDER BY tg.plan_kind, tg.id
) t;
SQL

echo "=== summary_by_plan_kind ==="
"${PSQL[@]}" <<'SQL'
WITH scoped AS (
  SELECT a.id,
    CASE
      WHEN a.channel_type = 45
        AND (
          lower(rtrim(COALESCE(a.credentials->>'base_url', ''), '/'))
            LIKE 'https://ark.cn-beijing.volces.com/api/plan/v3%'
          OR lower(COALESCE(a.credentials->>'base_url', '')) IN ('doubao-agent-plan')
        ) THEN 'volcengine_agent_plan'
      WHEN a.channel_type = 17
        AND lower(rtrim(COALESCE(a.credentials->>'base_url', ''), '/'))
            LIKE 'https://token-plan.cn-beijing.maas.aliyuncs.com%'
        THEN 'ali_token_plan'
      WHEN a.channel_type = 46
        AND (
          lower(rtrim(COALESCE(a.credentials->>'base_url', ''), '/'))
            LIKE 'https://qianfan.baidubce.com/v2/tokenplan/personal%'
          OR lower(rtrim(COALESCE(a.credentials->>'base_url', ''), '/'))
            LIKE 'https://qianfan.baidubce.com/anthropic/tokenplan/personal%'
        ) THEN 'qianfan_token_plan'
      WHEN a.channel_type = 1
        AND lower(rtrim(COALESCE(a.credentials->>'base_url', ''), '/'))
            = 'https://integrate.api.nvidia.com'
        THEN 'nvidia_build'
      ELSE NULL
    END AS plan_kind
  FROM accounts a
  WHERE a.deleted_at IS NULL
)
SELECT row_to_json(t) FROM (
  SELECT plan_kind, count(*) AS accounts
  FROM scoped WHERE plan_kind IS NOT NULL
  GROUP BY plan_kind ORDER BY plan_kind
) t;
SQL
