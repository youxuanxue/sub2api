#!/bin/bash
# Read-only host-side protection probe; verdict logic lives in the Python sibling.
set -u

PSQL=(docker exec -i -e 'PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=2s' tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)
APP_CONTAINER="${APP_CONTAINER:-auto}"

# Canonical app-container resolver (ops/lib/resolve-app-container.sh) is the sole
# owner of active-color + running-candidate logic. run-probe.sh ships it next to
# this probe; the second path covers running straight from the repo.
_tk_resolver=""
for _cand in "${TK_LIB_DIR:-$(dirname "$0")}/resolve-app-container.sh" \
             "$(dirname "$0")/../lib/resolve-app-container.sh"; do
  if [ -f "$_cand" ]; then _tk_resolver="$_cand"; break; fi
done
if [ -z "$_tk_resolver" ]; then
  echo "canonical app-container resolver not found (ops/lib/resolve-app-container.sh)" >&2
  exit 1
fi
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_resolver"

if resolved_app_container="$(tk_resolve_app_container "$APP_CONTAINER")" && app_env="$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$resolved_app_container" 2>/dev/null)"; then
  telemetry_setting="$(printf '%s\n' "$app_env" | awk -F= '$1 == "TELEMETRY_ARCHIVE_ENABLED" {print substr($0, index($0, "=") + 1); exit}')"
  telemetry_setting="$(printf '%s' "$telemetry_setting" | tr '[:upper:]' '[:lower:]')"
  case "$telemetry_setting" in
    ""|false|0|no|off)
      telemetry_enabled=false
      ;;
    true|1|yes|on)
      telemetry_enabled=true
      ;;
    *)
      telemetry_enabled=invalid
      ;;
  esac
else
  telemetry_enabled=unknown
fi

partition_sql=""
if [ -n "${PARTITION_COVERAGE_SQL:-}" ]; then
  [ -f "$PARTITION_COVERAGE_SQL" ] && partition_sql="$PARTITION_COVERAGE_SQL"
elif [ -f "$(dirname "$0")/data-layer-partition-coverage.sql" ]; then
  partition_sql="$(dirname "$0")/data-layer-partition-coverage.sql"
fi
if [ -n "$partition_sql" ]; then
  "${PSQL[@]}" < "$partition_sql" 2>/dev/null || printf '%s\n' 'PARTITIONSTATS {"probe_ok":false}'
else
  printf '%s\n' 'PARTITIONSTATS {"probe_ok":false}'
fi

case "$telemetry_enabled" in
  false)
    printf '%s\n' 'TELEMETRYSTATS {"probe_ok":true,"enabled":false}'
    ;;
  true)
    "${PSQL[@]}" -c "
WITH heartbeat AS (
  SELECT last_success_at, last_error_at, last_error, last_result
  FROM ops_job_heartbeats
  WHERE job_name = 'telemetry_archive_shadow'
)
SELECT 'TELEMETRYSTATS ' || json_build_object(
  'probe_ok', true,
  'enabled', true,
  'last_success_at', (SELECT last_success_at FROM heartbeat),
  'last_error_at', (SELECT last_error_at FROM heartbeat),
  'last_error', (SELECT last_error FROM heartbeat),
  'last_result', (
    SELECT CASE WHEN last_result IS NULL THEN NULL ELSE last_result::jsonb END
    FROM heartbeat
  )
)::text;
" 2>/dev/null || printf '%s\n' 'TELEMETRYSTATS {"probe_ok":false,"enabled":true}'
    ;;
  *)
    printf '%s\n' 'TELEMETRYSTATS {"probe_ok":false,"enabled":null}'
    ;;
esac

latest_dump="$(find /var/lib/tokenkey/pgdump -maxdepth 1 -type f -name 'tokenkey-*.sql.gz' -printf '%T@\n' 2>/dev/null | sort -nr | head -1)"
if [[ "$latest_dump" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
  latest_dump_iso="$(date -u -d "@${latest_dump%.*}" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || true)" # preflight-allow: swallow — malformed host timestamp becomes explicit null
  printf 'BACKUPSTATS {"latest_pgdump_at":"%s"}\n' "$latest_dump_iso"
else
  printf '%s\n' 'BACKUPSTATS {"latest_pgdump_at":null}'
fi
