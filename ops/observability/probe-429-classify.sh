#!/usr/bin/env bash
# probe-429-classify.sh — read-only classification of final-429 + 5xx rows in
# ops_error_logs over a window, to separate config-cap (group rpm / business
# limited) from empty-pool / no-available-accounts (#575) and real upstream.
# Shipped via run-probe.sh. row_to_json output only; parse by field name.
set -u
WINDOW_HOURS="${WINDOW_HOURS:-3}"
case "$WINDOW_HOURS" in ''|*[!0-9]*) echo "bad WINDOW_HOURS" >&2; exit 2;; esac
# PG/Redis container names differ between Stage0 (tokenkey-postgres/-redis) and
# the Tencent USW layout; ops/lib/resolve-app-container.sh owns the choice and
# run-probe.sh ships it next to this script.
for _tk_lib in "${TK_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || { echo "container resolver not found (ops/lib/resolve-app-container.sh)" >&2; exit 1; }
TK_PG="$(tk_require_postgres_container "${POSTGRES_CONTAINER:-auto}")" || exit 1
PSQL="docker exec $TK_PG psql -U tokenkey -d tokenkey -X -A -t"

echo "=== 429_by_type ==="
$PSQL -c "
WITH b AS (SELECT now()-interval '${WINDOW_HOURS} hour' AS s, now() AS u)
SELECT row_to_json(t) FROM (
  SELECT status_code,
         COALESCE(error_type,'')                       AS error_type,
         COALESCE(error_owner,'')                       AS error_owner,
         COALESCE(error_phase,'')                       AS error_phase,
         left(COALESCE(error_message,''),80)            AS msg,
         -- error_message is the sanitized public copy and is identical for an
         -- empty pool and a real upstream limit, so it cannot tell them apart.
         -- These two can: error_phase (routing vs upstream) plus the internal
         -- upstream_error_message ("No available accounts..." = local pool,
         -- "Resource has been exhausted..." = Google's own quota reply).
         -- Do NOT triage on error_type: the universal ingress hardcodes
         -- rate_limit_error for an empty pool (universal_routing_tk.go) while
         -- the per-platform handlers annotate api_error, so error_type encodes
         -- which ingress served the request, not what went wrong.
         left(COALESCE(upstream_error_message,''),80)   AS up_msg,
         COALESCE(upstream_status_code::text,'')        AS up_code,
         COUNT(*) AS n
  FROM ops_error_logs l, b
  WHERE l.created_at>=b.s AND l.created_at<b.u AND status_code IN (429,502,503,500)
  GROUP BY 1,2,3,4,5,6,7 ORDER BY n DESC LIMIT 40
) t;" 2>&1

echo
echo "=== active_model_cooldowns ==="
# Why a capacity 429 happened, not just that it did. An upstream 404 pins a
# model-level cooldown (reason=antigravity_404_model_unavailable) which empties
# the schedulable pool; the client then sees a capacity 429 and the 404 is gone
# from the error logs once every account is cooled. This reads the cooldown
# records straight out of accounts.extra->model_rate_limits so the cause is
# visible for the whole cooldown window, not only at its head.
#
# reason is optional in the payload -- an empty reason means "cooldown with no
# recorded cause", not "no cooldown".
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT a.platform,
         e.key                                          AS scope,
         COALESCE(e.value->>'reason','')                AS reason,
         COUNT(*)                                       AS accounts,
         COUNT(*) FILTER (
           WHERE (e.value->>'rate_limit_reset_at')::timestamptz > now()
         )                                              AS still_cooling,
         max(e.value->>'rate_limit_reset_at')           AS latest_reset_at
  FROM accounts a, jsonb_each((a.extra->'model_rate_limits')::jsonb) AS e(key,value)
  WHERE a.deleted_at IS NULL AND a.status='active'
  GROUP BY 1,2,3 ORDER BY still_cooling DESC, accounts DESC LIMIT 30
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
  SELECT to_char(date_bin('5 min', created_at, timestamptz 'epoch') AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI\"Z\"') AS slot5,
         status_code, COUNT(*) AS n
  FROM ops_error_logs l, b
  WHERE l.created_at>=b.s AND l.created_at<b.u AND status_code>=429
  GROUP BY 1,2 ORDER BY 1, n DESC LIMIT 60
) t;" 2>&1
