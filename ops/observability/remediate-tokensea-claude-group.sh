#!/usr/bin/env bash
# Write remediation: ensure tokensea account 136 is in the claude group.
# Requires APPLY=1. Delivered via run-probe.sh.
set -euo pipefail

APPLY="${APPLY:-0}"
ACCOUNT_ID="${ACCOUNT_ID:-136}"
GROUP_NAME="${GROUP_NAME:-claude}"

# PG/Redis container names differ between Stage0 (tokenkey-postgres/-redis) and
# the Tencent USW layout; ops/lib/resolve-app-container.sh owns the choice and
# run-probe.sh ships it next to this script.
for _tk_lib in "${TK_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || { echo "container resolver not found (ops/lib/resolve-app-container.sh)" >&2; exit 1; }
TK_PG="$(tk_require_postgres_container "${POSTGRES_CONTAINER:-auto}")" || exit 1
PSQL=(docker exec -i "$TK_PG" psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

echo "=== before ==="
"${PSQL[@]}" <<SQL
SELECT jsonb_build_object(
  'account_id', a.id,
  'name', a.name,
  'schedulable', a.schedulable,
  'groups', COALESCE((
    SELECT jsonb_agg(jsonb_build_object('id', g.id, 'name', g.name) ORDER BY g.name)
    FROM account_groups ag JOIN groups g ON g.id = ag.group_id
    WHERE ag.account_id = a.id
  ), '[]'::jsonb)
)
FROM accounts a
WHERE a.id = ${ACCOUNT_ID} AND a.deleted_at IS NULL;
SQL

GROUP_ID="$("${PSQL[@]}" -c "SELECT id::text FROM groups WHERE lower(name)=lower('${GROUP_NAME}') AND deleted_at IS NULL ORDER BY id LIMIT 1;")"
if [ -z "$GROUP_ID" ]; then
  echo '{"verdict":"setup_error","error":"claude_group_missing"}'
  exit 2
fi
echo "group_id=${GROUP_ID}"

if [ "$APPLY" != "1" ]; then
  echo '{"verdict":"dry_run","account_id":'"$ACCOUNT_ID"',"group_id":'"$GROUP_ID"'}'
  exit 0
fi

echo "=== apply ==="
"${PSQL[@]}" <<SQL
INSERT INTO account_groups (account_id, group_id, priority, created_at)
SELECT ${ACCOUNT_ID}, ${GROUP_ID},
       COALESCE((SELECT MAX(priority) FROM account_groups WHERE account_id = ${ACCOUNT_ID}), 0) + 1,
       NOW()
WHERE EXISTS (
  SELECT 1 FROM accounts WHERE id = ${ACCOUNT_ID} AND deleted_at IS NULL
)
AND NOT EXISTS (
  SELECT 1 FROM account_groups WHERE account_id = ${ACCOUNT_ID} AND group_id = ${GROUP_ID}
);
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_groups_changed', ${ACCOUNT_ID}, NULL,
       jsonb_build_object('group_ids', COALESCE((
         SELECT jsonb_agg(group_id ORDER BY priority, group_id)
         FROM account_groups WHERE account_id = ${ACCOUNT_ID}
       ), '[]'::jsonb))
WHERE EXISTS (
  SELECT 1 FROM account_groups WHERE account_id = ${ACCOUNT_ID} AND group_id = ${GROUP_ID}
);
SQL

echo "=== after ==="
"${PSQL[@]}" <<SQL
SELECT jsonb_build_object(
  'account_id', a.id,
  'name', a.name,
  'schedulable', a.schedulable,
  'groups', COALESCE((
    SELECT jsonb_agg(jsonb_build_object('id', g.id, 'name', g.name) ORDER BY g.name)
    FROM account_groups ag JOIN groups g ON g.id = ag.group_id
    WHERE ag.account_id = a.id
  ), '[]'::jsonb)
)
FROM accounts a
WHERE a.id = ${ACCOUNT_ID} AND a.deleted_at IS NULL;
SQL
echo '{"verdict":"applied","account_id":'"$ACCOUNT_ID"',"group_id":'"$GROUP_ID"'}'
