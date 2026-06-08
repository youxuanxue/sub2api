#!/usr/bin/env bash
# probe-429-classify.sh — read-only classification of final-429 + 5xx rows in
# ops_error_logs over a window, to separate config-cap (group rpm / business
# limited) from empty-pool / no-available-accounts (#575) and real upstream.
# Shipped via run-probe.sh. row_to_json output only; parse by field name.
set -u
WINDOW_HOURS="${WINDOW_HOURS:-3}"
case "$WINDOW_HOURS" in ''|*[!0-9]*) echo "bad WINDOW_HOURS" >&2; exit 2;; esac
PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'

echo "=== 429_by_type ==="
$PSQL -c "
WITH b AS (SELECT now()-interval '${WINDOW_HOURS} hour' AS s, now() AS u)
SELECT row_to_json(t) FROM (
  SELECT status_code,
         COALESCE(error_type,'')                       AS error_type,
         COALESCE(is_business_limited,false)            AS biz_limited,
         COALESCE(error_owner,'')                       AS error_owner,
         COALESCE(error_phase,'')                       AS error_phase,
         left(COALESCE(error_message,''),80)            AS msg,
         COUNT(*) AS n
  FROM ops_error_logs l, b
  WHERE l.created_at>=b.s AND l.created_at<b.u AND status_code IN (429,502,503,500)
  GROUP BY 1,2,3,4,5,6 ORDER BY n DESC LIMIT 40
) t;" 2>&1

echo
echo "=== err_by_group_model ==="
$PSQL -c "
WITH b AS (SELECT now()-interval '${WINDOW_HOURS} hour' AS s, now() AS u)
SELECT row_to_json(t) FROM (
  SELECT status_code, COALESCE(group_id,0) AS group_id,
         COALESCE(requested_model,'') AS model,
         COALESCE(account_id,0) AS account_id,
         COUNT(*) AS n
  FROM ops_error_logs l, b
  WHERE l.created_at>=b.s AND l.created_at<b.u AND status_code>=429
  GROUP BY 1,2,3,4 ORDER BY n DESC LIMIT 40
) t;" 2>&1

echo
echo "=== window_minutes (status>=429 per 5min) ==="
$PSQL -c "
WITH b AS (SELECT now()-interval '${WINDOW_HOURS} hour' AS s, now() AS u)
SELECT row_to_json(t) FROM (
  SELECT to_char(date_trunc('hour',created_at)+date_part('minute',created_at)::int/5*interval '5 min' AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI\"Z\"') AS slot5,
         status_code, COUNT(*) AS n
  FROM ops_error_logs l, b
  WHERE l.created_at>=b.s AND l.created_at<b.u AND status_code>=429
  GROUP BY 1,2 ORDER BY 1, n DESC LIMIT 60
) t;" 2>&1
