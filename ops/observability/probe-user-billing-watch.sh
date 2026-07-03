#!/usr/bin/env bash
# probe-user-billing-watch.sh — read-only per-user 盯盘 for a set of user_ids:
# requests, errors, metering/billing, plus an image/video breakout — all in one
# SSM round-trip. Runs INSIDE the TokenKey host (prod or edge) via run-probe.sh.
# Output is row_to_json so parsing is field-named, not column-index.
#
#   bash ops/observability/run-probe.sh --target prod \
#     --script ops/observability/probe-user-billing-watch.sh \
#     --env USER_IDS=1,16 [--env WINDOW_MINUTES=30]
#
# USER_IDS        comma-separated integer user ids (default 1,16)
# WINDOW_MINUTES  look-back window in minutes (default 30; matches report cadence)
#
# image/video discriminators reuse probe-image-video-billing.sh's proven predicates.
set -u

USER_IDS="${USER_IDS:-1,16}"
WINDOW_MINUTES="${WINDOW_MINUTES:-30}"
# Validate: digits and commas only (SQL IN-list interpolation guard).
case "$USER_IDS" in ''|*[!0-9,]*) echo "bad USER_IDS (want comma-separated ints)" >&2; exit 2;; esac
case "$WINDOW_MINUTES" in ''|*[!0-9]*) echo "bad WINDOW_MINUTES (want integer)" >&2; exit 2;; esac

PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'
W="interval '${WINDOW_MINUTES} minutes'"
IDS="$USER_IDS"

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
  '${USER_IDS}'::text                AS user_ids,
  ${WINDOW_MINUTES}::int             AS window_minutes) t;" 2>&1

echo
echo "=== users ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  id, email, username, status FROM users WHERE id IN (${IDS}) AND deleted_at IS NULL ORDER BY id) t;" 2>&1

# ---------------------------------------------------------------------------
# GENERAL — per-user requests + billing (usage_logs success path)
# ---------------------------------------------------------------------------
echo
echo "=== general: usage_logs per-user totals (window) ==="
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
echo "=== errors: per-user by status/surface (window) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  user_id,
  CASE WHEN ${VID_E} THEN 'video' WHEN ${IMG_E} THEN 'image' ELSE 'general' END AS surface,
  status_code, upstream_status_code, error_phase, error_type, error_owner,
  count(*) AS n, max(created_at) AT TIME ZONE 'UTC' AS last_at_utc
  FROM ops_error_logs
  WHERE user_id IN (${IDS}) AND created_at >= now() - ${W}
  GROUP BY 1,2,3,4,5,6,7 ORDER BY n DESC LIMIT 40) t;" 2>&1

echo
echo "=== errors: key/group breakdown for error types over 10 (window) ==="
$PSQL -c "WITH base AS (
  SELECT
    user_id,
    CASE WHEN ${VID_E} THEN 'video' WHEN ${IMG_E} THEN 'image' ELSE 'general' END AS surface,
    status_code,
    upstream_status_code,
    error_phase,
    error_type,
    error_owner,
    model,
    api_key_id,
    api_key_prefix,
    deleted_key_name,
    group_id,
    created_at
  FROM ops_error_logs
  WHERE user_id IN (${IDS}) AND created_at >= now() - ${W}
), frequent AS (
  SELECT
    user_id,
    surface,
    status_code,
    upstream_status_code,
    error_phase,
    error_type,
    error_owner,
    count(*) AS error_type_n
  FROM base
  GROUP BY 1,2,3,4,5,6,7
  HAVING count(*) > 10
)
SELECT row_to_json(t) FROM (SELECT
  f.user_id,
  f.surface,
  f.status_code,
  f.upstream_status_code,
  f.error_phase,
  f.error_type,
  f.error_owner,
  COALESCE(b.model, '') AS error_model,
  f.error_type_n,
  b.api_key_id,
  COALESCE(ak.name, b.deleted_key_name, '') AS api_key_name,
  COALESCE(b.api_key_prefix, '') AS api_key_prefix,
  b.group_id,
  COALESCE(g.name, '') AS group_name,
  (COALESCE(ak.routing_mode, 'direct') = 'universal') AS is_universal_key,
  count(*) AS key_group_n,
  max(b.created_at) AT TIME ZONE 'UTC' AS last_at_utc
  FROM frequent f
  JOIN base b
    ON b.user_id = f.user_id
   AND b.surface = f.surface
   AND b.status_code IS NOT DISTINCT FROM f.status_code
   AND b.upstream_status_code IS NOT DISTINCT FROM f.upstream_status_code
   AND b.error_phase IS NOT DISTINCT FROM f.error_phase
   AND b.error_type IS NOT DISTINCT FROM f.error_type
   AND b.error_owner IS NOT DISTINCT FROM f.error_owner
  LEFT JOIN api_keys ak ON ak.id = b.api_key_id AND ak.deleted_at IS NULL
  LEFT JOIN groups g ON g.id = b.group_id AND g.deleted_at IS NULL
  GROUP BY
    f.user_id,
    f.surface,
    f.status_code,
    f.upstream_status_code,
    f.error_phase,
    f.error_type,
    f.error_owner,
    COALESCE(b.model, ''),
    f.error_type_n,
    b.api_key_id,
    COALESCE(ak.name, b.deleted_key_name, ''),
    COALESCE(b.api_key_prefix, ''),
    b.group_id,
    COALESCE(g.name, ''),
    (COALESCE(ak.routing_mode, 'direct') = 'universal')
  ORDER BY f.error_type_n DESC, key_group_n DESC, f.user_id, b.api_key_id NULLS LAST, b.group_id NULLS LAST
  LIMIT 80) t;" 2>&1

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
