#!/bin/bash
# Read-only prod QA Phase 2 live health snapshot for ops-daily-diagnostics.
set -u

PSQL=(docker exec -i -e 'PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=5s' tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)
RECEIPT="${QA_MAINTENANCE_RECEIPT:-/var/lib/tokenkey/qa-maintenance-last-run.json}"

timer_enabled=false
timer_active=false
if systemctl is-enabled tokenkey-qa-maintenance.timer >/dev/null 2>&1; then
  timer_enabled=true
fi
if systemctl is-active tokenkey-qa-maintenance.timer >/dev/null 2>&1; then
  timer_active=true
fi
service_result="$(systemctl show tokenkey-qa-maintenance.service -p Result --value 2>/dev/null || true)"
service_finished="$(systemctl show tokenkey-qa-maintenance.service -p ExecMainExitTimestamp --value 2>/dev/null || true)"
if [ -z "${service_result}" ]; then
  service_result=unknown
fi
export TK_TIMER_ENABLED="${timer_enabled}"
export TK_TIMER_ACTIVE="${timer_active}"
export TK_SERVICE_RESULT="${service_result}"
export TK_SERVICE_FINISHED="${service_finished}"

RECEIPT_COMP_WINDOW=""
if [ -r "${RECEIPT}" ]; then
  RECEIPT_COMP_WINDOW="$(python3 - "${RECEIPT}" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
try:
    payload = json.loads(path.read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError):
    payload = None
if isinstance(payload, dict):
    compensation = payload.get("compensation")
    if isinstance(compensation, dict):
        window_start = compensation.get("window_start")
        if isinstance(window_start, str) and window_start.strip():
            print(window_start.strip())
PY
)"
fi
export TK_RECEIPT_COMP_WINDOW="${RECEIPT_COMP_WINDOW}"

python3 - <<'PY'
import datetime
import json
import os


