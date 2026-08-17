#!/usr/bin/env bash
# Validate one fresh QA maintenance run through the real systemd sandbox.
set -euo pipefail

RECEIPT="${QA_MAINTENANCE_RECEIPT:-/var/lib/tokenkey/qa-maintenance-last-run.json}"
GATE_STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

previous_run_id="$(python3 - "${RECEIPT}" <<'PY'
import json
import pathlib
import sys

try:
    payload = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError):
    payload = {}
run_id = payload.get("run_id") if isinstance(payload, dict) else None
print(run_id if isinstance(run_id, str) else "")
PY
)"
previous_invocation_id="$(systemctl show tokenkey-qa-maintenance.service -p InvocationID --value)"

systemctl start tokenkey-qa-maintenance.service

invocation_id="$(systemctl show tokenkey-qa-maintenance.service -p InvocationID --value)"
service_result="$(systemctl show tokenkey-qa-maintenance.service -p Result --value)"
exec_main_status="$(systemctl show tokenkey-qa-maintenance.service -p ExecMainStatus --value)"
service_started_at="$(systemctl show tokenkey-qa-maintenance.service -p ExecMainStartTimestamp --value)"
service_finished_at="$(systemctl show tokenkey-qa-maintenance.service -p ExecMainExitTimestamp --value)"

heartbeat_json="$(
  docker exec -i \
    -e 'PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=5s' \
    tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 -c \
    "SELECT COALESCE(row_to_json(h)::text, 'null') FROM (SELECT to_char(last_run_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS last_run_at, to_char(last_success_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS last_success_at, last_result FROM ops_job_heartbeats WHERE job_name = 'qa-maintenance' LIMIT 1) h"
)"
activation_count="$(
  docker exec -i \
    -e 'PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=5s' \
    tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 -c \
    "SELECT count(*) FROM qa_lifecycle_receipts WHERE phase = 'single_owner_activate'" |
    tr -d '[:space:]'
)"
if boundary_enabled="$(systemctl is-enabled tokenkey-qa-boundary.timer 2>/dev/null)"; then :; else :; fi
if boundary_active="$(systemctl is-active tokenkey-qa-boundary.timer 2>/dev/null)"; then :; else :; fi

QA_GATE_STARTED_AT="${GATE_STARTED_AT}" \
QA_GATE_PREVIOUS_RUN_ID="${previous_run_id}" \
QA_GATE_PREVIOUS_INVOCATION_ID="${previous_invocation_id}" \
QA_GATE_INVOCATION_ID="${invocation_id}" \
QA_GATE_SERVICE_RESULT="${service_result}" \
QA_GATE_EXEC_MAIN_STATUS="${exec_main_status}" \
QA_GATE_SERVICE_STARTED_AT="${service_started_at}" \
QA_GATE_SERVICE_FINISHED_AT="${service_finished_at}" \
QA_GATE_HEARTBEAT_JSON="${heartbeat_json}" \
QA_GATE_ACTIVATION_COUNT="${activation_count}" \
QA_GATE_BOUNDARY_ENABLED="${boundary_enabled}" \
QA_GATE_BOUNDARY_ACTIVE="${boundary_active}" \
python3 - "${RECEIPT}" <<'PY'
import datetime as dt
import json
import os
import pathlib
import sys

MAX_SKEW = dt.timedelta(minutes=5)


def fail(reason: str) -> None:
    raise SystemExit(f"qa-maintenance-health-gate: {reason}")


def timestamp(value: object) -> dt.datetime:
    if not isinstance(value, str) or not value.strip():
        fail("timestamp_missing")
    text = value.strip()
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        parsed = dt.datetime.fromisoformat(text)
    except ValueError:
        parsed = None
        for fmt in ("%a %Y-%m-%d %H:%M:%S UTC", "%a %Y-%m-%d %H:%M:%S.%f UTC"):
            try:
                parsed = dt.datetime.strptime(text, fmt).replace(tzinfo=dt.timezone.utc)
                break
            except ValueError:
                continue
        if parsed is None:
            fail("timestamp_invalid")
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=dt.timezone.utc)
    return parsed.astimezone(dt.timezone.utc)


