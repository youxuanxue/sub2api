#!/usr/bin/env bash
# Read-only: emit active admin proxies as field-named JSON lines.
# Source of truth matches https://tokenkey.dev/admin/proxies (proxies table).
#
#   bash ops/observability/run-probe.sh --target prod \
#     --script ops/observability/probe-list-proxies.sh
set -u
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

echo "=== proxies_active ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT id, name, protocol, host, port, status
  FROM proxies
  WHERE deleted_at IS NULL
    AND status = 'active'
  ORDER BY id
) t;"
