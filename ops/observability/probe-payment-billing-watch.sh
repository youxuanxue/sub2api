#!/bin/bash
# probe-payment-billing-watch.sh — read-only prod payment/recharge snapshot for
# weekly Feishu reports and attack-pattern alerts.
#
# Runs INSIDE the TokenKey host via run-probe.sh. Emits one JSON document on
# stdout (schema_version=1). Env knobs:
#   ANOMALY_WINDOW_MINUTES  default 15
#   RAPID_WINDOW_SECONDS    default 60
set -euo pipefail

ANOMALY_WINDOW_MINUTES="${ANOMALY_WINDOW_MINUTES:-15}"
RAPID_WINDOW_SECONDS="${RAPID_WINDOW_SECONDS:-60}"
if [[ ! "$ANOMALY_WINDOW_MINUTES" =~ ^[0-9]+$ ]] || [[ ! "$RAPID_WINDOW_SECONDS" =~ ^[0-9]+$ ]]; then
  echo "probe-payment-billing-watch: ANOMALY_WINDOW_MINUTES and RAPID_WINDOW_SECONDS must be non-negative integers" >&2
  exit 1
fi
PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'

$PSQL -v ON_ERROR_STOP=1 -c "
SELECT json_build_object(
  'schema_version', 1,
  'db_now_utc', to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),
  'anomaly_window_minutes', ${ANOMALY_WINDOW_MINUTES}::int,
  'rapid_window_seconds', ${RAPID_WINDOW_SECONDS}::int,

  'period_totals', (
    SELECT coalesce(json_agg(row_to_json(p) ORDER BY p.period), '[]'::json)
    FROM (
      SELECT * FROM (
        SELECT 'last_7d'::text AS period,
               count(*) FILTER (WHERE status = 'COMPLETED') AS completed_n,
               round(coalesce(sum(amount) FILTER (WHERE status = 'COMPLETED'), 0)::numeric, 2) AS completed_amount,
               count(*) FILTER (WHERE status <> 'COMPLETED') AS non_completed_n,
               round(coalesce(sum(amount) FILTER (WHERE status <> 'COMPLETED'), 0)::numeric, 2) AS non_completed_amount,
               count(DISTINCT user_id) FILTER (WHERE status = 'COMPLETED') AS completed_users
        FROM payment_orders
        WHERE created_at >= now() - interval '7 days'
        UNION ALL
        SELECT 'prev_calendar_month',
               count(*) FILTER (WHERE status = 'COMPLETED'),
               round(coalesce(sum(amount) FILTER (WHERE status = 'COMPLETED'), 0)::numeric, 2),
               count(*) FILTER (WHERE status <> 'COMPLETED'),
               round(coalesce(sum(amount) FILTER (WHERE status <> 'COMPLETED'), 0)::numeric, 2),
               count(DISTINCT user_id) FILTER (WHERE status = 'COMPLETED')
        FROM payment_orders
        WHERE created_at >= date_trunc('month', now() AT TIME ZONE 'UTC') - interval '1 month'
          AND created_at < date_trunc('month', now() AT TIME ZONE 'UTC')
        UNION ALL
        SELECT 'current_calendar_month',
               count(*) FILTER (WHERE status = 'COMPLETED'),
               round(coalesce(sum(amount) FILTER (WHERE status = 'COMPLETED'), 0)::numeric, 2),
               count(*) FILTER (WHERE status <> 'COMPLETED'),
               round(coalesce(sum(amount) FILTER (WHERE status <> 'COMPLETED'), 0)::numeric, 2),
               count(DISTINCT user_id) FILTER (WHERE status = 'COMPLETED')
        FROM payment_orders
        WHERE created_at >= date_trunc('month', now() AT TIME ZONE 'UTC')
        UNION ALL
        SELECT 'all_time',
               count(*) FILTER (WHERE status = 'COMPLETED'),
               round(coalesce(sum(amount) FILTER (WHERE status = 'COMPLETED'), 0)::numeric, 2),
               count(*) FILTER (WHERE status <> 'COMPLETED'),
               round(coalesce(sum(amount) FILTER (WHERE status <> 'COMPLETED'), 0)::numeric, 2),
               count(DISTINCT user_id) FILTER (WHERE status = 'COMPLETED')
        FROM payment_orders
      ) x
    ) p
  ),

  'completed_by_provider', (
    SELECT coalesce(json_agg(row_to_json(x) ORDER BY x.period, x.provider_key, x.payment_type), '[]'::json)
    FROM (
      SELECT period, provider_key, payment_type, n, amount FROM (
        SELECT 'last_7d'::text AS period,
               coalesce(provider_key, '(null)') AS provider_key,
               coalesce(payment_type, '(null)') AS payment_type,
               count(*) AS n,
               round(sum(amount)::numeric, 2) AS amount
        FROM payment_orders
        WHERE created_at >= now() - interval '7 days' AND status = 'COMPLETED'
        GROUP BY 2, 3
        UNION ALL
        SELECT 'prev_calendar_month',
               coalesce(provider_key, '(null)'),
               coalesce(payment_type, '(null)'),
               count(*),
               round(sum(amount)::numeric, 2)
        FROM payment_orders
        WHERE created_at >= date_trunc('month', now() AT TIME ZONE 'UTC') - interval '1 month'
          AND created_at < date_trunc('month', now() AT TIME ZONE 'UTC')
          AND status = 'COMPLETED'
        GROUP BY 2, 3
        UNION ALL
        SELECT 'current_calendar_month',
               coalesce(provider_key, '(null)'),
               coalesce(payment_type, '(null)'),
               count(*),
               round(sum(amount)::numeric, 2)
        FROM payment_orders
        WHERE created_at >= date_trunc('month', now() AT TIME ZONE 'UTC')
          AND status = 'COMPLETED'
        GROUP BY 2, 3
        UNION ALL
        SELECT 'all_time',
               coalesce(provider_key, '(null)'),
               coalesce(payment_type, '(null)'),
               count(*),
               round(sum(amount)::numeric, 2)
        FROM payment_orders
        WHERE status = 'COMPLETED'
        GROUP BY 2, 3
      ) s
    ) x
  ),

  'admin_credits', (
    SELECT coalesce(json_agg(row_to_json(x) ORDER BY x.period, x.notes_kind), '[]'::json)
    FROM (
      SELECT period, notes_kind, n, amount, users FROM (
        SELECT 'last_7d'::text AS period,
               CASE
                 WHEN type = 'balance' THEN 'payment_fulfillment'
                 WHEN coalesce(notes, '') = '开户期初余额（管理员）' THEN 'admin_opening'
                 WHEN coalesce(notes, '') IN ('注册初始余额', '邀请试用赠予', 'OAuth首次绑定默认余额') THEN 'signup_gift'
                 WHEN coalesce(notes, '') = '' THEN 'admin_adjust'
                 ELSE 'admin_other'
               END AS notes_kind,
               count(*) AS n,
               round(sum(value)::numeric, 2) AS amount,
               count(DISTINCT used_by) AS users
        FROM redeem_codes
        WHERE used_at >= now() - interval '7 days'
          AND type IN ('balance', 'admin_balance')
          AND value > 0
        GROUP BY 2
        UNION ALL
        SELECT 'prev_calendar_month',
               CASE
                 WHEN type = 'balance' THEN 'payment_fulfillment'
                 WHEN coalesce(notes, '') = '开户期初余额（管理员）' THEN 'admin_opening'
                 WHEN coalesce(notes, '') IN ('注册初始余额', '邀请试用赠予', 'OAuth首次绑定默认余额') THEN 'signup_gift'
                 WHEN coalesce(notes, '') = '' THEN 'admin_adjust'
                 ELSE 'admin_other'
               END,
               count(*),
               round(sum(value)::numeric, 2),
               count(DISTINCT used_by)
        FROM redeem_codes
        WHERE used_at >= date_trunc('month', now() AT TIME ZONE 'UTC') - interval '1 month'
          AND used_at < date_trunc('month', now() AT TIME ZONE 'UTC')
          AND type IN ('balance', 'admin_balance')
          AND value > 0
        GROUP BY 2
        UNION ALL
        SELECT 'current_calendar_month',
               CASE
                 WHEN type = 'balance' THEN 'payment_fulfillment'
                 WHEN coalesce(notes, '') = '开户期初余额（管理员）' THEN 'admin_opening'
                 WHEN coalesce(notes, '') IN ('注册初始余额', '邀请试用赠予', 'OAuth首次绑定默认余额') THEN 'signup_gift'
                 WHEN coalesce(notes, '') = '' THEN 'admin_adjust'
                 ELSE 'admin_other'
               END,
               count(*),
               round(sum(value)::numeric, 2),
               count(DISTINCT used_by)
        FROM redeem_codes
        WHERE used_at >= date_trunc('month', now() AT TIME ZONE 'UTC')
          AND type IN ('balance', 'admin_balance')
          AND value > 0
        GROUP BY 2
        UNION ALL
        SELECT 'all_time',
               CASE
                 WHEN type = 'balance' THEN 'payment_fulfillment'
                 WHEN coalesce(notes, '') = '开户期初余额（管理员）' THEN 'admin_opening'
                 WHEN coalesce(notes, '') IN ('注册初始余额', '邀请试用赠予', 'OAuth首次绑定默认余额') THEN 'signup_gift'
                 WHEN coalesce(notes, '') = '' THEN 'admin_adjust'
                 ELSE 'admin_other'
               END,
               count(*),
               round(sum(value)::numeric, 2),
               count(DISTINCT used_by)
        FROM redeem_codes
        WHERE type IN ('balance', 'admin_balance')
          AND value > 0
          AND used_at IS NOT NULL
        GROUP BY 2
      ) s
    ) x
  ),

  'completed_detail_7d', (
    SELECT coalesce(json_agg(row_to_json(d) ORDER BY d.paid_at_utc NULLS LAST, d.id), '[]'::json)
    FROM (
      SELECT o.id,
             o.user_id,
             o.user_email,
             o.amount::float8 AS amount,
             o.payment_type,
             o.provider_key,
             coalesce(o.payment_trade_no, '') AS payment_trade_no,
             o.out_trade_no,
             to_char(o.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS created_at_utc,
             to_char(o.paid_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS paid_at_utc,
             CASE
               WHEN o.paid_at IS NULL THEN NULL
               ELSE round(extract(epoch FROM (o.paid_at - o.created_at))::numeric, 3)
             END AS paid_after_seconds
      FROM payment_orders o
      WHERE o.status = 'COMPLETED'
        AND o.created_at >= now() - interval '7 days'
      ORDER BY o.paid_at NULLS LAST, o.id
      LIMIT 50
    ) d
  ),

  'anomaly_cancel_storms', (
    SELECT coalesce(json_agg(row_to_json(a) ORDER BY a.n DESC, a.user_id), '[]'::json)
    FROM (
      SELECT o.user_id,
             max(o.user_email) AS user_email,
             count(*) AS n,
             round(sum(o.amount)::numeric, 2) AS sum_amount,
             array_agg(DISTINCT o.provider_key ORDER BY o.provider_key) AS providers,
             array_agg(DISTINCT o.status ORDER BY o.status) AS statuses,
             to_char(min(o.created_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS first_at_utc,
             to_char(max(o.created_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS last_at_utc
      FROM payment_orders o
      WHERE o.created_at >= now() - make_interval(mins => ${ANOMALY_WINDOW_MINUTES}::int)
        AND o.status IN ('CANCELLED', 'EXPIRED', 'FAILED')
      GROUP BY o.user_id
      HAVING count(*) >= 5
      ORDER BY count(*) DESC, o.user_id
      LIMIT 20
    ) a
  ),

  'anomaly_rapid_creates', (
    SELECT coalesce(json_agg(row_to_json(a) ORDER BY a.n DESC, a.user_id), '[]'::json)
    FROM (
      SELECT o.user_id,
             max(o.user_email) AS user_email,
             count(*) AS n,
             round(sum(o.amount)::numeric, 2) AS sum_amount,
             array_agg(DISTINCT o.status ORDER BY o.status) AS statuses,
             to_char(min(o.created_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS first_at_utc,
             to_char(max(o.created_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS last_at_utc,
             round(extract(epoch FROM (max(o.created_at) - min(o.created_at)))::numeric, 3) AS span_seconds
      FROM payment_orders o
      WHERE o.created_at >= now() - make_interval(mins => ${ANOMALY_WINDOW_MINUTES}::int)
      GROUP BY o.user_id
      HAVING count(*) >= 3
         AND extract(epoch FROM (max(o.created_at) - min(o.created_at))) <= ${RAPID_WINDOW_SECONDS}::int
      ORDER BY count(*) DESC, o.user_id
      LIMIT 20
    ) a
  ),

  'anomaly_suspicious_completed', (
    SELECT coalesce(json_agg(row_to_json(a) ORDER BY a.id), '[]'::json)
    FROM (
      SELECT o.id,
             o.user_id,
             o.user_email,
             o.amount::float8 AS amount,
             o.provider_key,
             o.payment_type,
             coalesce(o.payment_trade_no, '') AS payment_trade_no,
             o.out_trade_no,
             to_char(o.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS created_at_utc,
             to_char(o.paid_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS paid_at_utc,
             round(extract(epoch FROM (o.paid_at - o.created_at))::numeric, 3) AS paid_after_seconds
      FROM payment_orders o
      WHERE o.status = 'COMPLETED'
        AND o.paid_at IS NOT NULL
        AND o.created_at >= now() - make_interval(mins => ${ANOMALY_WINDOW_MINUTES}::int)
        -- Forged EasyPay callbacks typically leave payment_trade_no empty; do not
        -- alert on sub-5s settle alone (legit Stripe can be that fast).
        AND coalesce(o.payment_trade_no, '') = ''
      ORDER BY o.id
      LIMIT 20
    ) a
  ),

  'providers', (
    SELECT coalesce(json_agg(row_to_json(p) ORDER BY p.id), '[]'::json)
    FROM (
      SELECT id, provider_key, name, enabled, payment_mode, supported_types
      FROM payment_provider_instances
      ORDER BY id
    ) p
  )
)::text;
"
