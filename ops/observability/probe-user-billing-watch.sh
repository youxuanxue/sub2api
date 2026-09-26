#!/usr/bin/env bash
# probe-user-billing-watch.sh — read-only per-user 盯盘 for active user_ids:
# requests, errors, metering/billing, plus an image/video breakout — all in one
# SSM round-trip. Runs INSIDE the TokenKey host (prod or edge) via run-probe.sh.
# Output is row_to_json so parsing is field-named, not column-index.
#
#   bash ops/observability/run-probe.sh --target prod \
#     --script ops/observability/probe-user-billing-watch.sh \
#     --env WINDOW_MINUTES=30 [--env USER_IDS=1,6,16,38]
#
# USER_IDS        optional comma-separated integer user ids override
# WINDOW_MINUTES  look-back window in minutes (default 30; matches report cadence)
#
# image/video discriminators reuse probe-image-video-billing.sh's proven predicates.
#
# 环比 and anomaly baselines are computed HERE, in SQL — not left to the
# caller's memory or mental arithmetic. Every run emits:
#   - current window totals (requests/cost, errors)
#   - window-over-window 环比 rows with delta_*_pct already calculated
#   - trailing-24h per-bucket baseline (avg/max), so "is this a spike"
#     compares against a real distribution, not two raw points.
#   - user-facing failures (status_code <> 200) already joined to model, serving
#     account, group/key and a root-cause sample, so the terminal-impact basics
#     never need a second round-trip or a manual cross-join.
set -u

USER_IDS_OVERRIDE="${USER_IDS:-}"
WINDOW_MINUTES="${WINDOW_MINUTES:-30}"
# Validate: digits and commas only (SQL IN-list interpolation guard).
case "$USER_IDS_OVERRIDE" in ''|*[!0-9,]*) [ -n "$USER_IDS_OVERRIDE" ] && { echo "bad USER_IDS (want comma-separated ints)" >&2; exit 2; } ;;
esac
case "$WINDOW_MINUTES" in ''|*[!0-9]*) echo "bad WINDOW_MINUTES (want integer)" >&2; exit 2;; esac

PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'
W="interval '${WINDOW_MINUTES} minutes'"

if [ -n "$USER_IDS_OVERRIDE" ]; then
  IDS="$USER_IDS_OVERRIDE"
else
  DISCOVERY_SQL="SELECT COALESCE(string_agg(id::text, ',' ORDER BY id), '') FROM users WHERE status = 'active' AND deleted_at IS NULL;"
  if ! ACTIVE_USER_IDS="$($PSQL -c "$DISCOVERY_SQL")"; then
    echo "active-user discovery failed" >&2
    exit 1
  fi
  IDS="$(printf '%s' "$ACTIVE_USER_IDS" | tr -d '[:space:]')"
fi
if [ -z "$IDS" ]; then
  echo "no active users found" >&2
  exit 3
fi
# Validate after discovery/override: digits and commas only (SQL IN-list interpolation guard).
case "$IDS" in ''|*[!0-9,]*) echo "bad USER_IDS (want comma-separated ints)" >&2; exit 2;; esac

# usage_logs image/video predicates
IMG_U="(billing_mode = 'image' OR COALESCE(image_count,0) > 0 OR inbound_endpoint ILIKE '%image%')"
VID_U="(billing_mode = 'video' OR video_duration_seconds IS NOT NULL OR inbound_endpoint ILIKE '%video%')"
# ops_error_logs image/video predicates
IMG_E="(request_path ILIKE '%/images%' OR inbound_endpoint ILIKE '%image%')"
VID_E="(request_path ILIKE '%/video%' OR inbound_endpoint ILIKE '%video%')"

echo "=== meta ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  now() AT TIME ZONE 'UTC'           AS now_utc,
  now() AT TIME ZONE 'Asia/Shanghai' AS now_cst,
  '${IDS}'::text                     AS user_ids,
  ${WINDOW_MINUTES}::int             AS window_minutes) t;" 2>&1

echo
echo "=== active users (discovery) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  id, email, username, status FROM users WHERE status = 'active' AND deleted_at IS NULL ORDER BY id) t;" 2>&1

