#!/usr/bin/env bash
# Read-only observations. No checkpoint, WAL switch, write isolation, or promotion.
# Deliver through run-probe.sh with its expected-instance-id guard. Repeated
# snapshots measure WAL generation; a source snapshot alone proves no target lag.
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
sudo docker exec -i \
  -e PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=5000 -c lock_timeout=500' \
  "$TK_PG" psql -U tokenkey -d tokenkey -XAtq -v ON_ERROR_STOP=1 <<'SQL'
SELECT json_build_object(
  'kind', 'pg_migration_snapshot',
  'at', clock_timestamp(),
  'server_version', current_setting('server_version'),
  'system_identifier', (SELECT system_identifier::text FROM pg_control_system()),
  'checkpoint_timeline', (SELECT timeline_id FROM pg_control_checkpoint()),
  'in_recovery', pg_is_in_recovery(),
  'current_lsn', CASE WHEN NOT pg_is_in_recovery() THEN pg_current_wal_lsn()::text END,
  'replay_lsn', pg_last_wal_replay_lsn()::text,
  'last_replay_transaction_at', pg_last_xact_replay_timestamp(),
  'archive_mode', current_setting('archive_mode'),
  'archive_timeout', current_setting('archive_timeout'),
  'wal_segment_size', current_setting('wal_segment_size'),
  'archiver', (SELECT json_build_object(
    'last_archived_wal', last_archived_wal, 'last_archived_time', last_archived_time,
    'failed_count', failed_count, 'last_failed_time', last_failed_time,
    'stats_reset', stats_reset) FROM pg_stat_archiver),
  'sessions', (SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()),
  'transactions', (SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND xact_start IS NOT NULL),
  'oldest_transaction_seconds', (SELECT extract(epoch FROM clock_timestamp()-min(xact_start)) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid())
);
SQL
