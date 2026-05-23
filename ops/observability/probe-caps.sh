#!/bin/bash
# probe-caps.sh — TokenKey read-only caps + 不可调度证据 + Redis 快照 + 近 2h 错误日志。
#
# Designed to run INSIDE the TokenKey host (prod or edge), via SSM `AWS-RunShellScript`
# with base64 delivery (see ops/observability/README or the SKILL `tokenkey-online-traffic-profile`).
# Pure read-only: psql SELECT + redis-cli ZCARD/GET/EVAL + docker ps.
#
# Determinism contract (matches dev-rules-convention.mdc §"skill / command 确定性基线"):
#   - All cap rows are emitted as `row_to_json(t)` — field names embedded next to values.
#     Downstream parsers must use jq/json.loads keyed by field name, never by column position.
#   - Redis snapshot rows are `key=value` whitespace-separated, again named.
#   - No locale-sensitive numeric formatting; no positional `|`-delimited tables.
#
# Env:
#   PLATFORM   — anthropic | openai | gemini | antigravity | newapi  (default: anthropic)
#   ERR_HOURS  — ops_error_logs lookback hours (default: 2)
#   ERR_LIMIT  — ops_error_logs row cap (default: 150)
#
# Container names follow Stage0 compose (docker-compose.yml): tokenkey, tokenkey-postgres,
# tokenkey-redis. Verified by `docker ps` block at the top of the output.
set -uo pipefail

PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'
RC='docker exec tokenkey-redis redis-cli'
PLATFORM="${PLATFORM:-anthropic}"
ERR_HOURS="${ERR_HOURS:-2}"
ERR_LIMIT="${ERR_LIMIT:-150}"

echo "=== docker ps (tokenkey stack) ==="
docker ps --filter name=tokenkey --format '{{.Names}}\t{{.Status}}\t{{.Image}}'

echo
echo "=== caps + schedulability evidence (one JSON per account; field names embedded) ==="
# row_to_json: 字段名嵌入值旁，物理不可能数错列。坑 6 的硬纪律。
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT
    a.id, a.name, a.platform, a.type, a.status, a.schedulable, a.concurrency,
    -- 不可调度证据（顶层列，不要去 extra 找）
    a.rate_limited_at, a.rate_limit_reset_at, a.overload_until,
    a.temp_unschedulable_until, a.temp_unschedulable_reason,
    a.session_window_status, a.session_window_start, a.session_window_end,
    left(COALESCE(a.error_message,''),200) AS error_message,
    -- cap（extra JSON）
    a.extra->>'base_rpm'                       AS base_rpm,
    a.extra->>'rpm_strategy'                   AS rpm_strategy,
    a.extra->>'rpm_sticky_buffer'              AS rpm_sticky_buffer,
    a.extra->>'max_sessions'                   AS max_sessions,
    a.extra->>'session_idle_timeout_minutes'   AS idle_min,
    a.extra->>'window_cost_limit'              AS window_cost_limit,
    a.extra->>'stability_tier'                 AS tier,
    -- 组关系
    ag.group_id, ag.priority AS group_priority
  FROM accounts a
  LEFT JOIN account_groups ag ON ag.account_id=a.id
  WHERE a.platform='$PLATFORM'
  ORDER BY a.id, ag.group_id NULLS LAST
) t;
" 2>&1

echo
echo "=== Redis snapshot (active accounts of platform=$PLATFORM) ==="
IDS=$($PSQL -c "SELECT string_agg(id::text,' ' ORDER BY id) FROM accounts WHERE platform='$PLATFORM' AND status='active';" 2>/dev/null)
echo "active_ids: $IDS"
for id in $IDS; do
  conc=$($RC ZCARD concurrency:account:$id 2>/dev/null)
  sess=$($RC ZCARD session_limit:account:$id 2>/dev/null)
  wait=$($RC ZCARD wait:account:$id 2>/dev/null)
  wc=$($RC GET    window_cost:account:$id 2>/dev/null)
  rpm=$($RC EVAL "local t=redis.call('TIME'); return redis.call('GET','rpm:'..ARGV[1]..':'..math.floor(tonumber(t[1])/60)) or '0'" 0 $id 2>/dev/null)
  # 字段名贴在值旁；解析 = grep+awk；禁止数列号
  echo "redis_snapshot acct=$id conc=$conc sess=$sess wait=$wait wcost=${wc:-} rpm_now=$rpm"
done

echo
echo "=== ops_error_logs last ${ERR_HOURS}h (platform-filtered + schedulability keywords) ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD HH24:MI:SS') AS ts_utc,
         severity, error_phase, error_type, status_code, upstream_status_code,
         account_id, model, provider_error_code,
         left(error_message,200) AS error_message
  FROM ops_error_logs
  WHERE created_at >= now()-interval '$ERR_HOURS hour'
    AND (platform='$PLATFORM'
         OR error_message ILIKE '%schedulable%'
         OR error_message ILIKE '%no available%'
         OR error_message ILIKE '%cooldown%'
         OR error_message ILIKE '%rate_limit%')
  ORDER BY created_at DESC
  LIMIT $ERR_LIMIT
) t;
" 2>&1
