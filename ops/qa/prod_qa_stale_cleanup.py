#!/usr/bin/env python3
"""Apply the first fixed QA age-retention plan through production SSM."""
from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import pathlib
import re
import shlex
import subprocess
import sys
import tempfile
import time
from typing import Any

REGION = "us-east-1"
INSTANCE_RE = re.compile(r"i-[0-9a-f]{17}")
MARKER_SHA_RE = re.compile(r"[0-9a-f]{64}")
CONFIRM_PREFIX = "tokenkey-prod-qa-retention-apply-v1:"


class StaleCleanupError(RuntimeError):
    """Fail-closed first QA cleanup error."""


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


def _load_plan(path: str | os.PathLike[str], *, allow_stale: bool = False) -> dict[str, Any]:
    try:
        value = json.loads(pathlib.Path(path).expanduser().resolve().read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise StaleCleanupError("retention activation plan cannot be read") from exc
    if (
        not isinstance(value, dict)
        or value.get("mode") != "prod_data_retention_activation_plan"
        or value.get("environment") != "prod"
        or value.get("activation_ready") is not True
        or value.get("deletion_authorized") is not False
        or INSTANCE_RE.fullmatch(str(value.get("instance_id", ""))) is None
        or not isinstance(value.get("qa"), dict)
        or value["qa"].get("mode") != "prod_qa_age_retention_plan"
        or value["qa"].get("deletion_authorized") is not False
    ):
        raise StaleCleanupError("retention activation plan failed validation")
    try:
        clock = dt.datetime.fromisoformat(str(value["ops"]["server_clock"]).replace("Z", "+00:00"))
    except (KeyError, TypeError, ValueError) as exc:
        raise StaleCleanupError("retention activation plan has no valid server clock") from exc
    age = dt.datetime.now(dt.timezone.utc) - clock
    if age < dt.timedelta(0):
        raise StaleCleanupError("retention activation plan clock is in the future")
    if age > dt.timedelta(minutes=10) and not allow_stale:
        raise StaleCleanupError("retention activation plan is stale")
    qa = value["qa"]
    for key in ("candidate_rows", "candidate_blob_files", "candidate_dlq_files"):
        if isinstance(qa.get(key), bool) or not isinstance(qa.get(key), int) or qa[key] < 0:
            raise StaleCleanupError(f"QA plan {key} is invalid")
    cutoff = str(qa.get("cutoff", ""))
    if qa.get("required_confirmation") != CONFIRM_PREFIX + cutoff or not qa.get("active_image"):
        raise StaleCleanupError("QA plan binding is invalid")
    return value


def _aws_json(args: list[str]) -> dict[str, Any]:
    proc = subprocess.run(args, capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        raise StaleCleanupError((proc.stderr or proc.stdout or "AWS failed").strip()[:600])
    try:
        value = json.loads(proc.stdout)
    except json.JSONDecodeError as exc:
        raise StaleCleanupError("AWS returned invalid JSON") from exc
    if not isinstance(value, dict):
        raise StaleCleanupError("AWS response is invalid")
    return value


def _run_remote(instance_id: str, command: str) -> tuple[str, dict[str, Any]]:
    sent = _aws_json([
        "aws", "ssm", "send-command", "--region", REGION, "--instance-ids", instance_id,
        "--document-name", "AWS-RunShellScript", "--comment", "TokenKey first QA age retention",
        "--parameters", json.dumps({"commands": ["set -euo pipefail", command]}, separators=(",", ":")),
        "--output", "json",
    ])
    command_id = str(sent.get("Command", {}).get("CommandId", ""))
    if not command_id:
        raise StaleCleanupError("SSM command id is missing")
    for _ in range(200):
        result = _aws_json([
            "aws", "ssm", "get-command-invocation", "--region", REGION,
            "--command-id", command_id, "--instance-id", instance_id, "--output", "json",
        ])
        if result.get("Status") in {"Pending", "InProgress", "Delayed"}:
            time.sleep(3)
            continue
        if result.get("Status") != "Success" or result.get("ResponseCode") != 0:
            raise StaleCleanupError(f"QA cleanup SSM failed: {result.get('Status')} {str(result.get('StandardErrorContent') or '')[:500]}")
        lines = [line for line in str(result.get("StandardOutputContent") or "").splitlines() if line.strip()]
        try:
            receipt = json.loads(lines[-1])
        except (IndexError, json.JSONDecodeError) as exc:
            raise StaleCleanupError("QA cleanup receipt is invalid") from exc
        if not isinstance(receipt, dict):
            raise StaleCleanupError("QA cleanup receipt is invalid")
        return command_id, receipt
    raise StaleCleanupError("QA cleanup SSM timed out")


def apply_first(
    plan_path: str | os.PathLike[str],
    receipt_path: pathlib.Path,
    confirmation: str,
    *,
    resume: bool = False,
) -> dict[str, Any]:
    if receipt_path.expanduser().exists():
        raise StaleCleanupError("QA cleanup receipt path already exists")
    plan = _load_plan(plan_path, allow_stale=resume)
    qa = plan["qa"]
    if confirmation != qa["required_confirmation"]:
        raise StaleCleanupError("first QA cleanup confirmation mismatch")
    values = [
        "/usr/local/bin/tokenkey-qa-stale-cleanup.sh", "--resume-first" if resume else "--apply-first",
        "--cutoff", qa["cutoff"], "--expected-rows", str(qa["candidate_rows"]),
        "--expected-blob-files", str(qa["candidate_blob_files"]),
        "--expected-dlq-files", str(qa["candidate_dlq_files"]),
        "--expected-active-image", qa["active_image"], "--confirm", confirmation,
    ]
    command_id, receipt = _run_remote(plan["instance_id"], shlex.join(values))
    if (
        receipt.get("mode") != "prod_qa_age_retention_first_apply"
        or receipt.get("cutoff") != qa["cutoff"]
        or receipt.get("planned_rows") != qa["candidate_rows"]
        or receipt.get("planned_blob_files") != qa["candidate_blob_files"]
        or receipt.get("planned_dlq_files") != qa["candidate_dlq_files"]
        or not isinstance(receipt.get("deleted_rows_this_attempt"), int)
        or receipt["deleted_rows_this_attempt"] < 0
        or receipt["deleted_rows_this_attempt"] > qa["candidate_rows"]
        or receipt.get("remaining_rows") != 0
        or receipt.get("remaining_blob_files") != 0
        or receipt.get("remaining_dlq_files") != 0
        or MARKER_SHA_RE.fullmatch(str(receipt.get("marker_sha256", ""))) is None
        or receipt.get("deletion_authorized") is not True
        or not isinstance(receipt.get("applied_at"), str)
        or not isinstance(receipt.get("authorization_expires_at"), str)
    ):
        raise StaleCleanupError("QA cleanup receipt failed validation")
    combined = {**receipt, "instance_id": plan["instance_id"], "command_id": command_id}
    _atomic_json(receipt_path, combined)
    return combined


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("apply-first", "resume-first"))
    parser.add_argument("--activation-plan", required=True)
    parser.add_argument("--receipt", required=True)
    parser.add_argument("--confirm", required=True)
    args = parser.parse_args()
    try:
        value = apply_first(
            args.activation_plan,
            pathlib.Path(args.receipt),
            args.confirm,
            resume=args.command == "resume-first",
        )
    except StaleCleanupError as exc:
        print(f"production QA stale cleanup refused: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(value, ensure_ascii=True, sort_keys=True, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
