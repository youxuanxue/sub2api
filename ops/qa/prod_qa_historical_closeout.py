#!/usr/bin/env python3
"""Plan or apply the fixed production QA historical state closeout."""

from __future__ import annotations

import argparse
import json
import pathlib
import shlex
import subprocess
import sys
import time
from typing import Any

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "ops" / "lib"))
import resolve_app_container  # noqa: E402

PROD_REGION = "us-east-1"
PROD_STACK = "tokenkey-prod-stage0"
CONFIRMATION = "tokenkey-prod-qa-historical-closeout-v1"
ADVISORY_LOCK_ID = 0x51414D41


class HistoricalCloseoutError(RuntimeError):
    """Fail-closed historical closeout error."""


def _guard_shell() -> list[str]:
    return [
        'test "$(systemctl is-enabled tokenkey-qa-maintenance.timer)" = disabled',
        'test "$(systemctl is-active tokenkey-qa-maintenance.timer)" = inactive',
        'test "$(systemctl is-enabled tokenkey-qa-stale-cleanup.timer)" = disabled',
        'test "$(systemctl is-active tokenkey-qa-stale-cleanup.timer)" = inactive',
        'runtime_tail=$(docker logs --since 24h "$APP_CONTAINER" 2>&1 | grep -E "cleanup_enabled=(true|false)|cleanup reload after advanced-settings update failed" | tail -1 || true)',
        'printf "%s" "$runtime_tail" | grep -q "cleanup_enabled=false"',
        'test -z "$(docker exec tokenkey-redis redis-cli --raw GET ops:cleanup:leader)"',
    ]


def _sql(apply: bool) -> str:
    update = ""
    if apply:
        update = """
DO $apply$
DECLARE
  changed_04 bigint;
  changed_01 bigint;
BEGIN
  UPDATE qa_archive_shards
  SET state='failed', verification_error_code='missing_evidence',
      last_error='historical recovery declined: records=5382 blob_refs=21528 missing_evidence=96',
      cleanup_eligible=false, updated_at=now()
  WHERE window_start=TIMESTAMPTZ '2026-08-04 04:00:00+00' AND generation=0;
  GET DIAGNOSTICS changed_04 = ROW_COUNT;
  UPDATE qa_archive_shards
  SET state='failed', verification_error_code='commit_mismatch',
      last_error='historical recovery declined: committed=407 source=884 late_identities=477',
      cleanup_eligible=false, updated_at=now()
  WHERE window_start=TIMESTAMPTZ '2026-08-07 01:00:00+00' AND generation=0;
  GET DIAGNOSTICS changed_01 = ROW_COUNT;
  IF changed_04<>1 OR changed_01<>1 THEN
    RAISE EXCEPTION 'historical closeout update count mismatch: 04=% 01=%',changed_04,changed_01;
  END IF;
END $apply$;
"""
    return f"""
BEGIN;
SET LOCAL lock_timeout='100ms';
SET LOCAL statement_timeout='10s';
DO $closeout$
DECLARE
  locked boolean;
  source_04 bigint;
  refs_04 bigint;
  source_01 bigint;
  control_04 bigint;
  control_01 bigint;
BEGIN
  SELECT pg_try_advisory_xact_lock({ADVISORY_LOCK_ID}) INTO locked;
  IF NOT locked THEN RAISE EXCEPTION 'QA maintenance advisory lock is active'; END IF;
  SELECT count(*), COALESCE(sum(
    (CASE WHEN NULLIF(blob_uri,'') IS NOT NULL THEN 1 ELSE 0 END) +
    (CASE WHEN NULLIF(request_blob_uri,'') IS NOT NULL THEN 1 ELSE 0 END) +
    (CASE WHEN NULLIF(response_blob_uri,'') IS NOT NULL THEN 1 ELSE 0 END) +
    (CASE WHEN NULLIF(stream_blob_uri,'') IS NOT NULL THEN 1 ELSE 0 END)
  ),0) INTO source_04,refs_04 FROM qa_records
  WHERE created_at>=TIMESTAMPTZ '2026-08-04 04:00:00+00'
    AND created_at<TIMESTAMPTZ '2026-08-04 05:00:00+00';
  SELECT count(*) INTO source_01 FROM qa_records
  WHERE created_at>=TIMESTAMPTZ '2026-08-07 01:00:00+00'
    AND created_at<TIMESTAMPTZ '2026-08-07 02:00:00+00';
  SELECT count(*) INTO control_04 FROM qa_archive_shards
  WHERE window_start=TIMESTAMPTZ '2026-08-04 04:00:00+00' AND generation=0
    AND record_count=5382 AND blob_ref_count=21528;
  SELECT count(*) INTO control_01 FROM qa_archive_shards
  WHERE window_start=TIMESTAMPTZ '2026-08-07 01:00:00+00' AND generation=0
    AND record_count=407;
  IF source_04<>5382 OR refs_04<>21528 OR control_04<>1 THEN
    RAISE EXCEPTION '04:00 historical counts changed: source=% refs=% control=%',source_04,refs_04,control_04;
  END IF;
  IF source_01<>884 OR control_01<>1 THEN
    RAISE EXCEPTION '01:00 historical counts changed: source=% control=%',source_01,control_01;
  END IF;
END $closeout$;
{update}
SELECT json_build_object(
  'ok',true,
  'mode','prod_qa_historical_closeout_{'apply' if apply else 'plan'}',
  'applied',{'true' if apply else 'false'},
  'archive_complete',false,
  'retention_age_based',true,
  'windows',json_build_array(
    json_build_object('window_start','2026-08-04T04:00:00Z','state',{'\'failed\'' if apply else "(SELECT state FROM qa_archive_shards WHERE window_start=TIMESTAMPTZ '2026-08-04 04:00:00+00' AND generation=0)"},'verification_error_code','missing_evidence','record_count',5382,'blob_ref_count',21528,'missing_evidence_count',96),
    json_build_object('window_start','2026-08-07T01:00:00Z','state',{'\'failed\'' if apply else "(SELECT state FROM qa_archive_shards WHERE window_start=TIMESTAMPTZ '2026-08-07 01:00:00+00' AND generation=0)"},'verification_error_code','commit_mismatch','committed_record_count',407,'source_record_count',884,'late_identity_count',477)
  ),
  'cleanup_eligible',false,
  'deletion_authorized',false
)::text;
COMMIT;
"""


