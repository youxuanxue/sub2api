#!/usr/bin/env bash
# probe-image-video-billing.sh — read-only prod盯盘 for image/video generation
# traffic: volume, errors, and metering/billing. Runs INSIDE the TokenKey host
# (prod or edge) via run-probe.sh. Output is row_to_json so parsing is
# field-named, not column-index.
#
#   bash ops/observability/run-probe.sh --target prod \
#     --script ops/observability/probe-image-video-billing.sh \
#     [--env WINDOW_MIN=30] [--env CTX_HOURS=6]
#
# WINDOW_MIN — detailed recent window in minutes (default 30; overlaps the
#              5-min report cadence so nothing slips between reports).
# CTX_HOURS  — broader context window in hours (default 6).
#
# Discriminators (image vs video) — derived from usage_logs / ops_error_logs:
#   image: billing_mode='image' OR image_count>0 OR inbound_endpoint ILIKE '%image%'
#          (errors: request_path/inbound_endpoint ILIKE '%/images%')
#   video: video_duration_seconds IS NOT NULL OR inbound_endpoint ILIKE '%video%'
#          (errors: request_path/inbound_endpoint ILIKE '%/video%'  — also matches /videos)
set -u

WINDOW_MIN="${WINDOW_MIN:-30}"
CTX_HOURS="${CTX_HOURS:-6}"
# Integer-validate before interpolating into SQL interval literals (matches
# probe-429-classify.sh's WINDOW_HOURS guard; closes the injection vector).
case "$WINDOW_MIN" in ''|*[!0-9]*) echo "bad WINDOW_MIN (want integer minutes)" >&2; exit 2;; esac
case "$CTX_HOURS"  in ''|*[!0-9]*) echo "bad CTX_HOURS (want integer hours)"   >&2; exit 2;; esac
PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'

W="interval '${WINDOW_MIN} minutes'"
C="interval '${CTX_HOURS} hours'"

# usage_logs predicates — keep image/video symmetric: billing_mode is the
# precise tag, the others are belt-and-suspenders. video submit rows can have
# NULL video_duration_seconds, so billing_mode='video' is load-bearing here
# (without it those zero-cost submit rows — exactly what we watch for — slip).
IMG_U="(billing_mode = 'image' OR COALESCE(image_count,0) > 0 OR inbound_endpoint ILIKE '%image%')"
VID_U="(billing_mode = 'video' OR video_duration_seconds IS NOT NULL OR inbound_endpoint ILIKE '%video%')"
# ops_error_logs predicates
IMG_E="(request_path ILIKE '%/images%' OR inbound_endpoint ILIKE '%image%')"
VID_E="(request_path ILIKE '%/video%' OR inbound_endpoint ILIKE '%video%')"

echo "=== meta ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  now() AT TIME ZONE 'UTC'                         AS now_utc,
  now() AT TIME ZONE 'Asia/Shanghai'              AS now_cst,
  ${WINDOW_MIN}::int                               AS window_min,
  ${CTX_HOURS}::int                                AS ctx_hours) t;" 2>&1

# ---------------------------------------------------------------------------
# IMAGE — billing/volume from usage_logs (successful, billed requests)
# ---------------------------------------------------------------------------
echo
echo "=== image: usage_logs totals (window vs ctx) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  count(*) FILTER (WHERE created_at >= now() - ${W})                       AS req_window,
  count(*) FILTER (WHERE created_at >= now() - ${C})                       AS req_ctx,
  COALESCE(sum(image_count) FILTER (WHERE created_at >= now() - ${C}),0)   AS images_ctx,
  ROUND(COALESCE(sum(total_cost)  FILTER (WHERE created_at >= now() - ${C}),0)::numeric,6) AS total_cost_ctx,
  ROUND(COALESCE(sum(actual_cost) FILTER (WHERE created_at >= now() - ${C}),0)::numeric,6) AS actual_cost_ctx,
  count(*) FILTER (WHERE created_at >= now() - ${C} AND COALESCE(total_cost,0)=0) AS zero_cost_ctx
  FROM usage_logs WHERE ${IMG_U} AND created_at >= now() - ${C}) t;" 2>&1

echo
echo "=== image: by model (ctx) — vol, images, cost, zero-cost rows ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  COALESCE(requested_model,model)                                AS req_model,
  upstream_model,
  count(*)                                                       AS reqs,
  COALESCE(sum(image_count),0)                                   AS images,
  ROUND(COALESCE(sum(total_cost),0)::numeric,6)                  AS total_cost,
  count(*) FILTER (WHERE COALESCE(total_cost,0)=0)               AS zero_cost_rows
  FROM usage_logs WHERE ${IMG_U} AND created_at >= now() - ${C}
  GROUP BY 1,2 ORDER BY reqs DESC LIMIT 30) t;" 2>&1

echo
echo "=== image: last 5 billed rows (sample) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  created_at AT TIME ZONE 'UTC' AS ts_utc,
  COALESCE(requested_model,model) AS req_model, upstream_model,
  image_count, image_size, billing_mode,
  ROUND(COALESCE(total_cost,0)::numeric,6) AS total_cost
  FROM usage_logs WHERE ${IMG_U} ORDER BY created_at DESC LIMIT 5) t;" 2>&1

