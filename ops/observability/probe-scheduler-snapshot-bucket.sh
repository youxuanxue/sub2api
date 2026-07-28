#!/usr/bin/env bash
# probe-scheduler-snapshot-bucket.sh — read-only scheduler snapshot bucket vs DB parity.
#
# Mirrors runtime ListSchedulableAccounts(group, platform, hasForcePlatform=false)
# for gemini/anthropic mixed buckets: Redis sched:* keys + DB mixed query + outbox tail.
#
# Env:
#   GROUP_ID   — target group (required)
#   PLATFORM   — gemini | anthropic (default: gemini)
#   OUTBOX_LIMIT — recent scheduler_outbox rows (default: 20)
set -u

PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'
RC='docker exec tokenkey-redis redis-cli'

GROUP_ID="${GROUP_ID:?GROUP_ID required}"
PLATFORM="${PLATFORM:-gemini}"
OUTBOX_LIMIT="${OUTBOX_LIMIT:-20}"

case "$PLATFORM" in
  gemini|anthropic) MODE=mixed ;;
  *)
    echo "unsupported PLATFORM=$PLATFORM (use gemini or anthropic)" >&2
    exit 1
    ;;
esac

BUCKET="${GROUP_ID}:${PLATFORM}:${MODE}"
READY_KEY="sched:ready:${BUCKET}"
ACTIVE_KEY="sched:active:${BUCKET}"
EPOCH_KEY="sched:epoch:${BUCKET}"
RETIRED_KEY="sched:retired:${BUCKET}"

echo "=== probe scheduler bucket group=$GROUP_ID platform=$PLATFORM mode=$MODE ==="

echo
echo "=== group authority ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT id, name, platform, status
  FROM groups
  WHERE id = ${GROUP_ID}
    AND deleted_at IS NULL
) t;
" 2>&1

echo
echo "=== DB mixed schedulable pool (matches loadAccountsFromDB useMixed=true) ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT
    a.id,
    a.name,
    a.platform,
    a.type,
    a.status,
    a.schedulable,
    COALESCE(a.extra->>'mixed_scheduling', '') AS mixed_scheduling,
    a.rate_limit_reset_at,
    a.overload_until,
    a.temp_unschedulable_until,
    ag.priority AS group_priority
  FROM account_groups ag
  JOIN accounts a ON a.id = ag.account_id AND a.deleted_at IS NULL
  WHERE ag.group_id = ${GROUP_ID}
    AND a.status = 'active'
    AND a.schedulable = true
    AND a.platform IN ('${PLATFORM}', 'antigravity')
    AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until <= NOW())
    AND (a.expires_at IS NULL OR a.expires_at > NOW() + INTERVAL '60 seconds')
    AND (a.overload_until IS NULL OR a.overload_until <= NOW())
    AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at <= NOW())
  ORDER BY ag.priority, a.priority, a.id
) t;
" 2>&1

echo
echo "=== DB after antigravity mixed_scheduling filter (runtime post-filter) ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT
    a.id,
    a.name,
    a.platform,
    COALESCE(a.extra->>'mixed_scheduling', '') AS mixed_scheduling
  FROM account_groups ag
  JOIN accounts a ON a.id = ag.account_id AND a.deleted_at IS NULL
  WHERE ag.group_id = ${GROUP_ID}
    AND a.status = 'active'
    AND a.schedulable = true
    AND a.platform IN ('${PLATFORM}', 'antigravity')
    AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until <= NOW())
    AND (a.expires_at IS NULL OR a.expires_at > NOW() + INTERVAL '60 seconds')
    AND (a.overload_until IS NULL OR a.overload_until <= NOW())
    AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at <= NOW())
    AND (
      a.platform = '${PLATFORM}'
      OR COALESCE(a.extra->>'mixed_scheduling', 'false') = 'true'
    )
  ORDER BY ag.priority, a.priority, a.id
) t;
" 2>&1

echo
echo "=== Redis bucket keys ==="
ready=$($RC GET "$READY_KEY" 2>/dev/null)
active=$($RC GET "$ACTIVE_KEY" 2>/dev/null)
epoch=$($RC GET "$EPOCH_KEY" 2>/dev/null)
retired=$($RC GET "$RETIRED_KEY" 2>/dev/null)
echo "bucket=$BUCKET ready=${ready:-} active=${active:-} epoch=${epoch:-} retired=${retired:-}"

if [[ -n "${active:-}" ]]; then
  snap_key="sched:${BUCKET}:v${active}"
  zcard=$($RC ZCARD "$snap_key" 2>/dev/null)
  members=$($RC ZRANGE "$snap_key" 0 -1 2>/dev/null)
  echo "snapshot_key=$snap_key zcard=${zcard:-0} members=${members:-}"
  if [[ -n "${members:-}" ]]; then
    echo "=== Redis sched:meta for snapshot members ==="
    for id in $members; do
      meta=$($RC GET "sched:meta:${id}" 2>/dev/null)
      if [[ -z "${meta:-}" ]]; then
        echo "acct=$id meta=MISSING"
      else
        platform=$(printf '%s' "$meta" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("Platform", d.get("platform","")))' 2>/dev/null || echo '?')
        mixed=$(printf '%s' "$meta" | python3 -c 'import json,sys; d=json.load(sys.stdin); e=d.get("Extra") or d.get("extra") or {}; print(e.get("mixed_scheduling",""))' 2>/dev/null || echo '?')
        echo "acct=$id platform=$platform mixed_scheduling=$mixed"
      fi
    done
  fi
else
  echo "snapshot_key=UNSET (no active version)"
fi

echo
echo "=== scheduler_outbox watermark + recent events touching group/account ==="
$PSQL -c "SELECT 'watermark=' || COALESCE((SELECT MAX(id)::text FROM scheduler_outbox WHERE id <= (SELECT COALESCE(NULLIF(current_setting('tk.redis_outbox_watermark', true), ''), '0')::bigint)), 'unknown');" 2>/dev/null || true
$RC GET sched:outbox:watermark 2>/dev/null | awk '{print "redis_watermark=" $0}'

$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT id, event_type, account_id, group_id, created_at, payload
  FROM scheduler_outbox
  WHERE (
    group_id = ${GROUP_ID}
    OR payload::text LIKE '%\"group_ids\":%${GROUP_ID}%'
    OR payload::text LIKE '%\"account_ids\":%'
  )
  ORDER BY id DESC
  LIMIT ${OUTBOX_LIMIT}
) t;
" 2>&1

echo
echo "=== recent account_changed / account_bulk_changed for antigravity in group ${GROUP_ID} ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT o.id, o.event_type, o.account_id, o.created_at, o.payload, a.platform, a.extra->>'mixed_scheduling' AS mixed_scheduling
  FROM scheduler_outbox o
  LEFT JOIN accounts a ON a.id = o.account_id AND a.deleted_at IS NULL
  WHERE o.event_type IN ('account_changed', 'account_bulk_changed', 'account_groups_changed')
    AND (
      o.group_id = ${GROUP_ID}
      OR EXISTS (
        SELECT 1 FROM account_groups ag
        WHERE ag.group_id = ${GROUP_ID} AND ag.account_id = o.account_id
      )
      OR o.payload::text LIKE '%\"group_ids\":%${GROUP_ID}%'
    )
  ORDER BY o.id DESC
  LIMIT ${OUTBOX_LIMIT}
) t;
" 2>&1
