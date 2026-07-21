#!/usr/bin/env bash
# probe-data-layer-retention-inventory.sh — read-only retention inventory.
#
# Runs on a TokenKey Stage0 host through run-probe.sh. It reports a bounded
# exact count for the indexed usage cutoff, planner/catalog estimates for
# other rows, and exact bytes occupied by whole expired PostgreSQL partitions.
# Whole partitions are the only PostgreSQL evidence in this probe that
# supports physical filesystem reclamation.
#
# Tagged output:
#   RETENTIONSTATS  one row per whitelisted table
#   RETENTIONUSAGE  relation metadata for non-partitioned usage logs
#   RETENTIONUSAGE_EXACT  bounded exact usage cutoff count
#   RETENTIONPLAN   planner estimates without reading matching rows
#   RETPARTITION   one row per leaf partition
#   RETBLOB        local QA blob filesystem evidence
#
# This script intentionally contains no write-side SQL or cleanup operation.
set -u -o pipefail

USAGE_RETENTION_DAYS="${USAGE_RETENTION_DAYS:-90}"
OPS_RETENTION_DAYS="${OPS_RETENTION_DAYS:-30}"
QA_RETENTION_DAYS="${QA_RETENTION_DAYS:-2}"
DATA_DIR="${TOKENKEY_DATA_DIR:-/var/lib/tokenkey}"
QA_BLOB_DIR="${TOKENKEY_QA_BLOB_DIR:-$DATA_DIR/app/qa_blobs}"

for value in "$USAGE_RETENTION_DAYS" "$OPS_RETENTION_DAYS" "$QA_RETENTION_DAYS"; do
  if [[ ! "$value" =~ ^[1-9][0-9]*$ ]]; then
    echo "RETENTIONSTATS {\"inventory_probe_ok\":false,\"reason\":\"retention days must be positive integers\"}"
    exit 0
  fi
done

if [[ ! "$DATA_DIR" =~ ^/[A-Za-z0-9._/-]+$ || "$DATA_DIR" == "/" || "$QA_BLOB_DIR" != "$DATA_DIR/app/qa_blobs" ]]; then
  echo 'RETBLOB {"inventory_probe_ok":false,"blob_inventory_ok":false,"reason":"QA blob inventory path is outside the bounded data directory"}'
  exit 0
fi

