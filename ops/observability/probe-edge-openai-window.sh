#!/usr/bin/env bash
# probe-edge-openai-window.sh — READ-ONLY openai-pool window/quota snapshot for an edge.
#
# Question (2026-08-25): prod account 63 openai-us6 kept returning
#   "Recovered upstream error 429: No available accounts ... total=1 eligible=0 unschedulable=1"
# That counter text is produced by the EDGE's own scheduler (us6 runs the same
# binary), not by prod — prod merely relays it. So the pool that is empty is the
# edge's openai pool. This probe reads that pool's schedulability + the session
# window / rate-limit columns to tell a 5h-window quota block apart from a
# transport failure or a manual close.
#
# Pure SELECT. No writes, no restarts, no credential values printed.
#
# Env:
#   PLATFORM  account platform to roster. Default openai.
#   NAME_LIKE optional ILIKE filter on account name (e.g. 'oh-3'). Default '' = all.
set -u

PLATFORM="${PLATFORM:-openai}"
NAME_LIKE="${NAME_LIKE:-}"
PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'

# SQL-injection guard: PLATFORM/NAME_LIKE are operator-supplied, so allow only a
# conservative charset before interpolation (same discipline as the other probes).
case "$PLATFORM" in
  *[!a-z0-9_-]*) echo "probe-edge-openai-window: bad PLATFORM" >&2; exit 2 ;;
esac
case "$NAME_LIKE" in
  *[!A-Za-z0-9_.-]*) echo "probe-edge-openai-window: bad NAME_LIKE" >&2; exit 2 ;;
esac

NAME_FILTER=""
if [ -n "$NAME_LIKE" ]; then
  NAME_FILTER="and a.name ilike '%${NAME_LIKE}%'"
fi

echo "=== now ==="
$PSQL -c "select to_char(now(),'MM-DD HH24:MI:SS')"

echo "=== [1] pool roster: schedulable + window + rate-limit columns ==="
$PSQL -c "
select coalesce(json_agg(row_to_json(t) order by t.id)::text,'[]') from (
  select a.id, a.name, a.platform, a.status, a.schedulable,
         a.quota_dimension,
         a.session_window_status,
         to_char(a.session_window_start,'MM-DD HH24:MI') as win_start,
         to_char(a.session_window_end,'MM-DD HH24:MI')   as win_end,
         to_char(a.rate_limited_at,'MM-DD HH24:MI')      as rl_at,
         to_char(a.rate_limit_reset_at,'MM-DD HH24:MI')  as rl_reset,
         to_char(a.temp_unschedulable_until,'MM-DD HH24:MI') as temp_until,
         left(coalesce(a.temp_unschedulable_reason,''),120) as temp_reason,
         to_char(a.updated_at,'MM-DD HH24:MI')           as upd
  from accounts a
  where a.platform='${PLATFORM}' and a.deleted_at is null ${NAME_FILTER}
) t"

echo "=== [2] how many are actually eligible right now (schedulable+active+not cooling) ==="
$PSQL -c "
select coalesce(json_agg(row_to_json(t))::text,'[]') from (
  select count(*) as total,
         count(*) filter (where a.schedulable and a.status='active') as schedulable_active,
         count(*) filter (where a.rate_limit_reset_at is not null and a.rate_limit_reset_at > now()) as cooling_now,
         count(*) filter (where a.temp_unschedulable_until is not null and a.temp_unschedulable_until > now()) as temp_blocked_now,
         count(*) filter (where a.session_window_status is not null and a.session_window_status <> 'allowed') as window_blocked
  from accounts a
  where a.platform='${PLATFORM}' and a.deleted_at is null
) t"

echo "=== [3] session-window status distribution (5h-window evidence) ==="
$PSQL -c "
select coalesce(json_agg(row_to_json(t) order by t.n desc)::text,'[]') from (
  select coalesce(a.session_window_status,'(null)') as ws,
         coalesce(a.quota_dimension,'(null)') as qd,
         count(*) n
  from accounts a
  where a.platform='${PLATFORM}' and a.deleted_at is null
  group by 1,2
) t"

echo "=== [4] this edge's own served vs empty-pool, last 3h (its scheduler, not prod's) ==="
$PSQL -c "
select coalesce(json_agg(row_to_json(t) order by t.hr)::text,'[]') from (
  select to_char(date_trunc('hour', created_at),'MM-DD HH24') as hr, count(*) n
  from usage_logs
  where created_at >= now() - interval '3 hours'
  group by 1
) t"

echo "=== [5] empty-pool rejections this edge emitted, last 3h ==="
$PSQL -c "
select coalesce(json_agg(row_to_json(t) order by t.n desc)::text,'[]') from (
  select coalesce(requested_model,'(null)') as model,
         status_code, coalesce(upstream_status_code,0) as ups,
         count(*) n, to_char(max(created_at),'HH24:MI:SS') as last_at,
         left(coalesce(max(error_message),''),100) as msg
  from ops_error_logs
  where created_at >= now() - interval '3 hours'
    and error_phase='routing'
  group by 1,2,3
) t"