echo
echo "=== selected users ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  id, email, username, status FROM users WHERE id IN (${IDS}) AND deleted_at IS NULL ORDER BY id) t;" 2>&1

# ---------------------------------------------------------------------------
# GENERAL — per-user requests + billing (usage_logs success path)
# ---------------------------------------------------------------------------
echo
echo "=== general: usage_logs per-user totals (current window) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  user_id,
  count(*)                                          AS reqs,
  count(*) FILTER (WHERE COALESCE(total_cost,0) > 0) AS billed_reqs,
  count(*) FILTER (WHERE COALESCE(total_cost,0) = 0) AS zero_cost_reqs,
  ROUND(COALESCE(sum(total_cost),0)::numeric,6)      AS total_cost,
  ROUND(COALESCE(sum(actual_cost),0)::numeric,6)     AS actual_cost,
  min(created_at) AT TIME ZONE 'UTC'                 AS first_at_utc,
  max(created_at) AT TIME ZONE 'UTC'                 AS last_at_utc
  FROM usage_logs
  WHERE user_id IN (${IDS}) AND created_at >= now() - ${W}
  GROUP BY user_id ORDER BY user_id) t;" 2>&1

echo
echo "=== general: usage_logs per-user 环比 (current vs previous window) ==="
$PSQL -c "WITH win AS (
  SELECT user_id, total_cost, (created_at >= now() - ${W}) AS is_cur
  FROM usage_logs
  WHERE user_id IN (${IDS}) AND created_at >= now() - 2*${W}
)
SELECT row_to_json(t) FROM (SELECT
  user_id,
  count(*) FILTER (WHERE is_cur) AS reqs,
  count(*) FILTER (WHERE NOT is_cur) AS prev_reqs,
  ROUND(COALESCE(sum(total_cost) FILTER (WHERE is_cur),0)::numeric,6) AS total_cost,
  ROUND(COALESCE(sum(total_cost) FILTER (WHERE NOT is_cur),0)::numeric,6) AS prev_total_cost,
  CASE WHEN count(*) FILTER (WHERE NOT is_cur) = 0 THEN NULL
       ELSE ROUND(((count(*) FILTER (WHERE is_cur) - count(*) FILTER (WHERE NOT is_cur))::numeric
                   / count(*) FILTER (WHERE NOT is_cur)) * 100, 1)
  END AS delta_reqs_pct,
  CASE WHEN COALESCE(sum(total_cost) FILTER (WHERE NOT is_cur),0) = 0 THEN NULL
       ELSE ROUND(((COALESCE(sum(total_cost) FILTER (WHERE is_cur),0)
                    - COALESCE(sum(total_cost) FILTER (WHERE NOT is_cur),0))
                   / COALESCE(sum(total_cost) FILTER (WHERE NOT is_cur),0)) * 100, 1)
  END AS delta_cost_pct
  FROM win GROUP BY user_id ORDER BY user_id) t;" 2>&1

echo
echo "=== general: usage_logs per-user by model (window) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  user_id, COALESCE(requested_model,model) AS req_model,
  count(*) AS reqs,
  ROUND(COALESCE(sum(total_cost),0)::numeric,6) AS total_cost,
  count(*) FILTER (WHERE COALESCE(total_cost,0)=0) AS zero_cost_rows
  FROM usage_logs
  WHERE user_id IN (${IDS}) AND created_at >= now() - ${W}
  GROUP BY 1,2 ORDER BY reqs DESC LIMIT 30) t;" 2>&1

# ---------------------------------------------------------------------------
# IMAGE / VIDEO — per-user breakout (usage_logs)
# ---------------------------------------------------------------------------
echo
echo "=== image: per-user totals (window) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  user_id, count(*) AS reqs,
  COALESCE(sum(image_count),0) AS images,
  ROUND(COALESCE(sum(total_cost),0)::numeric,6) AS total_cost,
  count(*) FILTER (WHERE COALESCE(total_cost,0)=0) AS zero_cost_rows
  FROM usage_logs
  WHERE user_id IN (${IDS}) AND ${IMG_U} AND created_at >= now() - ${W}
  GROUP BY user_id ORDER BY user_id) t;" 2>&1

