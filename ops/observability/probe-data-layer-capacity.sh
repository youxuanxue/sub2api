#!/bin/bash
# probe-data-layer-capacity.sh — bounded read-only capacity snapshot.
#
# Runs inside a Stage0 host through the read-only probe transport. Bounded psql
# SELECTs plus host `df` emit catalog size/cardinality, recent row growth, and
# data-volume free space as tagged JSON lines for data_layer_capacity_verdict.py:
#
#   PGSTATS {"catalog_probe_ok":true,"usage_logs_bytes":..,"dedup_bytes":..,"db_bytes":..,
#            "usage_logs_rows":..,"usage_logs_rows_source":"pg_stat_user_tables",..}
#   PGWINDOW {"window_probe_ok":true,"window_span_days":30,
#             "window_tables":{"<tbl>":{"window_bytes":..,"window_days":..,..}}}
#   PGGROWTH {"growth_probe_ok":true,"usage_logs_rows_30d":..,"usage_logs_rows_7d":..}
#
# PGWINDOW is the PRIMARY growth signal; PGSTATS cardinality is only a cross-check.
# Growth was once derived as rows_30d * (usage_logs_bytes / usage_logs_rows), where
# `rows` came from pg_stat_user_tables.n_live_tup -- a CUMULATIVE COUNTER kept in
# the stats file, not in the data files. pgBackRest restores data files, so the
# counter starts at zero: live prod reported 24.7k rows for a 7.1GiB table whose
# catalog reltuples said 7.7M. That inflated average row size ~390x and turned a
# 11-month runway into a fabricated 0.0-month "RDS trigger".
#
# ANALYZE does not fix it either: 81 of 82 leaf partitions carry
# n_mod_since_analyze = 0, because a day partition stops being written the day it
# rolls over, so autoanalyze never fires again. Every restore, host move and
# standby promotion would re-break the gate, and a manual ANALYZE step in the
# migration checklist is exactly the kind of constraint that gets forgotten.
#
# So the gate stops needing a row count. The table is daily RANGE partitioned, so
# the bytes added in the window are the bytes of the partitions whose lower bound
# falls in it -- a CATALOG read, which travels with the data files and is
# therefore immune to restores. The two `rows` terms always cancelled.
#   DFSTATS {"df_total_bytes":..,"df_used_bytes":..,"df_avail_bytes":..,"df_mount":".."}
#
# Determinism contract (dev-rules-convention.mdc §"skill / command 确定性基线"):
#   field names embedded next to values (row_to_json); no positional parsing.
# The verdict (green/approaching/trigger) is computed by the Python sibling, not here,
# so the threshold logic stays unit-testable (data_layer_capacity_verdict.py --selftest).
set -u

# approved anchor: docs/approved/design-capacity-first-data-layer-safety.md (read-only container target only)
# PG/Redis container names differ between Stage0 (tokenkey-postgres/-redis) and
# the Tencent USW layout; ops/lib/resolve-app-container.sh owns the choice and
# run-probe.sh ships it next to this script.
for _tk_lib in "${TK_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || { echo "container resolver not found (ops/lib/resolve-app-container.sh)" >&2; exit 1; }
TK_PG="$(tk_require_postgres_container "${POSTGRES_CONTAINER:-auto}")" || exit 1
# PGOPTIONS is applied while libpq opens the session, before PostgreSQL starts
# the implicit transaction used by psql -c. This makes the SELECT itself read
# only instead of merely changing the default for a later transaction.
PSQL=(docker exec \
  -e "PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=2s" \
  "$TK_PG" psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)
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
    EXISTS (SELECT 1 FROM pg_partitioned_table WHERE partrelid = 'ops_error_logs'::regclass) AS ops_error_partitioned
)
SELECT 'PGSTATS '||row_to_json(t)::text FROM (
  SELECT
    catalog_probe_ok,
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
    pg_database_size(current_database())                          AS db_bytes,
    CASE WHEN usage_partitioned THEN
      COALESCE((SELECT sum(n_live_tup) FROM pg_stat_user_tables WHERE relid IN (
        SELECT relid FROM pg_partition_tree('usage_logs'::regclass) WHERE isleaf
      )), 0)
    ELSE COALESCE((SELECT n_live_tup FROM pg_stat_user_tables WHERE relid = 'usage_logs'::regclass), 0) END AS usage_logs_rows,
    'pg_stat_user_tables'                                        AS usage_logs_rows_source,
    -- reltuples is a pg_class field, so unlike n_live_tup it travels with the
    -- data files and survives a physical restore. Reported purely so the verdict
    -- can corroborate the byte-based growth figure; never an input to it. A
    -- negative reltuples means never-analyzed in PG>=14, which clamps to 0 and
    -- simply drops the cross-check.
    -- (No double quotes in this SQL: the statement is a double-quoted shell
    -- string, so a quote here would terminate it early.)
    CASE WHEN usage_partitioned THEN
      COALESCE((SELECT sum(GREATEST(reltuples, 0))::bigint FROM pg_class WHERE oid IN (
        SELECT relid FROM pg_partition_tree('usage_logs'::regclass) WHERE isleaf
      )), 0)
    ELSE COALESCE((SELECT GREATEST(reltuples, 0)::bigint FROM pg_class
                   WHERE oid = 'usage_logs'::regclass), 0) END   AS usage_logs_reltuples,
    usage_partitioned                                            AS usage_logs_partitioned,
    ops_system_partitioned                                       AS ops_system_logs_partitioned,
    ops_error_partitioned                                        AS ops_error_logs_partitioned
  FROM flags
) t;
" 2>&1; then
  echo 'PGSTATS {"catalog_probe_ok":false,"usage_logs_bytes":null,"usage_logs_rows":null,"usage_logs_reltuples":null}'
