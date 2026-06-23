#!/usr/bin/env bash
# probe-admin-aggregation-config.sh — read-only runtime config/env snapshot for
# admin dashboard aggregation jobs.
set -euo pipefail

echo "=== env_dashboard_aggregation ==="
docker exec tokenkey sh -c 'printenv | grep -E "^(DASHBOARD_AGGREGATION_|DASHBOARD_CACHE_)" | sort || true' |
  sed -E 's/(PASSWORD|TOKEN|SECRET|KEY)=.*/\1=<redacted>/'

echo "=== config_files ==="
docker exec tokenkey sh -c '
for f in /app/config.yaml /app/config.yml /app/config/config.yaml /app/config/config.yml; do
  [ -f "$f" ] && printf "%s\n" "$f"
done
' || true

echo "=== aggregation_tables ==="
PSQL="${PSQL:-docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t}"
$PSQL -c "SELECT row_to_json(t) FROM (
  SELECT
    now() AT TIME ZONE 'UTC' AS now_utc,
    (SELECT last_aggregated_at FROM usage_dashboard_aggregation_watermark WHERE id = 1) AS aggregation_watermark,
    (SELECT COUNT(*) FROM usage_dashboard_hourly) AS hourly_rows,
    (SELECT MIN(bucket_start) FROM usage_dashboard_hourly) AS hourly_min,
    (SELECT MAX(bucket_start) FROM usage_dashboard_hourly) AS hourly_max,
    (SELECT COUNT(*) FROM usage_dashboard_daily) AS daily_rows,
    (SELECT MIN(bucket_date) FROM usage_dashboard_daily) AS daily_min,
    (SELECT MAX(bucket_date) FROM usage_dashboard_daily) AS daily_max,
    (SELECT COUNT(*) FROM usage_dashboard_group_daily) AS group_daily_rows,
    EXISTS(SELECT 1 FROM usage_dashboard_group_daily WHERE group_id = 0 AND bucket_date = DATE '1970-01-01') AS group_daily_backfilled,
    EXISTS(SELECT 1 FROM usage_dashboard_group_daily WHERE group_id = 0 AND bucket_date = DATE '1970-01-02') AS group_daily_metrics_backfilled,
    EXISTS(SELECT 1 FROM usage_dashboard_model_daily WHERE bucket_date = DATE '1970-01-01' AND model = '__tk_model_daily_backfill_marker__') AS model_daily_backfilled
) t;"
