#!/usr/bin/env bash
# vacuum-ops-error-logs.sh — reclaim / refresh stats after tk_101 DROP COLUMN.
#
# DROP COLUMN only updates the catalog; regular VACUUM (ANALYZE) refreshes
# planner stats and reclaims ordinary dead tuples. It does NOT force a table
# rewrite (that needs VACUUM FULL / pg_repack). Parent size on a partitioned
# table is often 0 — report partition-summed size.
#
# Opt-in required (run-probe write guard):
#   --env EXECUTE_VACUUM=1
#
# Optional:
#   PARENT_TABLE   default ops_error_logs
#   POSTGRES_CONTAINER / PGUSER / PGDATABASE
set -u

if [ "${EXECUTE_VACUUM:-0}" != "1" ]; then
  echo '{"ok":false,"reason":"refusing without EXECUTE_VACUUM=1"}'
  exit 2
fi

POSTGRES_CONTAINER="${POSTGRES_CONTAINER:-tokenkey-postgres}"
PGUSER="${PGUSER:-tokenkey}"
PGDATABASE="${PGDATABASE:-tokenkey}"
PARENT_TABLE="${PARENT_TABLE:-ops_error_logs}"

PSQL=(docker exec "$POSTGRES_CONTAINER" psql -U "$PGUSER" -d "$PGDATABASE" -X -A -t -v ON_ERROR_STOP=1)

size_sql() {
  cat <<SQL
SELECT row_to_json(t) FROM (
  SELECT
    '${PARENT_TABLE}' AS relation,
    pg_size_pretty(COALESCE((
      SELECT SUM(pg_total_relation_size(i.inhrelid))
      FROM pg_inherits i
      JOIN pg_class p ON p.oid=i.inhparent
      WHERE p.relname='${PARENT_TABLE}'
    ), 0) + pg_total_relation_size('${PARENT_TABLE}'::regclass)) AS total_size_with_partitions,
    pg_size_pretty(pg_total_relation_size('${PARENT_TABLE}'::regclass)) AS parent_only_size,
    (SELECT COUNT(*) FROM ${PARENT_TABLE}) AS total_rows,
    to_char(MAX(last_vacuum) AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI"Z"') AS last_vacuum,
    to_char(MAX(last_autovacuum) AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI"Z"') AS last_autovacuum,
    to_char(MAX(last_analyze) AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI"Z"') AS last_analyze,
    to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"') AS observed_at_utc
  FROM pg_stat_all_tables
  WHERE relname LIKE '${PARENT_TABLE}%'
) t;
SQL
}

echo "=== before ==="
"${PSQL[@]}" -c "$(size_sql)" 2>&1

echo "=== vacuum_start ==="
# VACUUM cannot run inside a transaction block — one statement per -c.
# statement_timeout via PGOPTIONS so it is not a preceding SET in the same session txn.
if ! docker exec \
  -e PGOPTIONS='-c statement_timeout=0' \
  "$POSTGRES_CONTAINER" \
  psql -U "$PGUSER" -d "$PGDATABASE" -X -v ON_ERROR_STOP=1 \
  -c "VACUUM (ANALYZE) ${PARENT_TABLE};" 2>&1; then
  echo '{"ok":false,"reason":"vacuum_failed"}'
  exit 1
fi
echo "=== vacuum_done ==="

echo "=== after ==="
"${PSQL[@]}" -c "$(size_sql)" 2>&1
echo '{"ok":true,"action":"vacuum_analyze","relation":"'"${PARENT_TABLE}"'"}'
