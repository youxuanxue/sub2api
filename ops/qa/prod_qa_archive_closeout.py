#!/usr/bin/env python3
"""Run read-only QA archive inspect/verify/restore commands on prod via SSM."""

from __future__ import annotations

import argparse
import base64
import datetime as dt
import gzip
import hashlib
import io
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

PROD_REGION = "us-east-1"
PROD_STACK = "tokenkey-prod-stage0"
RAW_ARCHIVE_STACK = "tokenkey-prod-qa-raw-archive"
HOST_RUNNER = "/usr/local/bin/tokenkey-qa-maintenance.sh"
RESTORE_CONFIRMATION_PREFIX = "tokenkey-prod-qa-archive-restore-v1"
GAP_CONFIRMATION_PREFIX = "tokenkey-prod-qa-gap-decision-v1:"
GAP_PLAN_SCHEMA = "qa-archive-gap-decision-v1"
SHA256_RE = re.compile(r"[0-9a-f]{64}")
READ_COMMANDS = {"inspect", "verify", "repair-plan"}
POLL_ATTEMPTS = 100
POLL_INTERVAL_SECONDS = 3
MAX_GAP_PLAN_BYTES = 8 << 20
MAX_GAP_TRANSPORT_CHARS = 18000


class QAArchiveCloseoutError(RuntimeError):
    pass


def _parse_window(value: str) -> dt.datetime:
    try:
        parsed = dt.datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=dt.timezone.utc)
    except ValueError as exc:
        raise QAArchiveCloseoutError("window must be an exact UTC RFC3339 hour") from exc
    if parsed.minute or parsed.second or parsed.microsecond:
        raise QAArchiveCloseoutError("window must be an exact UTC RFC3339 hour")
    return parsed


def _window_token(prefix: str, window: dt.datetime) -> str:
    return f"{prefix}:{window.strftime('%Y-%m-%dT%H:%M:%SZ')}"


def _validate_restore_output(value: str) -> str:
    path = pathlib.PurePosixPath(value)
    root = pathlib.PurePosixPath("/app/data/qa_archive_restore")
    if (
        not path.is_absolute()
        or ".." in path.parts
        or "." in path.parts
        or path == root
        or path.parent != root
    ):
        raise QAArchiveCloseoutError(
            "restore output must be a child of /app/data/qa_archive_restore/"
        )
    return str(path)


def _aws_json(args: list[str]) -> dict[str, Any]:
    completed = subprocess.run(args, capture_output=True, text=True, check=False)
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout or "aws failed").strip()
        raise QAArchiveCloseoutError(detail[:600])
    try:
        payload = json.loads(completed.stdout)
    except json.JSONDecodeError as exc:
        raise QAArchiveCloseoutError("aws returned invalid JSON") from exc
    if not isinstance(payload, dict):
        raise QAArchiveCloseoutError("aws JSON is not an object")
    return payload


def _resolve_instance() -> str:
    payload = _aws_json([
        "aws", "cloudformation", "describe-stacks", "--region", PROD_REGION,
        "--stack-name", PROD_STACK, "--output", "json",
    ])
    stacks = payload.get("Stacks")
    if not isinstance(stacks, list) or not stacks:
        raise QAArchiveCloseoutError("prod stack missing")
    for item in stacks[0].get("Outputs", []):
        if isinstance(item, dict) and item.get("OutputKey") == "InstanceId":
            value = item.get("OutputValue")
            if isinstance(value, str) and value.startswith("i-"):
                return value
    raise QAArchiveCloseoutError("prod InstanceId missing")


def _resolve_recovery_scope() -> tuple[str, str]:
    payload = _aws_json([
        "aws", "cloudformation", "describe-stacks", "--region", PROD_REGION,
        "--stack-name", RAW_ARCHIVE_STACK, "--output", "json",
    ])
    stacks = payload.get("Stacks")
    if not isinstance(stacks, list) or not stacks:
        raise QAArchiveCloseoutError("raw archive stack missing")
    outputs = {
        item.get("OutputKey"): item.get("OutputValue")
        for item in stacks[0].get("Outputs", [])
        if isinstance(item, dict)
    }
    bucket = outputs.get("QaRawArchiveBucketName")
    role = outputs.get("QaRawArchiveRecoveryRoleArn")
    if not isinstance(bucket, str) or not bucket or not isinstance(role, str) or not role.startswith("arn:aws:iam::"):
        raise QAArchiveCloseoutError("raw archive recovery outputs missing")
    return bucket, role


