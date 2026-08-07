#!/usr/bin/env python3
"""Build the read-only production retention activation plan."""
from __future__ import annotations

import argparse
import json
import os
import pathlib
import shlex
import subprocess
import sys
import tempfile
import time
from typing import Any

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import data_layer_archive_cleanup_hold as hold  # noqa: E402

REGION = "us-east-1"


class ActivationError(RuntimeError):
    """Fail-closed retention activation plan error."""


def _remote_plan_script() -> str:
    sql = r"""
WITH bounds AS MATERIALIZED (
  SELECT clock_timestamp() AS server_clock, clock_timestamp()-interval '30 days' AS cutoff
), settings AS (
  SELECT value::jsonb AS value FROM settings WHERE key='ops_advanced_settings'
), telemetry AS (
  SELECT last_success_at,last_error_at,last_result FROM ops_job_heartbeats
  WHERE job_name='telemetry_archive_shadow'
), maintenance AS (
  SELECT last_success_at,last_error_at,last_result FROM ops_job_heartbeats
  WHERE job_name='partition_maintenance'
), history AS (
  SELECT jsonb_object_agg(to_char(window_start AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
    jsonb_build_object('state',state,'verification_error_code',verification_error_code)) AS windows
  FROM qa_archive_shards
  WHERE generation=0 AND window_start IN (
    TIMESTAMPTZ '2026-08-04 04:00:00+00', TIMESTAMPTZ '2026-08-07 01:00:00+00')
)
SELECT json_build_object(
  'server_clock',(SELECT to_char(server_clock AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"') FROM bounds),
  'ops_retention_days',COALESCE((SELECT (value #>> '{data_retention,error_log_retention_days}')::int FROM settings),-1),
  'ops_cutoff',(SELECT to_char(cutoff AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"') FROM bounds),
  'ops_error_log_candidates',(SELECT count(*) FROM ops_error_logs WHERE created_at<(SELECT cutoff FROM bounds)),
  'ops_system_log_candidates',(SELECT count(*) FROM ops_system_logs WHERE created_at<(SELECT cutoff FROM bounds)),
  'historical_windows',COALESCE((SELECT windows FROM history),'{}'::jsonb),
  'forward_archive_window',(SELECT json_build_object(
    'window_start',to_char(window_start AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
    'state',state,'aggregate_record_count',aggregate_record_count,
    'aggregate_blob_missing_count',aggregate_blob_missing_count,
    'verified_at',to_char(verified_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
    'restore_verified_at',to_char(restore_verified_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
    FROM qa_archive_shards
    WHERE generation=0 AND window_start>TIMESTAMPTZ '2026-08-07 01:00:00+00'
      AND state='committed' AND aggregate_blob_missing_count=0
      AND verified_at IS NOT NULL AND restore_verified_at IS NOT NULL
    ORDER BY window_start DESC LIMIT 1),
  'usage_logs_partitioned',EXISTS(SELECT 1 FROM pg_partitioned_table p JOIN pg_class c ON c.oid=p.partrelid WHERE c.relname='usage_logs'),
  'usage_legacy_attached',EXISTS(SELECT 1 FROM pg_inherits WHERE inhparent=to_regclass('public.usage_logs') AND inhrelid=to_regclass('public.usage_logs_legacy')),
  'usage_future_partition_exists',to_regclass('public.usage_logs_' || to_char((clock_timestamp() AT TIME ZONE 'UTC')::date+1,'YYYYMMDD')) IS NOT NULL,
  'usage_partition_maintenance_last_success_at',(SELECT to_char(last_success_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"') FROM maintenance),
  'usage_partition_maintenance_clean',COALESCE((SELECT last_success_at>=clock_timestamp()-interval '26 hours' AND (last_error_at IS NULL OR last_error_at<=last_success_at) FROM maintenance),false),
  'telemetry_last_success_at',(SELECT to_char(last_success_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"') FROM telemetry),
  'telemetry_last_error_at',(SELECT to_char(last_error_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"') FROM telemetry),
  'telemetry_stats',(SELECT CASE WHEN last_result IS NULL THEN NULL ELSE last_result::jsonb END FROM telemetry),
  'telemetry_clean',COALESCE((SELECT
    last_success_at >= clock_timestamp()-interval '3 minutes'
    AND (last_error_at IS NULL OR last_error_at <= last_success_at)
    AND COALESCE((last_result::jsonb->>'dropped')::bigint,-1)=0
    AND COALESCE((last_result::jsonb->>'failed')::bigint,-1)=0
    FROM telemetry),false)
)::text;
""".strip()
    return "\n".join([
        "set -euo pipefail",
        "cd /var/lib/tokenkey",
        'active=$(tr -d "[:space:]" < active-color)',
        'case "$active" in blue|green) APP_CONTAINER="tokenkey-$active";; *) exit 21;; esac',
        'active_image=$(docker inspect "$APP_CONTAINER" --format "{{.Config.Image}}")',
        'maintenance_enabled=$(systemctl is-enabled tokenkey-qa-maintenance.timer 2>/dev/null || true)',
        'maintenance_active=$(systemctl is-active tokenkey-qa-maintenance.timer 2>/dev/null || true)',
        'stale_enabled=$(systemctl is-enabled tokenkey-qa-stale-cleanup.timer 2>/dev/null || true)',
        'stale_active=$(systemctl is-active tokenkey-qa-stale-cleanup.timer 2>/dev/null || true)',
        'qa_plan=$(/usr/local/bin/tokenkey-qa-stale-cleanup.sh --plan)',
        "db=$(docker exec -e PGOPTIONS='-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=30s' tokenkey-postgres psql -U tokenkey -d tokenkey -X -q -t -A -P pager=off -v ON_ERROR_STOP=1 -c " + shlex.quote(sql) + ")",
        "jq -cn --arg image \"$active_image\" --arg me \"$maintenance_enabled\" --arg ma \"$maintenance_active\" --arg se \"$stale_enabled\" --arg sa \"$stale_active\" --argjson qa \"$qa_plan\" --argjson db \"$db\" '{mode:\"prod_data_retention_activation_plan\",environment:\"prod\",active_image:$image,timers:{qa_maintenance:{enabled:$me,active:$ma},qa_stale_cleanup:{enabled:$se,active:$sa}},qa:$qa,ops:$db,deletion_authorized:false}'",
    ])


