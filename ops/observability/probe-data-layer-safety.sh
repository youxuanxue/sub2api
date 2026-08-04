#!/bin/bash
# Read-only host-side protection probe; verdict logic lives in the Python sibling.
set -u

PSQL=(docker exec -e 'PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=2s' tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)
APP_CONTAINER="${APP_CONTAINER:-auto}"
ACTIVE_COLOR_FILE="${ACTIVE_COLOR_FILE:-/var/lib/tokenkey/active-color}"

container_running() {
  local running
  running="$(docker inspect --format '{{.State.Running}}' "$1" 2>/dev/null)" || return 1
  [[ "$running" == true ]]
}

resolve_app_container() {
  local color candidate
  local -a running_candidates=()

  if [[ "$APP_CONTAINER" != auto ]]; then
    container_running "$APP_CONTAINER" || return 1
    printf '%s\n' "$APP_CONTAINER"
    return 0
  fi

  if [[ -r "$ACTIVE_COLOR_FILE" ]]; then
    color="$(tr -d '[:space:]' < "$ACTIVE_COLOR_FILE" 2>/dev/null || true)" # preflight-allow: swallow - invalid active-color falls through to unambiguous running fallback
    case "$color" in
      blue|green)
        candidate="tokenkey-$color"
        if container_running "$candidate"; then
          printf '%s\n' "$candidate"
          return 0
        fi
        ;;
    esac
  fi

  for candidate in tokenkey tokenkey-blue tokenkey-green; do
    if container_running "$candidate"; then
      running_candidates+=("$candidate")
    fi
  done
  [[ ${#running_candidates[@]} -eq 1 ]] || return 1
  printf '%s\n' "${running_candidates[0]}"
}

if resolved_app_container="$(resolve_app_container)" && app_env="$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$resolved_app_container" 2>/dev/null)"; then
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

"${PSQL[@]}" -c "
WITH names AS (
  SELECT
    to_char(now() AT TIME ZONE 'UTC', 'YYYYMM') AS current_month,
    to_char((now() AT TIME ZONE 'UTC') + interval '1 month', 'YYYYMM') AS future_month,
    to_char(now() AT TIME ZONE 'UTC', 'YYYYMMDD') AS current_day,
    to_char((now() AT TIME ZONE 'UTC') + interval '1 day', 'YYYYMMDD') AS future_day
), named_partitions AS (
  SELECT parent.relname AS parent_name, child.relname AS child_name
  FROM pg_inherits inheritance
  JOIN pg_class parent ON parent.oid = inheritance.inhparent
  JOIN pg_class child ON child.oid = inheritance.inhrelid
), legacy_bounds AS (
  SELECT
    parent.relname AS parent_name,
    substring(
      pg_get_expr(child.relpartbound, child.oid)
      FROM 'TO \(''([^'']+)''\)$'
    )::timestamptz AS upper_exclusive
  FROM pg_inherits inheritance
  JOIN pg_class parent ON parent.oid = inheritance.inhparent
  JOIN pg_class child ON child.oid = inheritance.inhrelid
  WHERE child.relname = parent.relname || '_legacy'
), heartbeat AS (
  SELECT last_success_at, last_error_at
  FROM ops_job_heartbeats
  WHERE job_name = 'ops_partition_maintenance'
)
SELECT 'PARTITIONSTATS ' || row_to_json(v)::text
FROM (
  SELECT
    now() AS server_clock,
    (
      EXISTS (
        SELECT 1 FROM named_partitions
        WHERE parent_name = 'ops_error_logs'
          AND child_name = 'ops_error_logs_' || names.current_month
      )
      OR COALESCE((
        SELECT upper_exclusive >= (
          date_trunc('month', now() AT TIME ZONE 'UTC') + interval '1 month'
        ) AT TIME ZONE 'UTC'
        FROM legacy_bounds WHERE parent_name = 'ops_error_logs'
      ), false)
    ) AS ops_error_logs_current_covered,
    (
      EXISTS (
        SELECT 1 FROM named_partitions
        WHERE parent_name = 'ops_error_logs'
          AND child_name = 'ops_error_logs_' || names.future_month
      )
      OR COALESCE((
        SELECT upper_exclusive >= (
          date_trunc('month', now() AT TIME ZONE 'UTC') + interval '2 month'
        ) AT TIME ZONE 'UTC'
        FROM legacy_bounds WHERE parent_name = 'ops_error_logs'
      ), false)
    ) AS ops_error_logs_future_covered,
    (
      EXISTS (
        SELECT 1 FROM named_partitions
        WHERE parent_name = 'ops_system_logs'
          AND child_name = 'ops_system_logs_' || names.current_month
      )
      OR COALESCE((
        SELECT upper_exclusive >= (
          date_trunc('month', now() AT TIME ZONE 'UTC') + interval '1 month'
        ) AT TIME ZONE 'UTC'
        FROM legacy_bounds WHERE parent_name = 'ops_system_logs'
      ), false)
    ) AS ops_system_logs_current_covered,
    (
      EXISTS (
        SELECT 1 FROM named_partitions
        WHERE parent_name = 'ops_system_logs'
          AND child_name = 'ops_system_logs_' || names.future_month
      )
      OR COALESCE((
        SELECT upper_exclusive >= (
          date_trunc('month', now() AT TIME ZONE 'UTC') + interval '2 month'
        ) AT TIME ZONE 'UTC'
        FROM legacy_bounds WHERE parent_name = 'ops_system_logs'
      ), false)
    ) AS ops_system_logs_future_covered,
    (
      EXISTS (
        SELECT 1 FROM named_partitions
        WHERE parent_name = 'usage_logs'
          AND child_name = 'usage_logs_' || names.current_day
      )
      OR COALESCE((
        SELECT upper_exclusive >= (
          date_trunc('day', now() AT TIME ZONE 'UTC') + interval '1 day'
        ) AT TIME ZONE 'UTC'
        FROM legacy_bounds WHERE parent_name = 'usage_logs'
      ), false)
    ) AS usage_logs_current_covered,
    (
      EXISTS (
        SELECT 1 FROM named_partitions
        WHERE parent_name = 'usage_logs'
          AND child_name = 'usage_logs_' || names.future_day
      )
      OR COALESCE((
        SELECT upper_exclusive >= (
          date_trunc('day', now() AT TIME ZONE 'UTC') + interval '2 day'
        ) AT TIME ZONE 'UTC'
        FROM legacy_bounds WHERE parent_name = 'usage_logs'
      ), false)
    ) AS usage_logs_future_covered,
    (SELECT last_success_at FROM heartbeat) AS partition_maintenance_last_success_at,
    (SELECT last_error_at FROM heartbeat) AS partition_maintenance_last_error_at
  FROM names
) v;
" 2>/dev/null || printf '%s\n' 'PARTITIONSTATS {"probe_ok":false}'

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
