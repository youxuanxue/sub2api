#!/usr/bin/env bash
# Read-only: emit active admin proxies as field-named JSON lines.
# Source of truth matches https://tokenkey.dev/admin/proxies (proxies table).
#
#   bash ops/observability/run-probe.sh --target prod \
#     --script ops/observability/probe-list-proxies.sh
set -u
PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'

echo "=== proxies_active ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT id, name, protocol, host, port, status
  FROM proxies
  WHERE deleted_at IS NULL
    AND status = 'active'
  ORDER BY id
) t;"