echo
echo "=== video: per-user totals (window) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  user_id, count(*) AS reqs,
  COALESCE(sum(video_duration_seconds),0) AS video_secs,
  ROUND(COALESCE(sum(total_cost),0)::numeric,6) AS total_cost,
  count(*) FILTER (WHERE COALESCE(total_cost,0)=0) AS zero_cost_rows
  FROM usage_logs
  WHERE user_id IN (${IDS}) AND ${VID_U} AND created_at >= now() - ${W}
  GROUP BY user_id ORDER BY user_id) t;" 2>&1

# ---------------------------------------------------------------------------
# ERRORS — per-user, with image/video surface tag
# ---------------------------------------------------------------------------
echo
echo "=== errors: per-user by status/surface (current window) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  user_id,
  CASE WHEN ${VID_E} THEN 'video' WHEN ${IMG_E} THEN 'image' ELSE 'general' END AS surface,
  status_code, upstream_status_code, error_phase, error_type, error_owner,
  count(*) AS n, max(created_at) AT TIME ZONE 'UTC' AS last_at_utc
  FROM ops_error_logs
  WHERE user_id IN (${IDS}) AND created_at >= now() - ${W}
  GROUP BY 1,2,3,4,5,6,7 ORDER BY n DESC LIMIT 40) t;" 2>&1

echo
echo "=== errors: per-user 环比 (current vs previous window) ==="
$PSQL -c "WITH win AS (
  SELECT user_id, (created_at >= now() - ${W}) AS is_cur
  FROM ops_error_logs
  WHERE user_id IN (${IDS}) AND created_at >= now() - 2*${W}
)
SELECT row_to_json(t) FROM (SELECT
  user_id,
  count(*) FILTER (WHERE is_cur) AS n,
  count(*) FILTER (WHERE NOT is_cur) AS prev_n,
  CASE WHEN count(*) FILTER (WHERE NOT is_cur) = 0 THEN NULL
       ELSE ROUND(((count(*) FILTER (WHERE is_cur) - count(*) FILTER (WHERE NOT is_cur))::numeric
                   / count(*) FILTER (WHERE NOT is_cur)) * 100, 1)
  END AS delta_n_pct
  FROM win GROUP BY user_id ORDER BY user_id) t;" 2>&1

echo
echo "=== errors: user-facing failures by model/account (window) ==="
# Only rows the client actually received as a failure (status_code <> 200, i.e.
# NOT recovered-200). Each row carries the terminal-experience basics in one
# place: model, serving account (+platform/status), group/key used, and a root
# cause sample — so the report never has to guess or cross-join by hand.
# account_id IS NULL here means the request never reached a pool (routing phase).
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  e.user_id,
  CASE WHEN ${VID_E} THEN 'video' WHEN ${IMG_E} THEN 'image' ELSE 'general' END AS surface,
  COALESCE(e.model,'') AS model,
  e.status_code, e.upstream_status_code,
  e.error_phase, e.error_type, e.error_owner,
  e.account_id,
  COALESCE(a.name,'')                     AS account_name,
  COALESCE(a.platform, e.platform, '')    AS account_platform,
  COALESCE(e.account_status, a.status,'')  AS account_status,
  COALESCE(g.name,'')                                     AS group_name,
  COALESCE(ak.name, e.deleted_key_name,'')                AS api_key_name,
  COALESCE(e.api_key_prefix,'')                           AS api_key_prefix,
  (COALESCE(ak.routing_mode,'direct') = 'universal')       AS is_universal_key,
  count(*) AS n,
  COALESCE(mode() WITHIN GROUP (ORDER BY e.provider_error_code), '') AS provider_error_code,
  COALESCE(mode() WITHIN GROUP (ORDER BY e.network_error_type), '')  AS network_error_type,
  left((array_agg(COALESCE(e.upstream_error_message, e.error_message, '')
        ORDER BY e.created_at DESC))[1], 200) AS root_cause_sample,
  max(e.created_at) AT TIME ZONE 'UTC' AS last_at_utc
  FROM ops_error_logs e
  LEFT JOIN accounts a ON a.id = e.account_id
  LEFT JOIN groups g   ON g.id = e.group_id AND g.deleted_at IS NULL
  LEFT JOIN api_keys ak ON ak.id = e.api_key_id AND ak.deleted_at IS NULL
  WHERE e.user_id IN (${IDS}) AND e.created_at >= now() - ${W}
    AND e.status_code IS DISTINCT FROM 200
  GROUP BY 1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16
  ORDER BY count(*) DESC, e.user_id
  LIMIT 40) t;" 2>&1

