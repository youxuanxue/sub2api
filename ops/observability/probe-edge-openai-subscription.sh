#!/bin/bash
# probe-edge-openai-subscription.sh — READ-ONLY OAuth subscription state probe.
#
# Question: is the edge's openai OAuth account blocked because its ChatGPT
# subscription lapsed, rather than because of a 5h/weekly usage window?
#
# Emits ONLY non-secret credential fields (plan_type, the two expiry timestamps,
# token presence as a boolean) plus the derived "is it expired now" verdict.
# Access/refresh/id tokens are NEVER selected — only their length, so a missing
# token is distinguishable from a present one without printing any material.
#
# Pure SELECT. No writes, no upstream calls.
set -u
PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'

echo "=== now ==="
$PSQL -c "select to_char(now() at time zone 'UTC','MM-DD HH24:MI:SS')"

echo "=== [1] subscription verdict per openai account (non-secret fields only) ==="
$PSQL -c "
select row_to_json(t) from (
  select id, name, account_type, status, schedulable,
         credentials->>'plan_type'                       as plan_type,
         credentials->>'subscription_expires_at'         as sub_expires_at,
         credentials->>'expires_at'                      as token_expires_at,
         (credentials->>'subscription_expires_at') is not null
           and (credentials->>'subscription_expires_at')::timestamptz <= now()
                                                        as sub_expired_now,
         (credentials->>'expires_at') is not null
           and (credentials->>'expires_at')::timestamptz <= now()
                                                        as token_expired_now,
         length(coalesce(credentials->>'access_token',''))  as access_token_len,
         length(coalesce(credentials->>'refresh_token','')) as refresh_token_len,
         auto_pause_on_expired, expires_at as col_expires_at
  from accounts
  where platform='openai' and deleted_at is null
  order by id) t"

echo "=== [2] how long ago did the subscription lapse ==="
$PSQL -c "
select row_to_json(t) from (
  select id, name,
         credentials->>'subscription_expires_at' as sub_expires_at,
         date_trunc('second', now() - (credentials->>'subscription_expires_at')::timestamptz)::text as lapsed_for
  from accounts
  where platform='openai' and deleted_at is null
    and credentials->>'subscription_expires_at' is not null
  order by id) t"

echo "=== [3] did it serve ANY request after the subscription lapsed ==="
$PSQL -c "
select row_to_json(t) from (
  select a.id, a.name,
         count(u.id) as reqs_since_lapse,
         to_char(min(u.created_at),'MM-DD HH24:MI') as first_after,
         to_char(max(u.created_at),'MM-DD HH24:MI') as last_after
  from accounts a
  left join usage_logs u
    on u.account_id=a.id
   and u.created_at > (a.credentials->>'subscription_expires_at')::timestamptz
  where a.platform='openai' and a.deleted_at is null
    and a.credentials->>'subscription_expires_at' is not null
  group by a.id, a.name order by a.id) t"

echo "=== [4] its upstream 401/403 history (auth-shaped rejections) ==="
$PSQL -c "
select row_to_json(t) from (
  select account_id, status_code, upstream_status_code, count(*) n,
         to_char(max(created_at),'MM-DD HH24:MI') last_at,
         left(max(error_message),110) msg
  from ops_error_logs
  where created_at > now() - interval '30 days'
    and (upstream_status_code in (401,403) or status_code in (401,403))
  group by 1,2,3 order by n desc limit 12) t"

echo "=== [5] token_refresh outcomes recorded on the account ==="
$PSQL -c "
select row_to_json(t) from (
  select id, name, left(coalesce(error_message,'(empty)'),160) err,
         to_char(updated_at,'MM-DD HH24:MI:SS') upd
  from accounts
  where platform='openai' and deleted_at is null
  order by id) t"
