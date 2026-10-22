#!/usr/bin/env bash
# Emergency read/write remediation: restore anthropic OAuth schedulability and clear
# stale cooldown fields that block the scheduling pool.
# Delivered via run-probe.sh; MODE selects the fix shape.
set -euo pipefail

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
MODE="${MODE:-}"

case "$MODE" in
  anthropic-oauth-pool)
    echo "=== before ==="
    $PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT id, name, type, status, schedulable, overload_until,
         temp_unschedulable_until, left(coalesce(error_message,''),80) AS error_message
  FROM accounts
  WHERE platform='anthropic' AND type IN ('oauth','setup-token') AND deleted_at IS NULL
  ORDER BY id
) t;"
    echo "=== apply ==="
    $PSQL -c "
UPDATE accounts SET
  schedulable = true,
  overload_until = NULL,
  rate_limited_at = NULL,
  rate_limit_reset_at = NULL,
  temp_unschedulable_until = NULL,
  temp_unschedulable_reason = NULL
WHERE platform = 'anthropic'
  AND type IN ('oauth', 'setup-token')
  AND deleted_at IS NULL
  AND status = 'active'
  AND coalesce(error_message, '') = '';
"
    # Ungrouped active oauth with empty error: attach the default group (id=1,
    # seeded by migrations/008_seed_default_group.sql) so group-scoped routes see it.
    $PSQL -c "
UPDATE accounts SET group_id = 1
WHERE platform = 'anthropic'
  AND type = 'oauth'
  AND deleted_at IS NULL
  AND status = 'active'
  AND group_id IS NULL
  AND coalesce(error_message, '') = '';
"
    echo "=== after ==="
    $PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT id, name, type, status, schedulable, group_id, overload_until,
         temp_unschedulable_until, left(coalesce(error_message,''),80) AS error_message
  FROM accounts
  WHERE platform='anthropic' AND type IN ('oauth','setup-token') AND deleted_at IS NULL
  ORDER BY id
) t;"
    ;;
  *)
    echo "MODE must be anthropic-oauth-pool" >&2
    exit 2
    ;;
esac
