#!/usr/bin/env python3
"""Apply the first fixed QA age-retention plan through production SSM."""
from __future__ import annotations

import argparse
import datetime as dt
import hashlib
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
EXPORT_CONFIRM_PREFIX = "tokenkey-prod-qa-export-orphan-apply-v1:"


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


def _validate_export_plan(value: Any) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise StaleCleanupError("QA export orphan plan is missing")
    files = value.get("files")
    if not isinstance(files, list):
        raise StaleCleanupError("QA export orphan file inventory is invalid")
    for item in files:
        if not isinstance(item, dict):
            raise StaleCleanupError("QA export orphan file fact is invalid")
        basename = item.get("basename")
        if (
            not isinstance(basename, str)
            or basename in {"", ".", ".."}
            or "/" in basename
            or isinstance(item.get("size_bytes"), bool)
            or not isinstance(item.get("size_bytes"), int)
            or item["size_bytes"] < 0
            or isinstance(item.get("mtime"), bool)
            or not isinstance(item.get("mtime"), int)
            or item["mtime"] < 0
        ):
            raise StaleCleanupError("QA export orphan file fact is invalid")
    base = {
        "schema_version": value.get("schema_version"),
        "container_dir": value.get("container_dir"),
        "host_dir": value.get("host_dir"),
        "cutoff": value.get("cutoff"),
        "directory_present": value.get("directory_present"),
        "files": files,
        "count": value.get("count"),
        "total_bytes": value.get("total_bytes"),
        "deletion_authorized": value.get("deletion_authorized"),
    }
    if (
        base["schema_version"] != "qa-export-orphan-plan-v1"
        or not isinstance(base["container_dir"], str)
        or not base["container_dir"].startswith("/")
        or not isinstance(base["host_dir"], str)
        or not base["host_dir"].startswith("/")
        or not isinstance(base["cutoff"], str)
        or not isinstance(base["directory_present"], bool)
        or base["count"] != len(files)
        or base["total_bytes"] != sum(item["size_bytes"] for item in files)
        or base["deletion_authorized"] is not False
    ):
        raise StaleCleanupError("QA export orphan plan failed validation")
    digest = hashlib.sha256(
        json.dumps(base, ensure_ascii=True, sort_keys=True, separators=(",", ":")).encode()
    ).hexdigest()
    if (
        value.get("plan_hash") != digest
        or value.get("required_confirmation") != EXPORT_CONFIRM_PREFIX + digest
    ):
        raise StaleCleanupError("QA export orphan plan hash is invalid")
    return value


def _load_plan(
    path: str | os.PathLike[str],
    *,
    allow_stale: bool = False,
    require_activation_ready: bool = True,
) -> dict[str, Any]:
    try:
        value = json.loads(pathlib.Path(path).expanduser().resolve().read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise StaleCleanupError("retention activation plan cannot be read") from exc
    if (
        not isinstance(value, dict)
        or value.get("mode") != "prod_data_retention_activation_plan"
        or value.get("environment") != "prod"
        or value.get("deletion_authorized") is not False
        or INSTANCE_RE.fullmatch(str(value.get("instance_id", ""))) is None
        or not isinstance(value.get("qa"), dict)
        or value["qa"].get("mode") != "prod_qa_age_retention_plan"
        or value["qa"].get("deletion_authorized") is not False
    ):
        raise StaleCleanupError("retention activation plan failed validation")
    if require_activation_ready and value.get("activation_ready") is not True:
        raise StaleCleanupError("retention activation plan is not ready")
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
    export = _validate_export_plan(qa.get("export_tmp"))
    if export.get("cutoff") != cutoff or not isinstance(qa.get("export_jobs"), dict):
        raise StaleCleanupError("QA export diagnostics are not bound to the retention plan")
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


def apply_export_orphans(
    plan_path: str | os.PathLike[str],
    receipt_path: pathlib.Path,
    confirmation: str,
) -> dict[str, Any]:
    if receipt_path.expanduser().exists():
        raise StaleCleanupError("QA export orphan receipt path already exists")
    plan = _load_plan(plan_path, require_activation_ready=False)
    qa = plan["qa"]
    export = qa["export_tmp"]
    if confirmation != export["required_confirmation"]:
        raise StaleCleanupError("QA export orphan confirmation mismatch")
    values = [
        "/usr/local/bin/tokenkey-qa-stale-cleanup.sh",
        "--apply-export-orphans",
        "--cutoff",
        qa["cutoff"],
        "--expected-active-image",
        qa["active_image"],
        "--expected-plan-hash",
        export["plan_hash"],
        "--confirm",
        confirmation,
    ]
    command_id, receipt = _run_remote(plan["instance_id"], shlex.join(values))
    if (
        receipt.get("mode") != "prod_qa_export_orphan_apply"
        or receipt.get("cutoff") != qa["cutoff"]
        or receipt.get("container_dir") != export["container_dir"]
        or receipt.get("host_dir") != export["host_dir"]
        or receipt.get("files") != export["files"]
        or receipt.get("planned_count") != export["count"]
        or receipt.get("planned_bytes") != export["total_bytes"]
        or receipt.get("plan_hash") != export["plan_hash"]
        or receipt.get("deleted_count") != export["count"]
        or receipt.get("deleted_bytes") != export["total_bytes"]
        or receipt.get("deletion_authorized") is not True
        or MARKER_SHA_RE.fullmatch(str(receipt.get("activation_marker_sha256", ""))) is None
    ):
        raise StaleCleanupError("QA export orphan receipt failed validation")
    combined = {**receipt, "instance_id": plan["instance_id"], "command_id": command_id}
    _atomic_json(receipt_path, combined)
    return combined


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "command", choices=("apply-first", "resume-first", "apply-export-orphans")
    )
    parser.add_argument("--activation-plan", required=True)
    parser.add_argument("--receipt", required=True)
    parser.add_argument("--confirm", required=True)
    args = parser.parse_args()
    try:
        if args.command == "apply-export-orphans":
            value = apply_export_orphans(
                args.activation_plan, pathlib.Path(args.receipt), args.confirm
            )
        else:
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
