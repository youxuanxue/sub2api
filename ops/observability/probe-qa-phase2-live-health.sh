#!/bin/bash
# Read-only prod QA Phase 2 live health snapshot for ops-daily-diagnostics.
set -u

PSQL=(docker exec -e 'PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=5s' tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)
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

"${PSQL[@]}" -v receipt_comp_window="${RECEIPT_COMP_WINDOW}" -c "
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
  SELECT NULLIF(trim(:'receipt_comp_window'), '')::timestamptz AS window_start
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
        'window_start', to_char(s.window_start AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),
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
), qa_parts AS (
  SELECT EXISTS (
    SELECT 1
    FROM qa_records r
    CROSS JOIN cutover c
    WHERE r.created_at >= c.window_start
      AND r.tableoid <> 'qa_records_default'::regclass
  ) AS recent_rows_outside_default
)
SELECT 'PHASE2HEARTBEAT ' || COALESCE((SELECT row_to_json(h)::text FROM heartbeat h), 'null')
UNION ALL
SELECT 'PHASE2ARCHIVE ' || row_to_json(v)::text
FROM (
  SELECT
    (SELECT row_to_json(n) FROM (
      SELECT to_char(window_start AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS window_start,
             state, commit_etag, cleanup_eligible, restore_verified
      FROM latest_normal
    ) n) AS normal,
    (SELECT row_to_json(c) FROM (
      SELECT to_char(window_start AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS window_start,
             state, commit_etag, cleanup_eligible, restore_verified,
             verification_error_code
      FROM latest_comp
    ) c) AS compensation,
    (SELECT items FROM terminal) AS terminal_failures_after_cutover
) v
UNION ALL
SELECT 'PHASE2QARECORDS ' || row_to_json(v)::text
FROM (
  SELECT CASE WHEN recent_rows_outside_default THEN 'partitioned' ELSE 'default_only' END AS partition_owner
  FROM qa_parts
) v;
"
