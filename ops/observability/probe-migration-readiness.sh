#!/usr/bin/env bash
# Read-only inventory for migration preparation. Never stops services, changes
# transaction settings, switches WAL, creates restore points, or edits DNS.
set -euo pipefail
set +x

TK_DOCKER="${TK_DOCKER:-sudo docker}"
# PG/Redis container names differ between Stage0 (tokenkey-postgres/-redis) and
# the Tencent USW layout; ops/lib/resolve-app-container.sh owns the choice and
# run-probe.sh ships it next to this script.
for _tk_lib in "${TK_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || { echo "container resolver not found (ops/lib/resolve-app-container.sh)" >&2; exit 1; }
TK_PG="$(tk_require_postgres_container "${POSTGRES_CONTAINER:-auto}")" || exit 1
printf '%s\n' '{"kind":"migration_readiness_inventory"}'

printf '%s\n' '--- database ---'
sudo docker exec -i \
  -e PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=5000 -c lock_timeout=500' \
  "$TK_PG" psql -U tokenkey -d tokenkey -XAtq -v ON_ERROR_STOP=1 <<'SQL'
SELECT json_build_object(
  'at', clock_timestamp(),
  'in_recovery', pg_is_in_recovery(),
  'current_lsn', CASE WHEN NOT pg_is_in_recovery() THEN pg_current_wal_lsn()::text END,
  'replay_lsn', pg_last_wal_replay_lsn()::text,
  'archiver', (SELECT json_build_object(
    'last_archived_wal', last_archived_wal, 'last_archived_time', last_archived_time,
    'failed_count', failed_count, 'last_failed_time', last_failed_time)
    FROM pg_stat_archiver),
  'sessions', (SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()),
  'active_transactions', (SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND xact_start IS NOT NULL),
  'longest_transaction_seconds', (SELECT extract(epoch FROM clock_timestamp()-min(xact_start)) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()),
  'longest_query_seconds', (SELECT extract(epoch FROM clock_timestamp()-min(query_start)) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND state='active')
);
SELECT json_build_object(
  'application_name', coalesce(application_name,''),
  'state', state,
  'backend_type', backend_type,
  'count', count(*)
)
FROM pg_stat_activity
WHERE datname=current_database() AND pid<>pg_backend_pid()
GROUP BY application_name,state,backend_type
ORDER BY count(*) DESC, application_name,state;
SQL

printf '%s\n' '--- host services and containers ---'
systemctl list-timers --all --no-legend 2>/dev/null | awk 'tolower($0) ~ /(backup|qa|maintenance|diagnostic|watch)/ {print}' | sed -E 's/[[:space:]]+/ /g' | cut -c1-500 || true
docker ps --format '{{.Names}}\t{{.Status}}\t{{.Image}}' | sed -E 's#(tokenkey[^[:space:]]*)\t#\1\t#' | cut -c1-500
