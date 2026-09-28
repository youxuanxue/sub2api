#!/usr/bin/env bash
# probe-migration-applied-verify.sh — read-only confirmation that named migrations
# actually ran on THIS host's database.
#
# Migration gates read DDL in the repo, not the live catalog. Every Edge has its
# own Postgres, so each host must be checked separately. Deliver via run-probe.sh.
#
# Env (all optional; defaults reproduce the tk_101 finalize check):
#   POSTGRES_CONTAINER   docker container (default: tokenkey-postgres)
#   PGUSER / PGDATABASE  defaults: tokenkey / tokenkey
#   MIGRATION_FILENAMES  space-separated schema_migrations.filename values
#   PARENT_TABLE         partitioned/parent table to inspect (default: ops_error_logs)
#   DROPPED_COLUMNS      space-separated column names that must be absent
#   DROPPED_TABLES       space-separated relations that must be gone
#   SAMPLE_DROPPED_COLS  subset used for per-partition residual checks (defaults to
#                        first three of DROPPED_COLUMNS)
#
# All SQL-interpolated names must match IDENT_RE (fail closed).
# row_to_json output only; parse by field name.
set -u

IDENT_RE='^[A-Za-z_][A-Za-z0-9_]*$'
FILENAME_RE='^[A-Za-z0-9][A-Za-z0-9._-]*$'

require_ident() {
  local label="$1" value="$2"
  if [[ ! "$value" =~ $IDENT_RE ]]; then
    echo "{\"ok\":false,\"reason\":\"invalid_${label}\",\"value\":$(printf '%s' "$value" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')}"
    exit 2
  fi
}

require_filename() {
  local value="$1"
  if [[ ! "$value" =~ $FILENAME_RE ]]; then
    echo "{\"ok\":false,\"reason\":\"invalid_migration_filename\",\"value\":$(printf '%s' "$value" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')}"
    exit 2
  fi
}

POSTGRES_CONTAINER="${POSTGRES_CONTAINER:-tokenkey-postgres}"
PGUSER="${PGUSER:-tokenkey}"
PGDATABASE="${PGDATABASE:-tokenkey}"
PARENT_TABLE="${PARENT_TABLE:-ops_error_logs}"

MIGRATION_FILENAMES="${MIGRATION_FILENAMES:-tk_100_ops_error_logs_drop_unwritten_columns.sql tk_101_ops_error_logs_finalize_unwritten_columns.sql}"
DROPPED_COLUMNS="${DROPPED_COLUMNS:-duration_ms network_error_type provider_error_code provider_error_type account_status retry_after_seconds attempted_key_prefix deleted_key_owner_user_id deleted_key_name}"
DROPPED_TABLES="${DROPPED_TABLES:-deleted_api_key_audits}"

require_ident PARENT_TABLE "$PARENT_TABLE"

# shellcheck disable=SC2206
_migration_arr=($MIGRATION_FILENAMES)
_column_arr=($DROPPED_COLUMNS)
_table_arr=($DROPPED_TABLES)

if [ -n "${SAMPLE_DROPPED_COLS:-}" ]; then
  # shellcheck disable=SC2206
  _sample_arr=($SAMPLE_DROPPED_COLS)
else
  _sample_arr=("${_column_arr[@]:0:3}")
fi

for _f in "${_migration_arr[@]}"; do require_filename "$_f"; done
for _c in "${_column_arr[@]}"; do require_ident DROPPED_COLUMNS "$_c"; done
for _c in "${_sample_arr[@]}"; do require_ident SAMPLE_DROPPED_COLS "$_c"; done
for _t in "${_table_arr[@]}"; do require_ident DROPPED_TABLES "$_t"; done

sql_quote_list() {
  # Turn argv into 'a','b','c' for IN (...). Values already allowlisted.
  local first=1 item
  for item in "$@"; do
    if [ "$first" -eq 1 ]; then
      first=0
    else
      printf ','
    fi
    printf "'%s'" "$item"
  done
}

MIGRATION_IN="$(sql_quote_list "${_migration_arr[@]}")"
COLUMN_IN="$(sql_quote_list "${_column_arr[@]}")"
SAMPLE_IN="$(sql_quote_list "${_sample_arr[@]}")"

