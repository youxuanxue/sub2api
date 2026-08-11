#!/bin/bash
# Read-only prod QA Phase 2 live health snapshot for ops-daily-diagnostics.
set -u

PSQL=(docker exec -i -e 'PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=5s' tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)
RECEIPT="${QA_MAINTENANCE_RECEIPT:-/var/lib/tokenkey/qa-maintenance-last-run.json}"
BOUNDARY_RECEIPT="${QA_BOUNDARY_RECEIPT:-/var/lib/tokenkey/qa-boundary-last-run.json}"

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

boundary_timer_enabled=false
boundary_timer_active=false
if systemctl is-enabled tokenkey-qa-boundary.timer >/dev/null 2>&1; then
  boundary_timer_enabled=true
fi
if systemctl is-active tokenkey-qa-boundary.timer >/dev/null 2>&1; then
  boundary_timer_active=true
fi
boundary_service_result="$(systemctl show tokenkey-qa-boundary.service -p Result --value 2>/dev/null || true)"
boundary_service_finished="$(systemctl show tokenkey-qa-boundary.service -p ExecMainExitTimestamp --value 2>/dev/null || true)"
if [ -z "${boundary_service_result}" ]; then
  boundary_service_result=unknown
fi
export TK_BOUNDARY_TIMER_ENABLED="${boundary_timer_enabled}"
export TK_BOUNDARY_TIMER_ACTIVE="${boundary_timer_active}"
export TK_BOUNDARY_SERVICE_RESULT="${boundary_service_result}"
export TK_BOUNDARY_SERVICE_FINISHED="${boundary_service_finished}"

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

boundary_payload = {
    "timer_enabled": os.environ.get("TK_BOUNDARY_TIMER_ENABLED") == "true",
    "timer_active": os.environ.get("TK_BOUNDARY_TIMER_ACTIVE") == "true",
    "service_result": os.environ.get("TK_BOUNDARY_SERVICE_RESULT", "unknown"),
    "finished_at": None,
}
archive_finished = os.environ.get("TK_SERVICE_FINISHED", "")
os.environ["TK_SERVICE_FINISHED"] = os.environ.get("TK_BOUNDARY_SERVICE_FINISHED", "")
boundary_payload["finished_at"] = _finished_at()
os.environ["TK_SERVICE_FINISHED"] = archive_finished
print("PHASE2BOUNDARYSYSTEMD " + json.dumps(boundary_payload, ensure_ascii=True, sort_keys=True))
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

if [ -r "${BOUNDARY_RECEIPT}" ]; then
  python3 - "${BOUNDARY_RECEIPT}" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
try:
    payload = json.loads(path.read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError):
    payload = None
print("PHASE2BOUNDARYRECEIPT " + (json.dumps(payload, ensure_ascii=True, sort_keys=True) if payload is not None else "null"))
PY
else
  printf 'PHASE2BOUNDARYRECEIPT null\n'
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
  SELECT last_run_at, last_success_at, last_error_at, last_result
  FROM ops_job_heartbeats
  WHERE job_name = 'qa-boundary'
  LIMIT 1
), db_clock AS (
  SELECT date_trunc('hour', clock_timestamp()) AS current_hour
), required_hours AS (
  SELECT
    d.current_hour + (g.offset * interval '1 hour') AS lower_bound,
    d.current_hour + ((g.offset + 1) * interval '1 hour') AS upper_bound
  FROM db_clock d
  CROSS JOIN generate_series(0, 71) AS g(offset)
), parsed_children AS (
  SELECT
    c.relname,
    pg_get_expr(c.relpartbound, c.oid, true) AS bound_expr,
    substring(pg_get_expr(c.relpartbound, c.oid, true) FROM $$FROM \\('([^']+)'$$)::timestamptz AS lower_bound,
    substring(pg_get_expr(c.relpartbound, c.oid, true) FROM $$TO \\('([^']+)'$$)::timestamptz AS upper_bound
  FROM pg_inherits i
  JOIN pg_class c ON c.oid = i.inhrelid
  WHERE i.inhparent = 'qa_records'::regclass
    AND pg_get_expr(c.relpartbound, c.oid, true) <> 'DEFAULT'
), canonical_coverage AS (
  SELECT count(*)::int AS covered
  FROM required_hours r
  WHERE EXISTS (
    SELECT 1
    FROM parsed_children c
    WHERE c.lower_bound = r.lower_bound
      AND c.upper_bound = r.upper_bound
      AND c.relname = 'qa_records_' || to_char(r.lower_bound AT TIME ZONE 'UTC', 'YYYYMMDD_HH24')
  )
), invalid_children AS (
  SELECT count(*)::int AS attached
  FROM parsed_children c
  WHERE c.lower_bound IS NULL
     OR c.upper_bound IS NULL
     OR c.upper_bound - c.lower_bound <> interval '1 hour'
     OR c.lower_bound <> date_trunc('hour', c.lower_bound)
     OR c.relname <> 'qa_records_' || to_char(c.lower_bound AT TIME ZONE 'UTC', 'YYYYMMDD_HH24')
), cutover_state AS (
  SELECT
    EXISTS (SELECT 1 FROM qa_lifecycle_receipts WHERE phase = 'activate') AS active,
    EXISTS (SELECT 1 FROM qa_lifecycle_receipts WHERE phase = 'finalize') AS finalize_receipt_present,
    EXISTS (
      SELECT 1
      FROM qa_lifecycle_receipts f
      JOIN qa_lifecycle_receipts a ON a.t0_utc = f.t0_utc
      WHERE f.phase = 'finalize' AND a.phase = 'activate'
    ) AS finalized
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
SELECT 'PHASE2BOUNDARYHEARTBEAT ' || COALESCE((SELECT row_to_json(b)::text FROM boundary_heartbeat b), 'null')
UNION ALL
SELECT 'PHASE2QARECORDS ' || row_to_json(v)::text
FROM (
  SELECT
    q.default_rows,
    q.non_default_rows,
    cs.active AS hourly_cutover_active,
    cs.finalize_receipt_present AS hourly_cutover_finalize_receipt_present,
    cs.finalized AS hourly_cutover_finalized,
    l.default_present,
    to_char(d.current_hour AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS future_coverage_start_utc,
    to_char((d.current_hour + interval '72 hours') AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS future_coverage_end_utc,
    72 AS future_coverage_required_hours,
    cc.covered AS future_coverage_canonical_hours,
    GREATEST(0, 72 - cc.covered) AS future_coverage_gap_hours,
    l.current_hour_partition_missing,
    l.expired_partitions_attached,
    ic.attached AS noncanonical_partitions_attached,
    l.hot_cleanup_backlog,
    (l.hot_cleanup_backlog > 0) AS hot_files_cleanup_pending,
    (SELECT row_to_json(b) FROM boundary_heartbeat b) AS boundary_heartbeat
  FROM qa_counts q
  CROSS JOIN lifecycle l
  CROSS JOIN db_clock d
  CROSS JOIN canonical_coverage cc
  CROSS JOIN invalid_children ic
  CROSS JOIN cutover_state cs
) v;
"""
)
PY