echo
echo "=== baseline: per-user request/cost trailing 24h (excl. current window) ==="
$PSQL -c "WITH buckets AS (
  SELECT
    user_id,
    to_timestamp(floor(extract(epoch from created_at) / (${WINDOW_MINUTES}*60)) * (${WINDOW_MINUTES}*60)) AS bucket_start,
    count(*) AS reqs,
    sum(total_cost) AS cost
  FROM usage_logs
  WHERE user_id IN (${IDS}) AND created_at >= now() - interval '24 hours' AND created_at < now() - ${W}
  GROUP BY 1,2
)
SELECT row_to_json(t) FROM (SELECT
  user_id,
  ROUND(avg(reqs)::numeric,1)  AS avg_reqs_per_window_24h,
  max(reqs)                    AS max_reqs_per_window_24h,
  ROUND(avg(cost)::numeric,4)  AS avg_cost_per_window_24h,
  ROUND(max(cost)::numeric,4)  AS max_cost_per_window_24h,
  count(*)                     AS buckets_seen
  FROM buckets GROUP BY 1 ORDER BY 1) t;" 2>&1

echo
echo "=== baseline: per-user error fingerprint trailing 24h (excl. current window) ==="
$PSQL -c "WITH buckets AS (
  SELECT
    user_id, status_code, upstream_status_code, error_phase, error_type, error_owner,
    to_timestamp(floor(extract(epoch from created_at) / (${WINDOW_MINUTES}*60)) * (${WINDOW_MINUTES}*60)) AS bucket_start,
    count(*) AS n
  FROM ops_error_logs
  WHERE user_id IN (${IDS}) AND created_at >= now() - interval '24 hours' AND created_at < now() - ${W}
  GROUP BY 1,2,3,4,5,6,7
)
SELECT row_to_json(t) FROM (SELECT
  user_id, status_code, upstream_status_code, error_phase, error_type, error_owner,
  ROUND(avg(n)::numeric,2) AS avg_per_window_24h,
  max(n)                   AS max_per_window_24h,
  count(*)                 AS buckets_seen
  FROM buckets
  GROUP BY 1,2,3,4,5,6
  ORDER BY avg_per_window_24h DESC LIMIT 40) t;" 2>&1

echo
echo "=== errors: last 12 samples (desensitized) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  created_at AT TIME ZONE 'UTC' AS ts_utc, user_id,
  CASE WHEN ${VID_E} THEN 'video' WHEN ${IMG_E} THEN 'image' ELSE 'general' END AS surface,
  COALESCE(platform,'?') AS platform, model, request_path,
  error_phase, error_type, status_code, upstream_status_code, account_id,
  left(COALESCE(upstream_error_message, error_message,''),180) AS msg
  FROM ops_error_logs
  WHERE user_id IN (${IDS}) AND created_at >= now() - ${W}
  ORDER BY created_at DESC LIMIT 12) t;" 2>&1

# ---------------------------------------------------------------------------
# LAST-SEEN — keep empty windows informative
# ---------------------------------------------------------------------------
echo
echo "=== last-seen per user (success + error, any time) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  u.id AS user_id,
  (SELECT max(created_at) AT TIME ZONE 'UTC' FROM usage_logs    WHERE user_id=u.id) AS last_success_utc,
  (SELECT max(created_at) AT TIME ZONE 'UTC' FROM ops_error_logs WHERE user_id=u.id) AS last_error_utc
  FROM (SELECT unnest(ARRAY[${IDS}]) AS id) u ORDER BY u.id) t;" 2>&1