PGOPTIONS_VALUE="-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=20s"
PSQL=(docker exec -i -e "PGOPTIONS=$PGOPTIONS_VALUE" tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

echo "=== RETENTIONSTATS (catalog estimates + reclaim classification) ==="
if ! "${PSQL[@]}" \
  -v usage_days="$USAGE_RETENTION_DAYS" \
  -v ops_days="$OPS_RETENTION_DAYS" \
  -v qa_days="$QA_RETENTION_DAYS" \
  -tA 2>&1 <<'SQL'
WITH cfg AS (
  SELECT
    clock_timestamp() AS as_of,
    clock_timestamp() - make_interval(days := :'usage_days'::int) AS usage_cutoff,
    clock_timestamp() - make_interval(days := :'ops_days'::int) AS ops_cutoff,
    clock_timestamp() - make_interval(days := :'qa_days'::int) AS qa_cutoff
), objects(table_name, dataset, cutoff, retention_days) AS (
  SELECT 'ops_system_logs', 'ops', ops_cutoff, :'ops_days'::int FROM cfg
  UNION ALL SELECT 'ops_error_logs', 'ops', ops_cutoff, :'ops_days'::int FROM cfg
  UNION ALL SELECT 'qa_records', 'qa', qa_cutoff, :'qa_days'::int FROM cfg
), leaves AS (
  SELECT
    o.table_name,
    o.dataset,
    o.cutoff,
    o.retention_days,
    tree.relid,
    c.relname AS relation_name,
    pg_total_relation_size(tree.relid) AS relation_bytes,
    COALESCE(s.n_live_tup::bigint, 0) AS live_rows,
    pg_get_expr(c.relpartbound, c.oid) AS partition_bound
  FROM objects o
  JOIN LATERAL pg_partition_tree(to_regclass(o.table_name)) tree
    ON tree.isleaf
  JOIN pg_class c ON c.oid = tree.relid
  LEFT JOIN pg_stat_all_tables s ON s.relid = tree.relid
), bounded AS (
  SELECT
    l.*,
    CASE
      WHEN l.partition_bound ~ $$TO \('([^']+)'\)$$
      THEN substring(l.partition_bound FROM $$TO \('([^']+)'\)$$)::timestamptz
      ELSE NULL
    END AS upper_bound
  FROM leaves l
), aggregates AS (
  SELECT
    b.table_name,
    b.dataset,
    b.cutoff,
    bool_or(b.partition_bound IS NOT NULL) AS partitioned,
    sum(b.relation_bytes)::bigint AS relation_bytes,
    sum(b.live_rows)::bigint AS live_rows,
    COALESCE(sum(b.relation_bytes) FILTER (
      WHERE b.upper_bound IS NOT NULL AND b.upper_bound <= b.cutoff
    ), 0)::bigint AS physically_droppable_bytes,
    COALESCE(sum(b.relation_bytes) FILTER (
      WHERE b.partition_bound IS NOT NULL
        AND (b.upper_bound IS NULL OR b.upper_bound > b.cutoff)
    ), 0)::bigint AS straddling_partition_bytes
  FROM bounded b
  GROUP BY b.table_name, b.dataset, b.cutoff
), expired AS (
  SELECT
    'ops_system_logs' AS table_name,
    NULL::bigint AS expired_rows,
    NULL::bigint AS expired_blob_refs
  FROM cfg
  UNION ALL
  SELECT
    'ops_error_logs',
    NULL::bigint,
    NULL::bigint
  FROM cfg
  UNION ALL
  SELECT
    'qa_records',
    NULL::bigint,
    NULL::bigint
  FROM cfg
)
SELECT 'RETENTIONSTATS '||row_to_json(result)::text
FROM (
  SELECT
    true AS inventory_probe_ok,
    a.table_name,
    a.dataset,
    (SELECT as_of FROM cfg) AS as_of,
    a.cutoff,
    a.partitioned,
    a.live_rows,
    e.expired_rows,
    e.expired_blob_refs,
    a.relation_bytes,
    CASE WHEN a.live_rows > 0
      AND e.expired_rows IS NOT NULL
      THEN round(a.relation_bytes::numeric * e.expired_rows / a.live_rows)::bigint
      ELSE NULL END AS expired_logical_bytes_estimate,
    a.physically_droppable_bytes,
    CASE WHEN a.live_rows > 0
      AND e.expired_rows IS NOT NULL
      THEN greatest(
        round(a.relation_bytes::numeric * e.expired_rows / a.live_rows)::bigint
          - a.physically_droppable_bytes,
        0
      )
      ELSE NULL END AS reusable_not_droppable_bytes_estimate,
    a.straddling_partition_bytes,
    'pg_stat/pg_total_relation_size; expired rows are a bounded EXPLAIN estimate below' AS evidence,
    'whole partitions only; estimates do not prove df reclaim' AS space_semantics
  FROM aggregates a
  JOIN expired e USING (table_name)
  ORDER BY a.table_name
) result;
SQL
then
  echo 'RETENTIONSTATS {"inventory_probe_ok":false,"reason":"retention SQL timed out, was blocked, or schema was incomplete"}'
fi

