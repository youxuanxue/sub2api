#!/bin/bash
# Read-only: proxies matching a name prefix (incl. soft-deleted) and every
# reference to them, as field-named JSON lines. Gate before shutting down a
# proxy host: live_accounts / fallback_origin_accounts / backup_of_live_proxies
# must all be 0.
#
#   bash ops/observability/run-probe.sh --target prod \
#     --script ops/observability/probe-proxy-bindings.sh --env PROXY_NAME_PREFIX=jp-
set -u

PROXY_NAME_PREFIX="${PROXY_NAME_PREFIX:-jp-}"
case "$PROXY_NAME_PREFIX" in
  *[!A-Za-z0-9_-]*|"") echo "PROXY_NAME_PREFIX must match [A-Za-z0-9_-]+" >&2; exit 2 ;;
esac

# Canonical resolver (ops/lib/resolve-app-container.sh) owns the PG container
# name; run-probe.sh ships it next to this probe.
_tk_resolver=""
for _cand in "${TK_LIB_DIR:-$(dirname "$0")}/resolve-app-container.sh" \
             "$(dirname "$0")/../lib/resolve-app-container.sh"; do
  if [ -f "$_cand" ]; then _tk_resolver="$_cand"; break; fi
done
if [ -z "$_tk_resolver" ]; then
  echo "canonical resolver not found (ops/lib/resolve-app-container.sh)" >&2
  exit 1
fi
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_resolver"
POSTGRES_CONTAINER="$(tk_resolve_postgres_container "${POSTGRES_CONTAINER:-auto}" || true)"
if [ -z "$POSTGRES_CONTAINER" ]; then
  echo "postgres container unresolved (want tokenkey-postgres or tk-archive-standby)" >&2
  exit 1
fi
PSQL=(docker exec -i -e 'PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=5s' "$POSTGRES_CONTAINER" psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

echo "=== proxy_bindings prefix=${PROXY_NAME_PREFIX} ==="
# Rows sharing a host with a prefixed proxy are included so legacy aliases
# (e.g. soft-deleted "default" rows) are visible too.
"${PSQL[@]}" <<SQL
SELECT row_to_json(t) FROM (
  SELECT p.id, p.name, p.host, p.port, p.status, p.deleted_at,
    (SELECT count(*) FROM accounts a WHERE a.proxy_id = p.id AND a.deleted_at IS NULL) AS live_accounts,
    (SELECT count(*) FROM accounts a WHERE a.proxy_id = p.id AND a.deleted_at IS NOT NULL) AS deleted_accounts,
    (SELECT count(*) FROM accounts a WHERE a.proxy_fallback_origin_id = p.id AND a.deleted_at IS NULL) AS fallback_origin_accounts,
    (SELECT count(*) FROM proxies q WHERE q.backup_proxy_id = p.id AND q.deleted_at IS NULL) AS backup_of_live_proxies
  FROM proxies p -- ops-allow-soft-deleted: audit includes soft-deleted aliases
  WHERE p.name LIKE '${PROXY_NAME_PREFIX}%'
     OR p.host IN (SELECT host FROM proxies WHERE name LIKE '${PROXY_NAME_PREFIX}%') -- ops-allow-soft-deleted
  ORDER BY p.id
) t;
SQL
