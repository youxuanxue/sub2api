#!/bin/bash
# probe-fleet-traffic-window.sh — read-only fleet-wide (all users) request/error snapshot
# for a short time window. Runs on TokenKey host via run-probe.sh.
#
# Env:
#   WINDOW_MINUTES default 5
set -u
M="${WINDOW_MINUTES:-5}"
PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'

echo "=== probe_fleet_traffic window_minutes=$M db_now=$(date -u +%Y-%m-%dT%H:%M:%SZ) ==="

echo "=== usage_logs summary (all users, success path) ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT count(*) reqs, round(sum(total_cost)::numeric,2) cost,
         min(created_at) first_at, max(created_at) last_at
  FROM usage_logs WHERE created_at >= now() - interval '${M} minutes'
) t;"

echo "=== usage_logs by_minute ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT to_char(date_trunc('minute', created_at AT TIME ZONE 'UTC'),'HH24:MI') min_utc,
         count(*) reqs, round(sum(total_cost)::numeric,2) cost
  FROM usage_logs WHERE created_at >= now() - interval '${M} minutes'
  GROUP BY 1 ORDER BY 1
) t;"

echo "=== usage_logs by_user (top8) ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT user_id, count(*) n, round(sum(total_cost)::numeric,2) cost
  FROM usage_logs WHERE created_at >= now() - interval '${M} minutes'
  GROUP BY 1 ORDER BY n DESC LIMIT 8
) t;"

echo "=== usage_logs by_model (top8) ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT model, count(*) n FROM usage_logs
  WHERE created_at >= now() - interval '${M} minutes'
  GROUP BY 1 ORDER BY n DESC LIMIT 8
) t;"

echo "=== usage_logs by_account (top10, who served) ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT ul.account_id, a.name acct, count(*) n
  FROM usage_logs ul LEFT JOIN accounts a ON a.id=ul.account_id
  WHERE ul.created_at >= now() - interval '${M} minutes'
  GROUP BY 1,2 ORDER BY n DESC LIMIT 10
) t;"

echo "=== ops_error_logs by final status (all users) ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT status_code, count(*) n, min(created_at) first_at, max(created_at) last_at
  FROM ops_error_logs WHERE created_at >= now() - interval '${M} minutes'
  GROUP BY 1 ORDER BY n DESC
) t;"

echo "=== ops_error_logs 401 breakdown (key disabled watch) ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT api_key_id, user_id, left(error_message,60) msg, count(*) n
  FROM ops_error_logs WHERE status_code=401 AND created_at >= now() - interval '${M} minutes'
  GROUP BY 1,2,3 ORDER BY n DESC LIMIT 6
) t;"

echo "=== cc-* stub propagation (anthropic apikey, error events) ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT oel.account_id, a.name acct, count(*) err,
         count(*) FILTER (WHERE oel.upstream_status_code=429) up429,
         count(*) FILTER (WHERE oel.status_code=502) f502,
         count(*) FILTER (WHERE oel.status_code=200) recovered
  FROM ops_error_logs oel JOIN accounts a ON a.id=oel.account_id
  WHERE oel.created_at >= now() - interval '${M} minutes'
    AND a.platform='anthropic' AND a.type='apikey'
  GROUP BY 1,2 ORDER BY err DESC LIMIT 10
) t;"
