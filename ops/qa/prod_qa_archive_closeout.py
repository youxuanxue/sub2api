#!/usr/bin/env python3
"""Run read-only QA archive inspect/verify/restore commands on prod via SSM."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import pathlib
import shlex
import subprocess
import sys
import time
from typing import Any

PROD_REGION = "us-east-1"
PROD_STACK = "tokenkey-prod-stage0"
HOST_RUNNER = "/usr/local/bin/tokenkey-qa-maintenance.sh"
RESTORE_CONFIRMATION_PREFIX = "tokenkey-prod-qa-archive-restore-v1"
READ_COMMANDS = {"inspect", "verify", "repair-plan"}
POLL_ATTEMPTS = 100
POLL_INTERVAL_SECONDS = 3


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
    parser.add_argument("command", choices=sorted(READ_COMMANDS | {"restore"}))
    parser.add_argument("--window-start", required=True)
    parser.add_argument("--output", default="")
    parser.add_argument("--confirm", default="")
    args = parser.parse_args()
    try:
        result = run(args.command, args.window_start, output=args.output, confirm=args.confirm)
    except QAArchiveCloseoutError as exc:
        print(f"qa archive closeout refused: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(result, ensure_ascii=True, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
