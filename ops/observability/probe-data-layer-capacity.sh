#!/bin/bash
# probe-data-layer-capacity.sh — TokenKey read-only data-layer capacity snapshot.
#
# Runs INSIDE a Stage0 host (prod or edge) via SSM AWS-RunShellScript + base64
# delivery (use ops/observability/run-probe.sh). Pure read-only: psql SELECT +
# host `df`. Emits the LEDGER's size + recent row growth + data-volume free space
# as two tagged, field-named JSON lines for data_layer_capacity_verdict.py:
#
#   PGSTATS {"usage_logs_bytes":..,"dedup_bytes":..,"db_bytes":..,
#            "usage_logs_rows":..,"usage_logs_rows_30d":..,"usage_logs_rows_7d":..}
#   DFSTATS {"df_total_bytes":..,"df_used_bytes":..,"df_avail_bytes":..,"df_mount":".."}
#
# Determinism contract (dev-rules-convention.mdc §"skill / command 确定性基线"):
#   field names embedded next to values (row_to_json); no positional parsing.
# The verdict (green/approaching/trigger) is computed by the Python sibling, not here,
# so the threshold logic stays unit-testable (data_layer_capacity_verdict.py --selftest).
set -u

PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'
DATA_DIR="${TOKENKEY_DATA_DIR:-/var/lib/tokenkey}"

echo "=== docker ps (tokenkey stack) ==="
docker ps --filter name=tokenkey --format '{{.Names}}\t{{.Status}}' 2>/dev/null || true

echo "=== PGSTATS (field names embedded) ==="
# Single JSON object; dedup table may be absent on some hosts -> COALESCE via to_regclass.
$PSQL -tAc "
SELECT 'PGSTATS '||row_to_json(t)::text FROM (
  SELECT
    pg_total_relation_size('usage_logs')                          AS usage_logs_bytes,
    COALESCE(pg_total_relation_size(to_regclass('usage_billing_dedup')), 0) AS dedup_bytes,
    pg_database_size(current_database())                          AS db_bytes,
    (SELECT count(*) FROM usage_logs)                            AS usage_logs_rows,
    (SELECT count(*) FROM usage_logs WHERE created_at >= now() - interval '30 days') AS usage_logs_rows_30d,
    (SELECT count(*) FROM usage_logs WHERE created_at >= now() - interval '7 days')  AS usage_logs_rows_7d
) t;
" 2>&1

echo "=== DFSTATS (data volume) ==="
# Host df of the data dir (where PG data lives); bytes via -B1. Field-named JSON.
df -B1 "${DATA_DIR}" 2>/dev/null | awk 'NR==2 {
  printf "DFSTATS {\"df_total_bytes\":%s,\"df_used_bytes\":%s,\"df_avail_bytes\":%s,\"df_mount\":\"%s\"}\n", $2, $3, $4, $6
}'