# ---------------------------------------------------------------------------
# VIDEO — billing/volume from usage_logs
# ---------------------------------------------------------------------------
echo
echo "=== video: usage_logs totals (window vs ctx) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  count(*) FILTER (WHERE created_at >= now() - ${W})                            AS req_window,
  count(*) FILTER (WHERE created_at >= now() - ${C})                            AS req_ctx,
  COALESCE(sum(video_duration_seconds) FILTER (WHERE created_at >= now() - ${C}),0) AS video_secs_ctx,
  ROUND(COALESCE(sum(total_cost)  FILTER (WHERE created_at >= now() - ${C}),0)::numeric,6) AS total_cost_ctx,
  ROUND(COALESCE(sum(actual_cost) FILTER (WHERE created_at >= now() - ${C}),0)::numeric,6) AS actual_cost_ctx,
  count(*) FILTER (WHERE created_at >= now() - ${C} AND COALESCE(total_cost,0)=0) AS zero_cost_ctx
  FROM usage_logs WHERE ${VID_U} AND created_at >= now() - ${C}) t;" 2>&1

echo
echo "=== video: by model (ctx) — vol, secs, cost, zero-cost rows ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  COALESCE(requested_model,model)                                AS req_model,
  upstream_model,
  count(*)                                                       AS reqs,
  COALESCE(sum(video_duration_seconds),0)                        AS video_secs,
  ROUND(COALESCE(sum(total_cost),0)::numeric,6)                  AS total_cost,
  count(*) FILTER (WHERE COALESCE(total_cost,0)=0)               AS zero_cost_rows
  FROM usage_logs WHERE ${VID_U} AND created_at >= now() - ${C}
  GROUP BY 1,2 ORDER BY reqs DESC LIMIT 30) t;" 2>&1

echo
echo "=== video: last 5 billed rows (sample) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  created_at AT TIME ZONE 'UTC' AS ts_utc,
  COALESCE(requested_model,model) AS req_model, upstream_model,
  video_duration_seconds, billing_mode,
  ROUND(COALESCE(total_cost,0)::numeric,6) AS total_cost
  FROM usage_logs WHERE ${VID_U} ORDER BY created_at DESC LIMIT 5) t;" 2>&1

# ---------------------------------------------------------------------------
# ERRORS — ops_error_logs filtered to image/video surfaces
# ---------------------------------------------------------------------------
echo
echo "=== errors: image+video counts (window vs ctx) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  count(*) FILTER (WHERE ${IMG_E} AND created_at >= now() - ${W}) AS img_err_window,
  count(*) FILTER (WHERE ${IMG_E} AND created_at >= now() - ${C}) AS img_err_ctx,
  count(*) FILTER (WHERE ${VID_E} AND created_at >= now() - ${W}) AS vid_err_window,
  count(*) FILTER (WHERE ${VID_E} AND created_at >= now() - ${C}) AS vid_err_ctx
  FROM ops_error_logs WHERE (${IMG_E} OR ${VID_E}) AND created_at >= now() - ${C}) t;" 2>&1

echo
echo "=== errors: breakdown by surface/status/type (ctx) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  CASE WHEN ${VID_E} THEN 'video' WHEN ${IMG_E} THEN 'image' ELSE 'other' END AS surface,
  COALESCE(platform,'?')        AS platform,
  COALESCE(model,'?')           AS model,
  error_phase, error_type,
  status_code, upstream_status_code,
  is_business_limited,
  count(*)                      AS n
  FROM ops_error_logs WHERE (${IMG_E} OR ${VID_E}) AND created_at >= now() - ${C}
  GROUP BY 1,2,3,4,5,6,7,8 ORDER BY n DESC LIMIT 40) t;" 2>&1

echo
echo "=== errors: last 8 (sample, desensitized) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  created_at AT TIME ZONE 'UTC' AS ts_utc,
  CASE WHEN ${VID_E} THEN 'video' WHEN ${IMG_E} THEN 'image' ELSE 'other' END AS surface,
  platform, model, request_path,
  error_phase, error_type, status_code, upstream_status_code,
  left(COALESCE(upstream_error_message, error_message,''),200) AS msg
  FROM ops_error_logs WHERE (${IMG_E} OR ${VID_E})
  ORDER BY created_at DESC LIMIT 8) t;" 2>&1

# ---------------------------------------------------------------------------
# LAST-SEEN — so empty windows are still informative
# ---------------------------------------------------------------------------
echo
echo "=== last-seen (even if windows empty) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  (SELECT max(created_at) AT TIME ZONE 'UTC' FROM usage_logs WHERE ${IMG_U})        AS last_image_ok,
  (SELECT max(created_at) AT TIME ZONE 'UTC' FROM usage_logs WHERE ${VID_U})        AS last_video_ok,
  (SELECT max(created_at) AT TIME ZONE 'UTC' FROM ops_error_logs WHERE ${IMG_E})    AS last_image_err,
  (SELECT max(created_at) AT TIME ZONE 'UTC' FROM ops_error_logs WHERE ${VID_E})    AS last_video_err) t;" 2>&1
