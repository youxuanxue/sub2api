#!/bin/bash
# probe-openai-fingerprint-quota.sh — read-only OpenAI OAuth fingerprint + quota snapshot.
#
# Emits row_to_json only. No credentials, request bodies, or raw extra dumps.
# Env:
#   ERR_HOURS   ops_error_logs lookback (default 72)
#   USAGE_DAYS  usage_logs lookback (default 7)

set -u

PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'
ERR_HOURS="${ERR_HOURS:-72}"
USAGE_DAYS="${USAGE_DAYS:-7}"

echo "=== host ==="
echo "hostname=$(hostname)"
echo "now_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo
echo "=== openai_oauth_accounts ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT
    a.id,
    a.name,
    a.status,
    a.schedulable,
    COALESCE(NULLIF(trim(a.extra->>'codex_fingerprint_mode'), ''), '<unset>') AS fingerprint_raw,
    CASE
      WHEN COALESCE(NULLIF(trim(a.extra->>'codex_fingerprint_mode'), ''), '') = '' THEN 'backfill_device'
      WHEN lower(trim(a.extra->>'codex_fingerprint_mode')) IN ('off','device','session','full')
        THEN 'keep_' || lower(trim(a.extra->>'codex_fingerprint_mode'))
      ELSE 'backfill_device'
    END AS merge_effect,
    NULLIF(a.extra->>'codex_5h_used_percent', '') AS used_5h,
    NULLIF(a.extra->>'codex_7d_used_percent', '') AS used_7d,
    NULLIF(a.extra->>'codex_usage_updated_at', '') AS usage_updated_at,
    a.rate_limited_at,
    a.rate_limit_reset_at,
    a.temp_unschedulable_until,
    left(COALESCE(a.temp_unschedulable_reason, ''), 160) AS unsched_reason,
    left(COALESCE(a.error_message, ''), 160) AS error_message
  FROM accounts a
  WHERE a.platform = 'openai'
    AND a.type = 'oauth'
    AND a.deleted_at IS NULL
  ORDER BY a.id
) t;
" 2>&1

echo
echo "=== usage_last_${USAGE_DAYS}d ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT
    a.id AS account_id,
    count(*)::bigint AS requests,
    count(*) FILTER (WHERE COALESCE(ul.total_cost, 0) > 0)::bigint AS billed,
    count(DISTINCT ul.user_id)::bigint AS distinct_users,
    count(DISTINCT ul.api_key_id)::bigint AS distinct_api_keys
  FROM accounts a
  LEFT JOIN usage_logs ul
    ON ul.account_id = a.id
   AND ul.created_at >= NOW() - (INTERVAL '1 day' * ${USAGE_DAYS})
  WHERE a.platform = 'openai'
    AND a.type = 'oauth'
    AND a.deleted_at IS NULL
  GROUP BY a.id
  ORDER BY a.id
) t;
" 2>&1

echo
echo "=== quota_errors_last_${ERR_HOURS}h ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT
    e.account_id,
    count(*)::bigint AS errors,
    count(*) FILTER (
      WHERE e.error_message ILIKE '%quota%'
         OR e.error_message ILIKE '%usage limit%'
         OR e.error_message ILIKE '%rate limit%'
         OR e.error_message ILIKE '%too many%'
         OR e.error_message ILIKE '%exceeded%'
         OR e.status_code = 429
         OR e.upstream_status_code = 429
    )::bigint AS quota_or_429,
    left(min(e.error_message), 180) AS sample_message
  FROM ops_error_logs e
  JOIN accounts a ON a.id = e.account_id
  WHERE a.platform = 'openai'
    AND a.type = 'oauth'
    AND a.deleted_at IS NULL
    AND e.created_at >= NOW() - (INTERVAL '1 hour' * ${ERR_HOURS})
  GROUP BY e.account_id
  ORDER BY e.account_id
) t;
" 2>&1