def _atomic_json(path: pathlib.Path, value: dict[str, Any]) -> None:
    path = path.expanduser().resolve()
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(value, handle, ensure_ascii=True, sort_keys=True, separators=(",", ":"))
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        pathlib.Path(temporary).replace(path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    except Exception:
        pathlib.Path(temporary).unlink(missing_ok=True)
        raise


def _parse_last_json(stdout: str) -> dict[str, Any]:
    lines = [line.strip() for line in stdout.splitlines() if line.strip()]
    if not lines:
        raise QAArchiveCloseoutError("remote command returned no receipt")
    try:
        value = json.loads(lines[-1])
    except json.JSONDecodeError as exc:
        raise QAArchiveCloseoutError("remote receipt is invalid JSON") from exc
    if not isinstance(value, dict):
        raise QAArchiveCloseoutError("remote receipt is not an object")
    return value


def _send_remote(instance_id: str, comment: str, remote: str) -> dict[str, Any]:
    payload = _aws_json([
        "aws", "ssm", "send-command", "--region", PROD_REGION,
        "--instance-ids", instance_id, "--document-name", "AWS-RunShellScript",
        "--comment", comment,
        "--parameters", json.dumps({"commands": [remote]}, separators=(",", ":")),
        "--output", "json",
    ])
    command_id = str(payload.get("Command", {}).get("CommandId", ""))
    if not command_id:
        raise QAArchiveCloseoutError("SSM command id missing")
    return {
        "command_id": command_id,
        "stdout": _poll(command_id, instance_id),
    }


def _validate_gap_plan(value: Any) -> dict[str, Any]:
    if (
        not isinstance(value, dict)
        or value.get("schema_version") != GAP_PLAN_SCHEMA
        or SHA256_RE.fullmatch(str(value.get("plan_hash", ""))) is None
        or not isinstance(value.get("windows"), list)
        or not value["windows"]
        or not isinstance(value.get("region"), str)
        or not value["region"]
        or not isinstance(value.get("bucket"), str)
        or not value["bucket"]
        or not str(value.get("recovery_role_arn", "")).startswith("arn:aws:iam::")
        or not isinstance(value.get("recovery_run_id"), str)
        or not value["recovery_run_id"]
    ):
        raise QAArchiveCloseoutError("gap decision plan failed validation")
    for window in value["windows"]:
        if (
            not isinstance(window, dict)
            or window.get("commit_exists") is not False
            or window.get("source_record_count") != 0
            or not str(window.get("commit_key", "")).startswith("raw/v1/date=")
        ):
            raise QAArchiveCloseoutError("gap decision window failed validation")
    canonical = dict(value)
    canonical["plan_hash"] = ""
    encoded = json.dumps(
        canonical, ensure_ascii=True, sort_keys=True, separators=(",", ":")
    ).encode()
    if hashlib.sha256(encoded).hexdigest() != value["plan_hash"]:
        raise QAArchiveCloseoutError("gap decision canonical hash mismatch")
    return value


def _load_gap_plan(path: pathlib.Path) -> dict[str, Any]:
    try:
        value = json.loads(path.expanduser().resolve().read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise QAArchiveCloseoutError("gap decision plan cannot be read") from exc
    return _validate_gap_plan(value)


def _encode_plan_transport(value: dict[str, Any]) -> str:
    raw = json.dumps(
        value, ensure_ascii=True, sort_keys=True, separators=(",", ":")
    ).encode()
    if len(raw) > MAX_GAP_PLAN_BYTES:
        raise QAArchiveCloseoutError("gap decision plan exceeds decompressed size limit")
    encoded = base64.b64encode(gzip.compress(raw, mtime=0)).decode()
    if len(encoded) > MAX_GAP_TRANSPORT_CHARS:
        raise QAArchiveCloseoutError("gap decision plan exceeds SSM transport limit")
    return encoded


def _decode_plan_transport(encoded: Any, expected_bytes: Any) -> dict[str, Any]:
    if not isinstance(encoded, str) or not encoded or len(encoded) > MAX_GAP_TRANSPORT_CHARS:
        raise QAArchiveCloseoutError("database gap plan transport is invalid")
    try:
        compressed = base64.b64decode(encoded, validate=True)
        with gzip.GzipFile(fileobj=io.BytesIO(compressed)) as handle:
            raw = handle.read(MAX_GAP_PLAN_BYTES + 1)
    except (ValueError, OSError) as exc:
        raise QAArchiveCloseoutError("database gap plan transport is invalid") from exc
    if len(raw) > MAX_GAP_PLAN_BYTES:
        raise QAArchiveCloseoutError("database gap plan exceeds decompressed size limit")
    if not isinstance(expected_bytes, int) or expected_bytes != len(raw):
        raise QAArchiveCloseoutError("database gap plan length mismatch")
    try:
        value = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise QAArchiveCloseoutError("database gap plan is invalid JSON") from exc
    if not isinstance(value, dict):
        raise QAArchiveCloseoutError("database gap plan is not an object")
    return value


def _run_gap_db_plan(instance_id: str) -> dict[str, Any]:
    result = _send_remote(
        instance_id,
        "TokenKey QA gap decision database plan",
        "sudo " + shlex.join([HOST_RUNNER, "--qa-archive", "gap-decision-db-plan"]),
    )
    receipt = _parse_last_json(str(result["stdout"]))
    plan = _decode_plan_transport(
        receipt.get("plan_gzip_base64"), receipt.get("plan_uncompressed_bytes")
    )
    if (
        receipt.get("ok") is not True
        or receipt.get("command") != "gap-decision-db-plan"
        or receipt.get("deletion_authorized") is not False
        or receipt.get("cleanup_eligible") is not False
        or not isinstance(plan, dict)
        or receipt.get("window_count") != len(plan.get("windows", []))
        or plan.get("schema_version") != GAP_PLAN_SCHEMA
        or plan.get("plan_hash") not in {"", None}
        or not isinstance(plan.get("windows"), list)
        or not plan["windows"]
    ):
        raise QAArchiveCloseoutError("database gap plan failed validation")
    return plan


def _run_gap_s3_plan(
    qa_archive_bin: str,
    db_plan: dict[str, Any],
    bucket: str,
    recovery_role_arn: str,
    recovery_run_id: str,
) -> dict[str, Any]:
    binary = pathlib.Path(qa_archive_bin).expanduser().resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        raise QAArchiveCloseoutError("executable target-tag qa-archive binary is required")
    encoded = _encode_plan_transport(db_plan)
    completed = subprocess.run(
        [
            str(binary), "gap-decision-s3-plan", "--db-plan-gzip-base64", encoded,
            "--region", PROD_REGION, "--bucket", bucket,
            "--recovery-role-arn", recovery_role_arn,
            "--recovery-run-id", recovery_run_id,
        ],
        capture_output=True, text=True, check=False,
    )
    if completed.returncode != 0:
        raise QAArchiveCloseoutError((completed.stderr or completed.stdout or "gap S3 plan failed").strip()[:600])
    receipt = _parse_last_json(completed.stdout)
    if (
        receipt.get("ok") is not True
        or receipt.get("command") != "gap-decision-s3-plan"
        or receipt.get("deletion_authorized") is not False
        or receipt.get("cleanup_eligible") is not False
    ):
        raise QAArchiveCloseoutError("S3 gap plan receipt failed validation")
    return _validate_gap_plan(receipt.get("plan"))


def build_gap_decision_plan(
    output_path: pathlib.Path,
    *,
    qa_archive_bin: str,
    recovery_run_id: str,
) -> dict[str, Any]:
    if not recovery_run_id or len(recovery_run_id) > 128 or re.fullmatch(r"[A-Za-z0-9_.-]+", recovery_run_id) is None:
        raise QAArchiveCloseoutError("safe recovery run id is required")
    if output_path.expanduser().exists():
        raise QAArchiveCloseoutError("gap decision plan path already exists")
    instance_id = _resolve_instance()
    bucket, role = _resolve_recovery_scope()
    db_plan = _run_gap_db_plan(instance_id)
    plan = _run_gap_s3_plan(qa_archive_bin, db_plan, bucket, role, recovery_run_id)
    _atomic_json(output_path, plan)
    return plan


def _run_gap_apply(
    instance_id: str,
    plan: dict[str, Any],
    plan_hash: str,
    approved_by: str,
) -> dict[str, Any]:
    encoded = _encode_plan_transport(plan)
    if len(encoded) > MAX_GAP_TRANSPORT_CHARS:
        raise QAArchiveCloseoutError("gap decision plan exceeds SSM transport limit")
    remote = "sudo " + shlex.join([
        HOST_RUNNER, "--qa-archive", "gap-decision-apply",
        "--plan-gzip-base64", encoded,
        "--confirm", GAP_CONFIRMATION_PREFIX + plan_hash,
        "--approved-by", approved_by,
    ])
    result = _send_remote(instance_id, "TokenKey QA approved gap decision apply", remote)
    receipt = _parse_last_json(str(result["stdout"]))
    already_applied = receipt.get("already_applied")
    receipt_approver = receipt.get("approved_by")
    if (
        receipt.get("ok") is not True
        or receipt.get("command") != "gap-decision-apply"
        or receipt.get("plan_hash") != plan_hash
        or not isinstance(already_applied, bool)
        or not isinstance(receipt_approver, str)
        or not receipt_approver.strip()
        or (not already_applied and receipt_approver != approved_by)
        or receipt.get("window_count") != len(plan["windows"])
        or receipt.get("deletion_authorized") is not False
        or receipt.get("cleanup_eligible") is not False
    ):
        raise QAArchiveCloseoutError("gap decision apply receipt failed validation")
    return {"command_id": result["command_id"], "remote_receipt": receipt}


def apply_gap_decision_plan(
    plan_path: pathlib.Path,
    receipt_path: pathlib.Path,
    *,
    confirmation: str,
    approved_by: str,
) -> dict[str, Any]:
    if receipt_path.expanduser().exists():
        raise QAArchiveCloseoutError("gap decision receipt path already exists")
    try:
        raw = json.loads(plan_path.expanduser().resolve().read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise QAArchiveCloseoutError("gap decision plan cannot be read") from exc
    plan_hash = str(raw.get("plan_hash", "")) if isinstance(raw, dict) else ""
    if confirmation != GAP_CONFIRMATION_PREFIX + plan_hash or SHA256_RE.fullmatch(plan_hash) is None:
        raise QAArchiveCloseoutError("gap decision confirmation mismatch")
    if not approved_by or len(approved_by) > 128 or re.fullmatch(r"[A-Za-z0-9_.@ -]+", approved_by) is None:
        raise QAArchiveCloseoutError("safe approved-by identity is required")
    plan = _validate_gap_plan(raw)
    instance_id = _resolve_instance()
    result = _run_gap_apply(instance_id, plan, plan_hash, approved_by)
    payload = {
        "mode": "prod_qa_archive_gap_decision_apply",
        "instance_id": instance_id,
        "plan_hash": plan_hash,
        **result,
        "cleanup_eligible": False,
        "deletion_authorized": False,
    }
    _atomic_json(receipt_path, payload)
    return payload


def _remote_command(command: str, window: dt.datetime, output: str, confirm: str) -> str:
    window_text = window.strftime("%Y-%m-%dT%H:%M:%SZ")
    cli = [HOST_RUNNER, "--qa-archive", command, "--window-start", window_text]
    if command == "restore":
        cli.extend(["--output", output, "--confirm", confirm])
    return "sudo " + " ".join(shlex.quote(item) for item in cli)

def _poll(command_id: str, instance_id: str) -> str:
    for _ in range(POLL_ATTEMPTS):
        payload = _aws_json([
            "aws", "ssm", "get-command-invocation", "--region", PROD_REGION,
            "--command-id", command_id, "--instance-id", instance_id, "--output", "json",
        ])
        status = payload.get("Status")
        if status in {"Pending", "InProgress", "Delayed"}:
            time.sleep(POLL_INTERVAL_SECONDS)
            continue
        if status != "Success" or payload.get("ResponseCode") != 0:
            detail = str(payload.get("StandardErrorContent") or "").strip()
            raise QAArchiveCloseoutError(f"ssm failed status={status!r}: {detail[:600]}")
        stdout = payload.get("StandardOutputContent")
        if not isinstance(stdout, str):
            raise QAArchiveCloseoutError("ssm success without stdout")
        return stdout
    raise QAArchiveCloseoutError("ssm did not finish")


def _validate_receipt(stdout: str, command: str, window: dt.datetime) -> dict[str, Any]:
    lines = [line.strip() for line in stdout.splitlines() if line.strip()]
    if not lines:
        raise QAArchiveCloseoutError("remote command returned no receipt")
    try:
        payload = json.loads(lines[-1])
    except json.JSONDecodeError as exc:
        raise QAArchiveCloseoutError("remote receipt is invalid JSON") from exc
    expected_window = window.strftime("%Y-%m-%dT%H:%M:%SZ")
    if (
        not isinstance(payload, dict)
        or payload.get("ok") is not True
        or payload.get("command") != command
        or payload.get("window_start") != expected_window
        or payload.get("deletion_authorized") is not False
        or payload.get("cleanup_eligible") is not False
    ):
        raise QAArchiveCloseoutError("remote receipt failed safety validation")
    return payload


def run(command: str, window_text: str, *, output: str = "", confirm: str = "") -> dict[str, Any]:
    window = _parse_window(window_text)
    if command not in READ_COMMANDS | {"restore"}:
        raise QAArchiveCloseoutError(f"unsupported command {command!r}")
    if command == "restore":
        expected = _window_token(RESTORE_CONFIRMATION_PREFIX, window)
        output = _validate_restore_output(output)
        if confirm != expected:
            raise QAArchiveCloseoutError("window-bound restore confirmation required")
    instance_id = _resolve_instance()
    remote = _remote_command(command, window, output, confirm)
    payload = _aws_json([
        "aws", "ssm", "send-command", "--region", PROD_REGION,
        "--instance-ids", instance_id, "--document-name", "AWS-RunShellScript",
        "--comment", f"TokenKey QA archive {command}",
        "--parameters", json.dumps({"commands": [remote]}, separators=(",", ":")),
        "--output", "json",
    ])
    command_id = payload["Command"]["CommandId"]
    receipt = _validate_receipt(_poll(command_id, instance_id), command, window)
    return {"instance_id": instance_id, "command_id": command_id, "remote_receipt": receipt}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    for command in sorted(READ_COMMANDS | {"restore"}):
        child = subparsers.add_parser(command)
        child.add_argument("--window-start", required=True)
        child.add_argument("--output", default="")
        child.add_argument("--confirm", default="")
    gap_plan = subparsers.add_parser("gap-plan")
    gap_plan.add_argument("--output", required=True, type=pathlib.Path)
    gap_plan.add_argument("--qa-archive-bin", required=True)
    gap_plan.add_argument("--recovery-run-id", required=True)
    gap_apply = subparsers.add_parser("gap-apply")
    gap_apply.add_argument("--plan", required=True, type=pathlib.Path)
    gap_apply.add_argument("--receipt-output", required=True, type=pathlib.Path)
    gap_apply.add_argument("--confirm", required=True)
    gap_apply.add_argument("--approved-by", required=True)
    args = parser.parse_args()
    try:
        if args.command == "gap-plan":
            result = build_gap_decision_plan(
                args.output,
                qa_archive_bin=args.qa_archive_bin,
                recovery_run_id=args.recovery_run_id,
            )
        elif args.command == "gap-apply":
            result = apply_gap_decision_plan(
                args.plan,
                args.receipt_output,
                confirmation=args.confirm,
                approved_by=args.approved_by,
            )
        else:
            result = run(
                args.command,
                args.window_start,
                output=args.output,
                confirm=args.confirm,
            )
    except QAArchiveCloseoutError as exc:
        print(f"qa archive closeout refused: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(result, ensure_ascii=True, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