def _finished_at():
    value = os.environ.get("TK_SERVICE_FINISHED", "").strip()
    if not value or value.lower() in {"n/a", "none", "[n/a]"}:
        return None
    for fmt in ("%a %Y-%m-%d %H:%M:%S UTC", "%a %Y-%m-%d %H:%M:%S.%f UTC"):
        try:
            parsed = datetime.datetime.strptime(value, fmt)
            return parsed.replace(tzinfo=datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        except ValueError:
            continue
    if value.endswith("Z"):
        iso = value[:-1] + "+00:00"
    else:
        iso = value
    try:
        parsed = datetime.datetime.fromisoformat(iso)
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=datetime.timezone.utc)
    return parsed.astimezone(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


payload = {
    "timer_enabled": os.environ.get("TK_TIMER_ENABLED") == "true",
    "timer_active": os.environ.get("TK_TIMER_ACTIVE") == "true",
    "service_result": os.environ.get("TK_SERVICE_RESULT", "unknown"),
    "finished_at": _finished_at(),
}
print("PHASE2SYSTEMD " + json.dumps(payload, ensure_ascii=True, sort_keys=True))
PY

if [ -r "${RECEIPT}" ]; then
  python3 - "${RECEIPT}" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
try:
    payload = json.loads(path.read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError):
    payload = None
print("PHASE2RECEIPT " + (json.dumps(payload, ensure_ascii=True, sort_keys=True) if payload is not None else "null"))
PY
else
  printf 'PHASE2RECEIPT null\n'
fi

python3 - <<'PY' | "${PSQL[@]}" -f -
import os

window = os.environ.get("TK_RECEIPT_COMP_WINDOW", "").strip().replace("'", "''")
if window:
    comp_target = f"SELECT '{window}'::timestamptz AS window_start"
else:
    comp_target = "SELECT NULL::timestamptz AS window_start"

print(
    f"""
WITH heartbeat AS (
  SELECT last_run_at, last_success_at, last_error_at, last_result
  FROM ops_job_heartbeats
  WHERE job_name = 'qa-maintenance'
  LIMIT 1
), cutover AS (
  SELECT window_start
  FROM qa_archive_shards
  WHERE forward_cutover IS TRUE
  ORDER BY window_start
  LIMIT 1
), latest_normal AS (
  SELECT s.window_start, s.state, s.commit_etag, s.cleanup_eligible,
         (s.restore_verified_at IS NOT NULL) AS restore_verified
  FROM qa_archive_shards s
  CROSS JOIN cutover c
  WHERE s.generation = 0
    AND s.window_start >= c.window_start
  ORDER BY s.window_start DESC
  LIMIT 1
), comp_target AS (
  {comp_target}
), latest_comp AS (
  SELECT s.window_start, s.state, s.commit_etag, s.cleanup_eligible,
         (s.restore_verified_at IS NOT NULL) AS restore_verified,
         s.verification_error_code
  FROM qa_archive_shards s
  CROSS JOIN cutover c
  CROSS JOIN latest_normal n
  CROSS JOIN comp_target t
  WHERE s.generation = 0
    AND s.window_start >= c.window_start
    AND s.window_start < n.window_start
    AND (
      t.window_start IS NOT NULL AND s.window_start = t.window_start
      OR t.window_start IS NULL AND s.state <> 'committed'
    )
  ORDER BY CASE WHEN t.window_start IS NOT NULL THEN 0 ELSE 1 END,
           s.window_start ASC
  LIMIT 1
), terminal AS (
  SELECT COALESCE(
    json_agg(
      json_build_object(
        'window_start', to_char(s.window_start AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
        'verification_error_code', s.verification_error_code
      )
      ORDER BY s.window_start
    ),
    '[]'::json
  ) AS items
  FROM qa_archive_shards s
  CROSS JOIN cutover c
  CROSS JOIN latest_normal n
  WHERE s.generation = 0
    AND s.window_start >= c.window_start
    AND s.window_start < n.window_start
    AND s.state = 'failed'
    AND s.verification_error_code IS NOT NULL
), default_child AS (
  SELECT c.oid
  FROM pg_inherits i
  JOIN pg_class c ON c.oid = i.inhrelid
  WHERE i.inhparent = 'qa_records'::regclass
    AND pg_get_expr(c.relpartbound, c.oid, true) = 'DEFAULT'
), qa_counts AS (
  SELECT
    count(*) FILTER (
      WHERE EXISTS (SELECT 1 FROM default_child d WHERE r.tableoid = d.oid)
    ) AS default_rows,
    count(*) FILTER (
      WHERE NOT EXISTS (SELECT 1 FROM default_child d WHERE r.tableoid = d.oid)
    ) AS non_default_rows
  FROM qa_records r
  CROSS JOIN cutover c
  WHERE r.created_at >= c.window_start
), lifecycle AS (
  SELECT
    (SELECT count(*) FROM default_child) > 0 AS default_present,
    (
      SELECT count(*)
      FROM pg_inherits i
      JOIN pg_class c ON c.oid = i.inhrelid
      WHERE i.inhparent = 'qa_records'::regclass
        AND pg_get_expr(c.relpartbound, c.oid, true) <> 'DEFAULT'
        AND substring(pg_get_expr(c.relpartbound, c.oid, true) FROM $$TO \\('([^']+)'$$)::timestamptz
          <= date_trunc('hour', clock_timestamp()) - interval '24 hours'
    ) AS expired_partitions_attached,
    (
      SELECT count(*)
      FROM qa_archive_shards s
      WHERE s.source_dropped_at IS NOT NULL
        AND s.hot_files_cleaned_at IS NULL
    ) AS hot_cleanup_backlog,
    (
      SELECT count(*) = 0
      FROM pg_inherits i
      JOIN pg_class c ON c.oid = i.inhrelid
      WHERE i.inhparent = 'qa_records'::regclass
        AND pg_get_expr(c.relpartbound, c.oid, true) <> 'DEFAULT'
        AND substring(pg_get_expr(c.relpartbound, c.oid, true) FROM $$FROM \\('([^']+)'$$)::timestamptz
          = date_trunc('hour', clock_timestamp())
    ) AS current_hour_partition_missing
), boundary_heartbeat AS (
  SELECT last_run_at, last_result
  FROM ops_job_heartbeats
  WHERE job_name = 'qa-boundary'
  LIMIT 1
), current_write AS (
  SELECT child.relname AS partition_name
  FROM pg_class child
  WHERE child.oid = (
    SELECT tableoid FROM qa_records ORDER BY id DESC LIMIT 1
  )
)
SELECT 'PHASE2HEARTBEAT ' || COALESCE((SELECT row_to_json(h)::text FROM heartbeat h), 'null')
UNION ALL
SELECT 'PHASE2ARCHIVE ' || row_to_json(v)::text
FROM (
  SELECT
    (SELECT row_to_json(n) FROM (
      SELECT to_char(window_start AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS window_start,
             state, commit_etag, cleanup_eligible, restore_verified
      FROM latest_normal
    ) n) AS normal,
    (SELECT row_to_json(c) FROM (
      SELECT to_char(window_start AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS window_start,
             state, commit_etag, cleanup_eligible, restore_verified,
             verification_error_code
      FROM latest_comp
    ) c) AS compensation,
    (SELECT items FROM terminal) AS terminal_failures_after_cutover
) v
UNION ALL
SELECT 'PHASE2QARECORDS ' || row_to_json(v)::text
FROM (
  SELECT
    CASE
      WHEN q.non_default_rows > 0 AND q.default_rows = 0 THEN 'partitioned'
      WHEN q.non_default_rows > 0 AND q.default_rows > 0 THEN 'mixed'
      ELSE 'default_only'
    END AS partition_owner,
    q.default_rows,
    q.non_default_rows,
    cw.partition_name AS current_write_partition,
    (q.default_rows = 0 AND NOT l.current_hour_partition_missing) AS hourly_cutover_active,
    l.default_present,
    GREATEST(0, 72 - (
      SELECT count(*)::int
      FROM pg_inherits i
      JOIN pg_class c ON c.oid = i.inhrelid
      WHERE i.inhparent = 'qa_records'::regclass
        AND pg_get_expr(c.relpartbound, c.oid, true) <> 'DEFAULT'
        AND substring(pg_get_expr(c.relpartbound, c.oid, true) FROM $$TO \\('([^']+)'$$)::timestamptz
          > date_trunc('hour', clock_timestamp())
        AND substring(pg_get_expr(c.relpartbound, c.oid, true) FROM $$TO \\('([^']+)'$$)::timestamptz
          - substring(pg_get_expr(c.relpartbound, c.oid, true) FROM $$FROM \\('([^']+)'$$)::timestamptz = interval '1 hour'
    )) AS future_coverage_gap_hours,
    l.current_hour_partition_missing,
    l.expired_partitions_attached,
    (l.hot_cleanup_backlog > 0) AS hot_files_cleanup_pending,
    (SELECT row_to_json(b) FROM boundary_heartbeat b) AS boundary_heartbeat
  FROM qa_counts q
  CROSS JOIN lifecycle l
  LEFT JOIN current_write cw ON true
) v;
"""
)
PY