def _remote_script(apply: bool) -> str:
    sql = _sql(apply)
    lines = [
        "set -euo pipefail",
        "cd /var/lib/tokenkey",
        *resolve_app_container.remote_shell_snippet(docker="docker"),
        'test -n "$APP_CONTAINER"',
        *_guard_shell(),
        "docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -q -t -A -P pager=off -v ON_ERROR_STOP=1 "
        + "-c "
        + shlex.quote(sql),
    ]
    return "\n".join(lines)


def _aws_json(args: list[str]) -> dict[str, Any]:
    completed = subprocess.run(args, capture_output=True, text=True, check=False)
    if completed.returncode != 0:
        raise HistoricalCloseoutError((completed.stderr or completed.stdout or "aws failed").strip()[:600])
    try:
        payload = json.loads(completed.stdout)
    except json.JSONDecodeError as exc:
        raise HistoricalCloseoutError("aws returned invalid JSON") from exc
    if not isinstance(payload, dict):
        raise HistoricalCloseoutError("aws response is invalid")
    return payload


def _instance_id() -> str:
    payload = _aws_json([
        "aws", "cloudformation", "describe-stacks", "--region", PROD_REGION,
        "--stack-name", PROD_STACK, "--output", "json",
    ])
    for item in payload.get("Stacks", [{}])[0].get("Outputs", []):
        if item.get("OutputKey") == "InstanceId":
            return str(item.get("OutputValue"))
    raise HistoricalCloseoutError("prod instance is unavailable")


def run(command: str, confirmation: str = "") -> dict[str, Any]:
    apply = command == "apply"
    if command not in {"plan", "apply"}:
        raise HistoricalCloseoutError("unsupported historical closeout command")
    if apply and confirmation != CONFIRMATION:
        raise HistoricalCloseoutError("historical closeout confirmation mismatch")
    instance = _instance_id()
    sent = _aws_json([
        "aws", "ssm", "send-command", "--region", PROD_REGION,
        "--instance-ids", instance, "--document-name", "AWS-RunShellScript",
        "--comment", f"TokenKey QA historical closeout {command}",
        "--parameters", json.dumps({"commands": [_remote_script(apply)]}, separators=(",", ":")),
        "--output", "json",
    ])
    command_id = sent["Command"]["CommandId"]
    for _ in range(100):
        result = _aws_json([
            "aws", "ssm", "get-command-invocation", "--region", PROD_REGION,
            "--command-id", command_id, "--instance-id", instance, "--output", "json",
        ])
        if result.get("Status") in {"Pending", "InProgress", "Delayed"}:
            time.sleep(3)
            continue
        if result.get("Status") != "Success" or result.get("ResponseCode") != 0:
            raise HistoricalCloseoutError(
                f"historical closeout SSM failed: {result.get('Status')} {str(result.get('StandardErrorContent') or '')[:500]}"
            )
        lines = [line for line in str(result.get("StandardOutputContent") or "").splitlines() if line.strip()]
        try:
            receipt = json.loads(lines[-1])
        except (IndexError, json.JSONDecodeError) as exc:
            raise HistoricalCloseoutError("historical closeout receipt is invalid") from exc
        if (
            not isinstance(receipt, dict)
            or receipt.get("ok") is not True
            or receipt.get("applied") is not apply
            or receipt.get("deletion_authorized") is not False
            or receipt.get("cleanup_eligible") is not False
        ):
            raise HistoricalCloseoutError("historical closeout receipt failed validation")
        return {"instance_id": instance, "command_id": command_id, "remote_receipt": receipt}
    raise HistoricalCloseoutError("historical closeout SSM did not finish")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("plan", "apply"))
    parser.add_argument("--confirm", default="")
    args = parser.parse_args()
    try:
        payload = run(args.command, args.confirm)
    except HistoricalCloseoutError as exc:
        print(f"QA historical closeout refused: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(payload, ensure_ascii=True, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