echo "=== RETENTIONUSAGE (non-partitioned usage relation) ==="
if usage_meta=$(docker exec -i -e "PGOPTIONS=$PGOPTIONS_VALUE" tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 -tAc "
SELECT 'RETENTIONUSAGE '||row_to_json(t)::text
FROM (
  SELECT
    true AS inventory_probe_ok,
    'usage_logs' AS table_name,
    pg_total_relation_size('usage_logs')::bigint AS relation_bytes,
    COALESCE((SELECT n_live_tup::bigint FROM pg_stat_user_tables
      WHERE relid = 'usage_logs'::regclass), 0) AS live_rows,
    $USAGE_RETENTION_DAYS::int AS retention_days,
    now() - make_interval(days := $USAGE_RETENTION_DAYS::int) AS cutoff,
    'pg_total_relation_size + pg_stat_user_tables; no row scan' AS evidence,
    'non-partitioned relation: expired bytes require a separate bounded export/delete rehearsal' AS space_semantics
) t;
" 2>/dev/null); then
  printf '%s\n' "$usage_meta"
else
  echo 'RETENTIONUSAGE {"inventory_probe_ok":false,"reason":"usage relation metadata unavailable"}'
fi

echo "=== RETENTIONUSAGE_EXACT (bounded indexed count) ==="
if usage_expired=$(docker exec -i -e "PGOPTIONS=$PGOPTIONS_VALUE" tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 -tAc "
SELECT 'RETENTIONUSAGE_EXACT '||row_to_json(t)::text
FROM (
  SELECT
    true AS inventory_probe_ok,
    count(*)::bigint AS expired_rows,
    pg_total_relation_size('usage_logs')::bigint AS relation_bytes,
    COALESCE((SELECT n_live_tup::bigint FROM pg_stat_user_tables
      WHERE relid = 'usage_logs'::regclass), 0) AS live_rows,
    $USAGE_RETENTION_DAYS::int AS retention_days,
    now() - make_interval(days := $USAGE_RETENTION_DAYS::int) AS cutoff
  FROM usage_logs
  WHERE created_at < now() - make_interval(days := $USAGE_RETENTION_DAYS::int)
) t;
" 2>/dev/null); then
  printf '%s\n' "$usage_expired"
else
  echo 'RETENTIONUSAGE_EXACT {"inventory_probe_ok":false,"reason":"indexed usage cutoff count timed out or was blocked"}'
fi

echo "=== RETENTIONPLAN (planner estimates; no table scan) ==="
emit_plan() {
  local table_name="$1"
  local retention_days="$2"
  local plan
  if plan=$(docker exec -i -e "PGOPTIONS=$PGOPTIONS_VALUE" tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 -tAc "EXPLAIN (FORMAT JSON) SELECT 1 FROM $table_name WHERE created_at < now() - make_interval(days := ${retention_days}::int)" 2>/dev/null); then
    plan=$(printf '%s' "$plan" | tr -d '\n')
    printf 'RETENTIONPLAN {"inventory_probe_ok":true,"table_name":"%s","retention_days":%s,"evidence":"EXPLAIN (FORMAT JSON), no rows read","plan":%s}\n' "$table_name" "$retention_days" "$plan"
  else
    printf 'RETENTIONPLAN {"inventory_probe_ok":false,"table_name":"%s","retention_days":%s,"reason":"planner estimate unavailable"}\n' "$table_name" "$retention_days"
  fi
}
emit_plan usage_logs "$USAGE_RETENTION_DAYS"
emit_plan ops_system_logs "$OPS_RETENTION_DAYS"
emit_plan ops_error_logs "$OPS_RETENTION_DAYS"
emit_plan qa_records "$QA_RETENTION_DAYS"

echo "=== RETPARTITION (partition bounds and physical candidates) ==="
if ! "${PSQL[@]}" \
  -v usage_days="$USAGE_RETENTION_DAYS" \
  -v ops_days="$OPS_RETENTION_DAYS" \
  -v qa_days="$QA_RETENTION_DAYS" \
  -tA 2>&1 <<'SQL'
WITH cfg AS (
  SELECT
    clock_timestamp() - make_interval(days := :'usage_days'::int) AS usage_cutoff,
    clock_timestamp() - make_interval(days := :'ops_days'::int) AS ops_cutoff,
    clock_timestamp() - make_interval(days := :'qa_days'::int) AS qa_cutoff
), objects(table_name, dataset, cutoff, retention_days) AS (
  SELECT 'ops_system_logs', 'ops', ops_cutoff, :'ops_days'::int FROM cfg
  UNION ALL SELECT 'ops_error_logs', 'ops', ops_cutoff, :'ops_days'::int FROM cfg
  UNION ALL SELECT 'qa_records', 'qa', qa_cutoff, :'qa_days'::int FROM cfg
), parts AS (
  SELECT
    o.table_name,
    o.dataset,
    o.cutoff,
    o.retention_days,
    c.relname AS partition_name,
    pg_get_expr(c.relpartbound, c.oid) AS partition_bound,
    pg_total_relation_size(tree.relid)::bigint AS relation_bytes
  FROM objects o
  JOIN LATERAL pg_partition_tree(to_regclass(o.table_name)) tree
    ON tree.isleaf
  JOIN pg_class c ON c.oid = tree.relid
), bounded AS (
  SELECT
    p.*,
    CASE
      WHEN p.partition_bound ~ $$TO \('([^']+)'\)$$
      THEN substring(p.partition_bound FROM $$TO \('([^']+)'\)$$)::timestamptz
      ELSE NULL
    END AS upper_bound
  FROM parts p
)
SELECT 'RETPARTITION '||row_to_json(result)::text
FROM (
  SELECT
    true AS inventory_probe_ok,
    table_name,
    dataset,
    partition_name,
    partition_bound,
    cutoff,
    relation_bytes,
    CASE WHEN upper_bound IS NOT NULL
      THEN upper_bound + make_interval(days := retention_days)
      ELSE NULL
    END AS eligible_for_physical_drop_at,
    (upper_bound IS NOT NULL AND upper_bound <= cutoff) AS fully_expired,
    CASE WHEN upper_bound IS NOT NULL AND upper_bound <= cutoff
      THEN 'physical_drop_candidate'
      ELSE 'straddling_or_hot'
    END AS reclaim_class
  FROM bounded
  ORDER BY table_name, partition_name
) result;
SQL
then
  echo 'RETPARTITION {"inventory_probe_ok":false,"reason":"partition inventory timed out or was blocked"}'
fi

echo "=== RETBLOB (QA local filesystem evidence) ==="
if [[ ! -d "$QA_BLOB_DIR" ]]; then
  echo "RETBLOB {\"inventory_probe_ok\":false,\"blob_inventory_ok\":false,\"qa_blob_dir\":\"$QA_BLOB_DIR\",\"reason\":\"QA blob directory is absent\"}"
  exit 0
fi

QA_CUTOFF=""
if command -v date >/dev/null 2>&1; then
  QA_CUTOFF=$(date -u -d "-${QA_RETENTION_DAYS} days" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || true)
fi
if [[ -z "$QA_CUTOFF" ]]; then
  echo "RETBLOB {\"inventory_probe_ok\":false,\"blob_inventory_ok\":false,\"qa_blob_dir\":\"$QA_BLOB_DIR\",\"reason\":\"GNU date cutoff calculation unavailable\"}"
  exit 0
fi

blob_inventory_failed=0
if ! total_bytes=$(du -s -B1 "$QA_BLOB_DIR" 2>/dev/null | awk 'NR==1 {print $1}'); then
  blob_inventory_failed=1
fi
if ! total_files=$(find "$QA_BLOB_DIR" -type f -print 2>/dev/null | wc -l | tr -d '[:space:]'); then
  blob_inventory_failed=1
fi
if ! expired_bytes=$(find "$QA_BLOB_DIR" -type f -not -newermt "$QA_CUTOFF" -printf '%s\n' 2>/dev/null | awk '{sum += $1} END {printf "%.0f", sum + 0}'); then
  blob_inventory_failed=1
fi
if ! expired_files=$(find "$QA_BLOB_DIR" -type f -not -newermt "$QA_CUTOFF" -print 2>/dev/null | wc -l | tr -d '[:space:]'); then
  blob_inventory_failed=1
fi

if [[ "$blob_inventory_failed" -ne 0 || -z "$total_bytes" || -z "$total_files" || -z "$expired_bytes" || -z "$expired_files" ]]; then
  echo "RETBLOB {\"inventory_probe_ok\":false,\"blob_inventory_ok\":false,\"qa_blob_dir\":\"$QA_BLOB_DIR\",\"reason\":\"filesystem inventory command failed\"}"
  exit 0
fi
printf 'RETBLOB {"inventory_probe_ok":true,"blob_inventory_ok":true,"qa_blob_dir":"%s","cutoff":"%s","total_files":%s,"total_bytes":%s,"files_older_than_cutoff":%s,"bytes_older_than_cutoff":%s,"evidence":"host filesystem mtime + du","space_semantics":"external blob bytes are separate from PostgreSQL relation bytes; S3 bytes are not included"}\n' \
  "$QA_BLOB_DIR" "$QA_CUTOFF" "$total_files" "$total_bytes" "$expired_files" "$expired_bytes"