fi

echo "=== PGWINDOW (partition-bound byte accounting) ==="
# Catalog-only: no stats dependency, no table scan. Measured 382ms on live prod
# against the existing statement_timeout=2s.
#
# Two details this must get right, both verified against live prod:
#   * Time zone. The container runs TimeZone=Asia/Tokyo and bounds are stored as
#     '2026-10-29 09:00:00+09'. The window is bucketed in UTC, or the whole
#     series shifts by a day.
#   * MINVALUE. usage_logs_legacy is `FROM (MINVALUE)`, which has no parsable
#     lower bound. It counts toward total_bytes, is excluded from the window, and
#     is reported as unbounded_parts so the verdict can see it rather than having
#     it silently dropped.
if ! "${PSQL[@]}" -tAc "
SET application_name = 'tokenkey-capacity-probe-window';
SELECT 'PGWINDOW '||row_to_json(t)::text FROM (
  WITH spec(tbl) AS (VALUES ('usage_logs'),('ops_system_logs'),('ops_error_logs')),
  leaf AS (
    SELECT s.tbl,
           pg_total_relation_size(c.oid) AS bytes,
           GREATEST(c.reltuples, 0) AS rows_est,
           pg_get_expr(c.relpartbound, c.oid, true) AS bound
    FROM spec s
    JOIN pg_partitioned_table pt ON pt.partrelid = to_regclass(s.tbl)
    JOIN pg_partition_tree(to_regclass(s.tbl)) tree ON tree.isleaf
    JOIN pg_class c ON c.oid = tree.relid
  ),
  parsed AS (
    SELECT tbl, bytes, rows_est,
           CASE WHEN bound ~ '^FOR VALUES FROM \(''[0-9]{4}-[0-9]{2}-[0-9]{2}'
                THEN (substring(bound from 'FROM \(''([^'']+)'))::timestamptz
           END AS lo
    FROM leaf
  ),
  bounds AS (SELECT date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS today),
  agg AS (
    SELECT p.tbl,
           count(*)                             AS leaf_parts,
           count(*) FILTER (WHERE p.lo IS NULL) AS unbounded_parts,
           COALESCE(sum(p.bytes), 0)            AS total_bytes,
           COALESCE(sum(p.bytes) FILTER (
             WHERE p.lo >= b.today - interval '30 days' AND p.lo < b.today), 0) AS window_bytes,
           COALESCE(sum(p.rows_est) FILTER (
             WHERE p.lo >= b.today - interval '30 days' AND p.lo < b.today), 0)::bigint AS window_rows_est,
           count(DISTINCT date_trunc('day', p.lo AT TIME ZONE 'UTC')) FILTER (
             WHERE p.lo >= b.today - interval '30 days' AND p.lo < b.today)     AS window_days
    FROM parsed p CROSS JOIN bounds b
    GROUP BY p.tbl
  )
  SELECT true AS window_probe_ok,
         30   AS window_span_days,
         -- Namespaced: the verdict merges every tagged line into one flat
         -- snapshot, so a generic key like 'tables' would collide.
         (SELECT json_object_agg(tbl, json_build_object(
            'leaf_parts', leaf_parts, 'unbounded_parts', unbounded_parts,
            'total_bytes', total_bytes, 'window_bytes', window_bytes,
            'window_rows_est', window_rows_est,
            'window_days', window_days)) FROM agg) AS window_tables
) t;
" 2>&1; then
  echo 'PGWINDOW {"window_probe_ok":false,"window_span_days":null,"window_tables":null}'
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
