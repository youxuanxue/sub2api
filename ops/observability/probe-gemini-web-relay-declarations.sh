#!/usr/bin/env bash
# Read-only pre-deploy check, including disabled accounts that may be enabled later.
set -euo pipefail

# PG/Redis container names differ between Stage0 (tokenkey-postgres/-redis) and
# the Tencent USW layout; ops/lib/resolve-app-container.sh owns the choice and
# run-probe.sh ships it next to this script.
for _tk_lib in "${TK_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || { echo "container resolver not found (ops/lib/resolve-app-container.sh)" >&2; exit 1; }
if ! POSTGRES_CONTAINER="$(tk_resolve_postgres_container "${POSTGRES_CONTAINER:-auto}")"; then
  echo '{"verdict":"setup_error","error":"no_postgres_container"}'
  exit 2
fi

CHECK_PY="${GEMINI_WEB_RELAY_CHECK:-/tmp/gemini_web_relay_check.py}"

SNAPSHOT_SQL=$(cat <<'SQL'
SELECT jsonb_build_object('accounts', COALESCE(jsonb_agg(jsonb_build_object(
    'id', id, 'platform', platform, 'type', type,
    'declared_web', COALESCE(extra->>'relay_kind' = 'gemini_web', false),
    'marker', credentials->'gemini_web_relay'
) ORDER BY id), '[]'::jsonb))::text
FROM accounts
WHERE deleted_at IS NULL
  AND (extra->>'relay_kind' = 'gemini_web' OR credentials ? 'gemini_web_relay');
SQL
)

if ! SNAPSHOT="$(docker exec -i \
    -e 'PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=10s' \
    "$POSTGRES_CONTAINER" psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 \
    -c "$SNAPSHOT_SQL")"; then
  echo '{"verdict":"setup_error","error":"snapshot_query_failed"}'
  exit 2
fi
printf '%s\n' "$SNAPSHOT" | python3 "$CHECK_PY"
