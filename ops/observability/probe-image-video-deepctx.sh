#!/usr/bin/env bash
# probe-image-video-deepctx.sh — read-only one-shot deep context for the
# image/video failure baseline: WHY image gen returns errors (openai pool
# state, offending account_ids, missing-scope vs empty-pool), and traffic
# provenance (test sweep vs organic users). Runs INSIDE the host via run-probe.
set -u
PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'

echo "=== openai account pool (not soft-deleted) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  status,
  schedulable,
  (temp_unschedulable_until IS NOT NULL AND temp_unschedulable_until > now()) AS in_cooldown,
  channel_type,
  count(*) AS n
  FROM accounts
  WHERE platform='openai' AND deleted_at IS NULL
  GROUP BY 1,2,3,4 ORDER BY n DESC) t;" 2>&1

echo
echo "=== openai accounts schedulable RIGHT NOW (detail) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  id, name, status, schedulable, priority, channel_type,
  temp_unschedulable_until AT TIME ZONE 'UTC' AS cooldown_until_utc,
  left(COALESCE(temp_unschedulable_reason,''),120) AS reason
  FROM accounts
  WHERE platform='openai' AND deleted_at IS NULL
  ORDER BY schedulable DESC, priority ASC LIMIT 20) t;" 2>&1

echo
echo "=== image errors by account_id (24h) — who is failing ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  account_id, platform, model,
  status_code, upstream_status_code, error_type, error_phase,
  count(*) AS n,
  max(created_at AT TIME ZONE 'UTC') AS last_utc
  FROM ops_error_logs
  WHERE (request_path ILIKE '%/images%' OR inbound_endpoint ILIKE '%image%')
    AND created_at >= now() - interval '24 hours'
  GROUP BY 1,2,3,4,5,6,7 ORDER BY n DESC LIMIT 30) t;" 2>&1

echo
echo "=== image traffic provenance: distinct users/api_keys (7d, ok+err) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  'usage_ok' AS src, count(*) AS rows,
  count(DISTINCT user_id) AS users, count(DISTINCT api_key_id) AS keys,
  min(created_at AT TIME ZONE 'UTC') AS first_utc, max(created_at AT TIME ZONE 'UTC') AS last_utc
  FROM usage_logs
  WHERE (billing_mode='image' OR COALESCE(image_count,0)>0 OR inbound_endpoint ILIKE '%image%')
    AND created_at >= now() - interval '7 days'
  UNION ALL
  SELECT 'err', count(*),
  count(DISTINCT user_id), count(DISTINCT api_key_id),
  min(created_at AT TIME ZONE 'UTC'), max(created_at AT TIME ZONE 'UTC')
  FROM ops_error_logs
  WHERE (request_path ILIKE '%/images%' OR inbound_endpoint ILIKE '%image%')
    AND created_at >= now() - interval '7 days') t;" 2>&1

echo
echo "=== video traffic provenance: distinct users/api_keys (7d, ok+err) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  'usage_ok' AS src, count(*) AS rows,
  count(DISTINCT user_id) AS users, count(DISTINCT api_key_id) AS keys,
  min(created_at AT TIME ZONE 'UTC') AS first_utc, max(created_at AT TIME ZONE 'UTC') AS last_utc
  FROM usage_logs
  WHERE (video_duration_seconds IS NOT NULL OR billing_mode='video' OR inbound_endpoint ILIKE '%video%')
    AND created_at >= now() - interval '7 days'
  UNION ALL
  SELECT 'err', count(*),
  count(DISTINCT user_id), count(DISTINCT api_key_id),
  min(created_at AT TIME ZONE 'UTC'), max(created_at AT TIME ZONE 'UTC')
  FROM ops_error_logs
  WHERE (request_path ILIKE '%/video%' OR inbound_endpoint ILIKE '%video%')
    AND created_at >= now() - interval '7 days') t;" 2>&1

echo
echo "=== video usage rows detail (7d) — submit vs billed (cost/secs) ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  COALESCE(requested_model,model) AS req_model,
  billing_mode,
  count(*) AS rows,
  count(*) FILTER (WHERE COALESCE(total_cost,0)=0) AS zero_cost,
  count(*) FILTER (WHERE video_duration_seconds IS NOT NULL) AS has_secs,
  ROUND(COALESCE(sum(total_cost),0)::numeric,6) AS total_cost
  FROM usage_logs
  WHERE (video_duration_seconds IS NOT NULL OR billing_mode='video' OR inbound_endpoint ILIKE '%video%')
    AND created_at >= now() - interval '7 days'
  GROUP BY 1,2 ORDER BY rows DESC LIMIT 30) t;" 2>&1