def last_result(value: object) -> dict[str, str]:
    if not isinstance(value, str):
        return {}
    result = {}
    for item in value.split():
        key, separator, field = item.partition("=")
        if separator and key:
            result[key] = field
    return result


if os.environ["QA_GATE_SERVICE_RESULT"] != "success":
    fail("systemd_service_failed")
if os.environ["QA_GATE_EXEC_MAIN_STATUS"] != "0":
    fail("systemd_exit_status_nonzero")
invocation_id = os.environ["QA_GATE_INVOCATION_ID"]
if not invocation_id or invocation_id == os.environ["QA_GATE_PREVIOUS_INVOCATION_ID"]:
    fail("systemd_invocation_not_fresh")

try:
    receipt = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError):
    fail("receipt_missing_or_invalid")
if not isinstance(receipt, dict):
    fail("receipt_invalid")
run_id = receipt.get("run_id")
if not isinstance(run_id, str) or not run_id or run_id == os.environ["QA_GATE_PREVIOUS_RUN_ID"]:
    fail("receipt_run_not_fresh")
expected_receipt = {
    "schema_version": "qa-maintenance-runner-v1",
    "trigger": "timer",
    "runner_uid": 1000,
    "runner_gid": 1000,
    "child_exit_code": 0,
    "runner_exit_code": 0,
    "deletion_authorized": False,
}
for key, expected in expected_receipt.items():
    if receipt.get(key) != expected:
        fail(f"receipt_{key}_invalid")
if receipt.get("error_code") not in {None, ""}:
    fail("receipt_error")
normal = receipt.get("normal")
if not isinstance(normal, dict):
    fail("normal_archive_missing")
if normal.get("state") != "committed" or normal.get("restore_verified") is not True:
    fail("normal_archive_not_restore_verified")
if normal.get("cleanup_eligible") is not False:
    fail("normal_archive_cleanup_not_denied")

gate_started = timestamp(os.environ["QA_GATE_STARTED_AT"])
receipt_started = timestamp(receipt.get("started_at"))
receipt_finished = timestamp(receipt.get("finished_at"))
service_started = timestamp(os.environ["QA_GATE_SERVICE_STARTED_AT"])
service_finished = timestamp(os.environ["QA_GATE_SERVICE_FINISHED_AT"])
if receipt_started < gate_started or receipt_started > receipt_finished:
    fail("receipt_time_not_current")
if abs(receipt_started - service_started) > MAX_SKEW:
    fail("receipt_systemd_start_mismatch")
if abs(receipt_finished - service_finished) > MAX_SKEW:
    fail("receipt_systemd_finish_mismatch")

try:
    heartbeat = json.loads(os.environ["QA_GATE_HEARTBEAT_JSON"])
except json.JSONDecodeError:
    fail("heartbeat_invalid")
if not isinstance(heartbeat, dict):
    fail("heartbeat_missing")
facts = last_result(heartbeat.get("last_result"))
if facts.get("status") != "committed":
    fail("heartbeat_not_committed")
if facts.get("run_id") != run_id or facts.get("trigger") != "timer":
    fail("heartbeat_run_mismatch")
if facts.get("deletion_authorized") != "false":
    fail("heartbeat_deletion_not_denied")
if abs(timestamp(heartbeat.get("last_run_at")) - receipt_started) > MAX_SKEW:
    fail("heartbeat_start_mismatch")
if abs(timestamp(heartbeat.get("last_success_at")) - receipt_finished) > MAX_SKEW:
    fail("heartbeat_finish_mismatch")

activation_count = os.environ["QA_GATE_ACTIVATION_COUNT"]
boundary_state = (
    os.environ["QA_GATE_BOUNDARY_ENABLED"],
    os.environ["QA_GATE_BOUNDARY_ACTIVE"],
)
if activation_count == "0":
    if boundary_state != ("enabled", "active"):
        fail("pre_activation_boundary_owner_invalid")
elif activation_count == "1":
    if boundary_state != ("disabled", "inactive"):
        fail("activated_boundary_owner_invalid")
else:
    fail("activation_receipt_count_invalid")

print(json.dumps({
    "ok": True,
    "run_id": run_id,
    "invocation_id": invocation_id,
    "boundary_owner": "maintenance" if activation_count == "1" else "boundary",
}, ensure_ascii=True, sort_keys=True, separators=(",", ":")))
PY