PSQL=(docker exec "$POSTGRES_CONTAINER" psql -U "$PGUSER" -d "$PGDATABASE" -X -A -t)

echo "=== migration_recorded ==="
"${PSQL[@]}" -c "
SELECT row_to_json(t) FROM (
  SELECT filename,
         to_char(applied_at AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS applied_at_utc
  FROM schema_migrations
  WHERE filename IN (${MIGRATION_IN})
  ORDER BY filename
) t;" 2>&1

if [ "${#_column_arr[@]}" -gt 0 ]; then
  echo
  echo "=== dropped_columns_still_present (expect zero rows) ==="
  "${PSQL[@]}" -c "
SELECT row_to_json(t) FROM (
  SELECT c.table_name, c.column_name
  FROM information_schema.columns c
  WHERE c.table_name = '${PARENT_TABLE}'
    AND c.column_name IN (${COLUMN_IN})
  ORDER BY c.column_name
) t;" 2>&1
fi

echo
echo "=== parent_column_count_and_partitions ==="
"${PSQL[@]}" -c "
SELECT row_to_json(t) FROM (
  SELECT
    (SELECT COUNT(*) FROM information_schema.columns
      WHERE table_name='${PARENT_TABLE}') AS parent_columns,
    (SELECT COUNT(*) FROM pg_inherits i
      JOIN pg_class p ON p.oid=i.inhparent
      WHERE p.relname='${PARENT_TABLE}') AS partitions,
    (SELECT COUNT(*) FROM pg_attribute a
      JOIN pg_class c ON c.oid=a.attrelid
      JOIN pg_inherits i ON i.inhrelid=c.oid
      JOIN pg_class p ON p.oid=i.inhparent
      WHERE p.relname='${PARENT_TABLE}' AND NOT a.attisdropped AND a.attnum>0
        AND a.attname IN (${SAMPLE_IN})) AS partitions_with_dropped_cols
) t;" 2>&1

for _tbl in "${_table_arr[@]}"; do
  echo
  echo "=== relation_gone:${_tbl} (expect exists=false) ==="
  "${PSQL[@]}" -c "
SELECT row_to_json(t) FROM (
  SELECT to_regclass('public.${_tbl}') IS NOT NULL AS exists
) t;" 2>&1
done

echo
echo "=== table_comment ==="
"${PSQL[@]}" -c "
SELECT row_to_json(t) FROM (
  SELECT left(COALESCE(obj_description('${PARENT_TABLE}'::regclass),''),120) AS comment
) t;" 2>&1

echo
echo "=== table_still_writable (last 15min ingest) ==="
"${PSQL[@]}" -c "
SELECT row_to_json(t) FROM (
  SELECT COUNT(*) AS rows_last_15min
  FROM ${PARENT_TABLE}
  WHERE created_at >= now() - interval '15 min'
) t;" 2>&1

echo
echo "=== relation_size_and_vacuum ==="
# Parent pg_total_relation_size is often 0 on partitioned tables; sum children too.
"${PSQL[@]}" -c "
SELECT row_to_json(t) FROM (
  SELECT
    pg_size_pretty(COALESCE((
      SELECT SUM(pg_total_relation_size(i.inhrelid))
      FROM pg_inherits i
      JOIN pg_class p ON p.oid=i.inhparent
      WHERE p.relname='${PARENT_TABLE}'
    ), 0) + pg_total_relation_size('${PARENT_TABLE}'::regclass)) AS total_size_with_partitions,
    pg_size_pretty(pg_total_relation_size('${PARENT_TABLE}'::regclass)) AS parent_only_size,
    (SELECT COUNT(*) FROM ${PARENT_TABLE}) AS total_rows,
    to_char(MAX(last_vacuum) AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI\"Z\"') AS last_vacuum,
    to_char(MAX(last_autovacuum) AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI\"Z\"') AS last_autovacuum
  FROM pg_stat_all_tables
  WHERE relname LIKE '${PARENT_TABLE}%'
) t;" 2>&1