def _atomic_json(path: pathlib.Path, value: dict[str, Any]) -> None:
    path = path.expanduser().resolve()
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(value, handle, ensure_ascii=True, sort_keys=True, separators=(",", ":"))
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        pathlib.Path(temporary).replace(path)
    except Exception:
        pathlib.Path(temporary).unlink(missing_ok=True)
        raise


def _aws_json(args: list[str]) -> dict[str, Any]:
    proc = subprocess.run(args, capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        raise ActivationError((proc.stderr or proc.stdout or "aws failed").strip()[:600])
    try:
        value = json.loads(proc.stdout)
    except json.JSONDecodeError as exc:
        raise ActivationError("AWS returned invalid JSON") from exc
    if not isinstance(value, dict):
        raise ActivationError("AWS response is invalid")
    return value


def _run_remote(instance_id: str) -> dict[str, Any]:
    sent = _aws_json([
        "aws","ssm","send-command","--region",REGION,"--instance-ids",instance_id,
        "--document-name","AWS-RunShellScript","--comment","TokenKey data retention activation plan",
        "--parameters",json.dumps({"commands":[_remote_plan_script()]},separators=(",",":")),"--output","json",
    ])
    command_id = str(sent.get("Command",{}).get("CommandId", ""))
    if not command_id:
        raise ActivationError("SSM command id is missing")
    for _ in range(60):
        result = _aws_json(["aws","ssm","get-command-invocation","--region",REGION,
            "--command-id",command_id,"--instance-id",instance_id,"--output","json"])
        if result.get("Status") in {"Pending","InProgress","Delayed"}:
            time.sleep(3)
            continue
        if result.get("Status") != "Success" or result.get("ResponseCode") != 0:
            raise ActivationError(f"activation plan SSM failed: {result.get('Status')} {str(result.get('StandardErrorContent') or '')[:400]}")
        lines = [x for x in str(result.get("StandardOutputContent") or "").splitlines() if x.strip()]
        try:
            payload = json.loads(lines[-1])
        except (IndexError,json.JSONDecodeError) as exc:
            raise ActivationError("activation plan output is invalid") from exc
        if not isinstance(payload,dict) or payload.get("deletion_authorized") is not False:
            raise ActivationError("activation plan failed safety validation")
        return {**payload,"instance_id":instance_id,"command_id":command_id}
    raise ActivationError("activation plan SSM timed out")


def _ready(payload: dict[str, Any]) -> tuple[bool,list[str]]:
    reasons: list[str] = []
    timers = payload.get("timers",{})
    if timers.get("qa_maintenance",{}).get("enabled") != "enabled" or timers.get("qa_maintenance",{}).get("active") != "active":
        reasons.append("qa maintenance timer is not active")
    if timers.get("qa_stale_cleanup",{}).get("enabled") != "disabled" or timers.get("qa_stale_cleanup",{}).get("active") != "inactive":
        reasons.append("qa stale cleanup timer must remain disabled before first apply")
    qa = payload.get("qa",{})
    if qa.get("active_image") != payload.get("active_image"):
        reasons.append("QA plan active image does not match the host")
    ops = payload.get("ops",{})
    forward = ops.get("forward_archive_window")
    if not isinstance(forward,dict) or forward.get("state") != "committed":
        reasons.append("no new sealed QA hour has passed verify and restore")
    if ops.get("ops_retention_days") != 30:
        reasons.append("ops retention is not 30 days")
    expected = {
        "2026-08-04T04:00:00Z":("failed","missing_evidence"),
        "2026-08-07T01:00:00Z":("failed","commit_mismatch"),
    }
    windows = ops.get("historical_windows",{})
    for key,(state,code) in expected.items():
        item = windows.get(key,{})
        if item.get("state") != state or item.get("verification_error_code") != code:
            reasons.append(f"historical QA state is not closed: {key}")
    if (
        ops.get("usage_logs_partitioned") is not True
        or ops.get("usage_legacy_attached") is not True
        or ops.get("usage_future_partition_exists") is not True
        or ops.get("usage_partition_maintenance_clean") is not True
    ):
        reasons.append("usage_logs partition cutover or maintenance proof is incomplete")
    stats = ops.get("telemetry_stats")
    if (
        ops.get("telemetry_clean") is not True
        or not isinstance(stats,dict)
        or stats.get("dropped") != 0
        or stats.get("failed") != 0
    ):
        reasons.append("telemetry shadow has no fresh zero-loss heartbeat")
    return not reasons,reasons


def plan(receipt_path: str) -> dict[str, Any]:
    path = pathlib.Path(receipt_path).expanduser()
    if not path.is_file():
        raise ActivationError("cleanup hold receipt does not exist")
    try:
        verified = hold.verify(path)
        receipt = hold._load_receipt(path)
    except hold.HoldControlError as exc:
        raise ActivationError(f"cleanup hold receipt verification failed: {exc}") from exc
    if verified.get("instance_id") != receipt.get("instance_id"):
        raise ActivationError("cleanup hold verification reached a different instance")
    payload = _run_remote(str(receipt["instance_id"]))
    ready,reasons = _ready(payload)
    return {**payload,"hold_started_at":receipt["hold_started_at"],"activation_ready":ready,
        "blocking_reasons":reasons,"deletion_authorized":False}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("plan", choices=("plan",))
    parser.add_argument("--cleanup-hold-receipt", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    try:
        value = plan(args.cleanup_hold_receipt)
        _atomic_json(pathlib.Path(args.output), value)
    except ActivationError as exc:
        print(f"retention activation plan refused: {exc}",file=sys.stderr)
        return 2
    print(json.dumps(value,ensure_ascii=True,sort_keys=True,separators=(",",":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
