#!/usr/bin/env bash
# probe-kiro-response-compat.sh - read-only history for Kiro-served model traffic.
# It compares routing, stream mode, users, and client User-Agent versions without
# reading request or response bodies.
#
# Env:
#   DAYS      lookback days, default 15
#   MODEL     effective requested model, default claude-opus-4-8; * means all
#   UA_LIMIT  top User-Agent rows per UTC day, default 20
#   UA_FILTER optional User-Agent substring
set -u

PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'
DAYS="${DAYS:-15}"
MODEL="${MODEL:-claude-opus-4-8}"
UA_LIMIT="${UA_LIMIT:-20}"
UA_FILTER="${UA_FILTER:-}"

for name in DAYS UA_LIMIT; do
  value=$(eval "printf '%s' \"\${$name}\"")
  case "$value" in
    ''|*[!0-9]*) echo "[probe-kiro-response-compat] ERROR: $name not positive int: '$value'" >&2; exit 2 ;;
  esac
  if [ "$value" -lt 1 ]; then
    echo "[probe-kiro-response-compat] ERROR: $name must be >= 1" >&2
    exit 2
  fi
done

MODEL_SQL="${MODEL//\'/\'\'}"
if [ "$MODEL" = "*" ]; then
  MODEL_SQL=""
fi
UA_FILTER_SQL="${UA_FILTER//\'/\'\'}"

echo "=== meta ==="
printf 'days=%s\nmodel=%s\nua_limit=%s\nua_filter=%s\n' \
  "$DAYS" "$MODEL" "$UA_LIMIT" "${UA_FILTER:-<none>}"

echo
echo "=== by_day_account ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT
    to_char(date_trunc('day', ul.created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day_utc,
    ul.account_id,
    COALESCE(a.name, '<deleted>') AS account,
    COUNT(*) AS requests,
    COUNT(*) FILTER (WHERE ul.stream) AS stream_requests,
    COUNT(DISTINCT ul.user_id) AS users
  FROM usage_logs ul
  LEFT JOIN accounts a ON a.id = ul.account_id -- ops-allow-soft-deleted: retain historical traffic after account deletion.
  WHERE ul.created_at >= now() - interval '${DAYS} days'
    AND ('${MODEL_SQL}' = '' OR COALESCE(NULLIF(TRIM(ul.requested_model), ''), ul.model) = '${MODEL_SQL}')
    AND (a.platform = 'kiro' OR a.name LIKE 'kiro-%')
    AND ('${UA_FILTER_SQL}' = '' OR ul.user_agent ILIKE '%' || '${UA_FILTER_SQL}' || '%')
  GROUP BY 1, 2, 3
  ORDER BY 1, 2
) t;
" 2>&1

echo
echo "=== by_day_user_agent ==="
$PSQL -c "
WITH grouped AS (
  SELECT
    to_char(date_trunc('day', ul.created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day_utc,
    ul.user_id,
    left(COALESCE(NULLIF(TRIM(ul.user_agent), ''), '<empty>'), 180) AS user_agent,
    COUNT(*) AS requests,
    COUNT(*) FILTER (WHERE ul.stream) AS stream_requests
  FROM usage_logs ul
  LEFT JOIN accounts a ON a.id = ul.account_id -- ops-allow-soft-deleted: retain historical traffic after account deletion.
  WHERE ul.created_at >= now() - interval '${DAYS} days'
    AND ('${MODEL_SQL}' = '' OR COALESCE(NULLIF(TRIM(ul.requested_model), ''), ul.model) = '${MODEL_SQL}')
    AND (a.platform = 'kiro' OR a.name LIKE 'kiro-%')
    AND ('${UA_FILTER_SQL}' = '' OR ul.user_agent ILIKE '%' || '${UA_FILTER_SQL}' || '%')
  GROUP BY 1, 2, 3
) , ranked AS (
  SELECT grouped.*,
         row_number() OVER (PARTITION BY day_utc ORDER BY requests DESC, user_id, user_agent) AS rank
  FROM grouped
)
SELECT row_to_json(t) FROM (
  SELECT day_utc, user_id, user_agent, requests, stream_requests
  FROM ranked
  WHERE rank <= ${UA_LIMIT}
  ORDER BY day_utc, rank
) t;
" 2>&1

echo
echo "=== matching_error_events ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT
    to_char(date_trunc('day', l.created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day_utc,
    l.user_id,
    left(COALESCE(NULLIF(TRIM(l.user_agent), ''), '<empty>'), 180) AS user_agent,
    l.status_code,
    COALESCE(NULLIF(TRIM(l.requested_model), ''), l.model) AS model,
    l.account_id,
    COUNT(*) AS events,
    to_char(MIN(l.created_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS first_at_utc,
    to_char(MAX(l.created_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS last_at_utc
  FROM ops_error_logs l
  LEFT JOIN accounts a ON a.id = l.account_id -- ops-allow-soft-deleted: retain historical errors after account deletion.
  WHERE l.created_at >= now() - interval '${DAYS} days'
    AND ('${MODEL_SQL}' = '' OR COALESCE(NULLIF(TRIM(l.requested_model), ''), l.model) = '${MODEL_SQL}')
    AND ('${UA_FILTER_SQL}' = '' OR l.user_agent ILIKE '%' || '${UA_FILTER_SQL}' || '%')
    AND (a.platform = 'kiro' OR a.name LIKE 'kiro-%' OR l.platform = 'kiro')
  GROUP BY 1, 2, 3, 4, 5, 6
  ORDER BY day_utc, events DESC
  LIMIT 100
) t;
" 2>&1
