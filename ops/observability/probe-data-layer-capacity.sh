#!/bin/bash
# probe-data-layer-capacity.sh — TokenKey read-only data-layer capacity snapshot.
#
# Runs INSIDE a Stage0 host (prod or edge) via SSM AWS-RunShellScript + base64
# delivery (use ops/observability/run-probe.sh). Pure read-only: bounded psql
# SELECT + host `df`. Emits catalog size/cardinality, recent row growth, and
# data-volume free space as tagged JSON lines for data_layer_capacity_verdict.py:
#
#   PGSTATS {"catalog_probe_ok":true,"usage_logs_bytes":..,"dedup_bytes":..,"db_bytes":..,
#            "usage_logs_rows":..,"usage_logs_rows_source":"pg_stat_user_tables",..}
#   PGGROWTH {"growth_probe_ok":true,"usage_logs_rows_30d":..,"usage_logs_rows_7d":..}
#   DFSTATS {"df_total_bytes":..,"df_used_bytes":..,"df_avail_bytes":..,"df_mount":".."}
#
# Determinism contract (dev-rules-convention.mdc §"skill / command 确定性基线"):
#   field names embedded next to values (row_to_json); no positional parsing.
# The verdict (green/approaching/trigger) is computed by the Python sibling, not here,
# so the threshold logic stays unit-testable (data_layer_capacity_verdict.py --selftest).
set -u

# PGOPTIONS is applied while libpq opens the session, before PostgreSQL starts
# the implicit transaction used by psql -c. This makes the SELECT itself read
# only instead of merely changing the default for a later transaction.
PSQL=(docker exec \
  -e "PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=2s" \
  tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)
DATA_DIR="${TOKENKEY_DATA_DIR:-/var/lib/tokenkey}"

echo "=== docker ps (tokenkey stack) ==="
docker ps --filter name=tokenkey --format '{{.Names}}\t{{.Status}}' 2>/dev/null || true  # preflight-allow: swallow — diagnostic header only

echo "=== PGSTATS (field names embedded) ==="
# Relation sizes and pg_stat cardinality are catalog reads: do not scan the
# multi-GiB ledger merely to derive an average row size. The recent-growth query
# below is separately bounded and may fail closed without losing this snapshot.
if ! "${PSQL[@]}" -tAc "
SET application_name = 'tokenkey-capacity-probe';
WITH flags AS (
  SELECT
    true                                                           AS catalog_probe_ok,
    EXISTS (SELECT 1 FROM pg_partitioned_table WHERE partrelid = 'usage_logs'::regclass) AS usage_partitioned,
    EXISTS (SELECT 1 FROM pg_partitioned_table WHERE partrelid = 'ops_system_logs'::regclass) AS ops_system_partitioned,
    EXISTS (SELECT 1 FROM pg_partitioned_table WHERE partrelid = 'ops_error_logs'::regclass) AS ops_error_partitioned,
    EXISTS (SELECT 1 FROM pg_partitioned_table WHERE partrelid = to_regclass('qa_records')) AS qa_records_partitioned
)
SELECT 'PGSTATS '||row_to_json(t)::text FROM (
  SELECT
    CASE WHEN usage_partitioned THEN
      COALESCE((SELECT sum(pg_total_relation_size(relid)) FROM pg_partition_tree('usage_logs'::regclass) WHERE isleaf), 0)
    ELSE pg_total_relation_size('usage_logs') END                 AS usage_logs_bytes,
    COALESCE(pg_total_relation_size(to_regclass('usage_billing_dedup')), 0) AS dedup_bytes,
    CASE WHEN ops_system_partitioned THEN
      COALESCE((SELECT sum(pg_total_relation_size(relid)) FROM pg_partition_tree('ops_system_logs'::regclass) WHERE isleaf), 0)
    ELSE COALESCE(pg_total_relation_size(to_regclass('ops_system_logs')), 0) END AS ops_system_logs_bytes,
    CASE WHEN ops_error_partitioned THEN
      COALESCE((SELECT sum(pg_total_relation_size(relid)) FROM pg_partition_tree('ops_error_logs'::regclass) WHERE isleaf), 0)
    ELSE COALESCE(pg_total_relation_size(to_regclass('ops_error_logs')), 0) END AS ops_error_logs_bytes,
    CASE WHEN qa_records_partitioned THEN
      COALESCE((SELECT sum(pg_total_relation_size(relid)) FROM pg_partition_tree(to_regclass('qa_records')) WHERE isleaf), 0)
    ELSE COALESCE(pg_total_relation_size(to_regclass('qa_records')), 0) END AS qa_records_bytes,
    pg_database_size(current_database())                          AS db_bytes,
    CASE WHEN usage_partitioned THEN
      COALESCE((SELECT sum(n_live_tup) FROM pg_stat_user_tables WHERE relid IN (
        SELECT relid FROM pg_partition_tree('usage_logs'::regclass) WHERE isleaf
      )), 0)
    ELSE COALESCE((SELECT n_live_tup FROM pg_stat_user_tables WHERE relid = 'usage_logs'::regclass), 0) END AS usage_logs_rows,
    'pg_stat_user_tables'                                        AS usage_logs_rows_source,
    usage_partitioned                                            AS usage_logs_partitioned,
    ops_system_partitioned                                       AS ops_system_logs_partitioned,
    ops_error_partitioned                                        AS ops_error_logs_partitioned,
    qa_records_partitioned                                       AS qa_records_partitioned
  FROM flags
) t;
" 2>&1; then
  echo 'PGSTATS {"catalog_probe_ok":false,"usage_logs_bytes":null,"usage_logs_rows":null}'
fi

echo "=== PGGROWTH (bounded recent row scan) ==="
# One index-bounded pass over at most 30 days. A timeout/lock conflict is an
# expected inconclusive result, never a reason to keep scanning or report green.
if ! "${PSQL[@]}" -tAc "
SET application_name = 'tokenkey-capacity-probe-growth';
SELECT 'PGGROWTH '||row_to_json(t)::text FROM (
  SELECT
    true AS growth_probe_ok,
    count(*) AS usage_logs_rows_30d,
    count(*) FILTER (WHERE created_at >= now() - interval '7 days') AS usage_logs_rows_7d
  FROM usage_logs
  WHERE created_at >= now() - interval '30 days'
) t;
" 2>&1; then
  echo 'PGGROWTH {"growth_probe_ok":false,"usage_logs_rows_30d":null,"usage_logs_rows_7d":null}'
fi

echo "=== DFSTATS (data volume) ==="
# Host df of the data dir (where PG data lives); bytes via -B1. -P (POSIX) forces
# one line per filesystem so a long device name can't wrap and mis-shift columns.
df -P -B1 "${DATA_DIR}" 2>/dev/null | awk 'NR==2 {
  printf "DFSTATS {\"df_total_bytes\":%s,\"df_used_bytes\":%s,\"df_avail_bytes\":%s,\"df_mount\":\"%s\"}\n", $2, $3, $4, $6
}'
