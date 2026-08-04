#!/usr/bin/env python3
"""Fixed production controller for guarded partition maintenance."""

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
from collections.abc import Callable, Iterable
from typing import Any

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1] / "lib"))
import resolve_app_container  # noqa: E402  (path bootstrap above)


PROD_REGION = "us-east-1"
PROD_STACK = "tokenkey-prod-stage0"
CONFIRMATION = "tokenkey-prod-partition-maintenance-v1"
INSTANCE_RE = re.compile(r"i-[0-9a-f]{17}")
COMMAND_ID_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9-]{7,127}")
POLL_ATTEMPTS = 100
POLL_INTERVAL_SECONDS = 3

# The remote side receives a command string, not a checkout, so it cannot source
# ops/lib/resolve-app-container.sh. It renders the resolver from the same owner
# instead of carrying a hand-written copy: scripts/checks/app-container-resolver.py
# fails the build if the active-color logic is inlined here again.
_RESOLVER_LINES = resolve_app_container.remote_shell_snippet(docker="sudo docker")

_REMOTE_SCRIPT = "\n".join(
    [
        "set -euo pipefail",
        *_RESOLVER_LINES,
        # Ambiguity is refusal, not a positional guess: running the guarded DDL
        # against the wrong half of a blue/green pair is not recoverable by retry.
        'if [ -z "$APP_CONTAINER" ]; then',
        '  echo "partition maintenance refused: running app container is ambiguous" >&2',
        "  exit 40",
        "fi",
        'sudo docker exec --user 1000:1000 "$APP_CONTAINER" /app/sub2api'
        f" --partition-maintenance-once --confirm {CONFIRMATION}",
    ]
)
REMOTE_COMMAND = "sudo bash -c " + shlex.quote(_REMOTE_SCRIPT)


class PartitionMaintenanceError(RuntimeError):
    """Fail-closed controller error."""


AWSRunner = Callable[[list[str]], subprocess.CompletedProcess[str]]


def _canonical_json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=True, separators=(",", ":"), sort_keys=True)


def _default_aws_runner(args: list[str]) -> subprocess.CompletedProcess[str]:
    try:
        return subprocess.run(
            args,
            capture_output=True,
            text=True,
            timeout=30,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise PartitionMaintenanceError("AWS command could not run") from exc


def _aws_json(args: list[str], run_aws: AWSRunner) -> dict[str, Any]:
    completed = run_aws(args)
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout or "AWS command failed").strip()
        raise PartitionMaintenanceError(f"AWS command failed: {detail[:600]}")
    try:
        payload = json.loads(completed.stdout)
    except json.JSONDecodeError as exc:
        raise PartitionMaintenanceError("AWS command did not return full JSON") from exc
    if not isinstance(payload, dict):
        raise PartitionMaintenanceError("AWS command JSON is not an object")
    return payload


def _resolve_instance(run_aws: AWSRunner) -> str:
    payload = _aws_json(
        [
            "aws",
            "cloudformation",
            "describe-stacks",
            "--region",
            PROD_REGION,
            "--stack-name",
            PROD_STACK,
            "--output",
            "json",
        ],
        run_aws,
    )
    stacks = payload.get("Stacks")
    if not isinstance(stacks, list) or len(stacks) != 1 or not isinstance(stacks[0], dict):
        raise PartitionMaintenanceError("production stack response is incomplete")
    stack = stacks[0]
    if stack.get("StackName") != PROD_STACK or not isinstance(stack.get("Outputs"), list):
        raise PartitionMaintenanceError("production stack identity is invalid")
    values = [
        item.get("OutputValue")
        for item in stack["Outputs"]
        if isinstance(item, dict) and item.get("OutputKey") == "InstanceId"
    ]
    if len(values) != 1 or not isinstance(values[0], str) or INSTANCE_RE.fullmatch(values[0]) is None:
        raise PartitionMaintenanceError("production stack has no unique valid InstanceId")
    return values[0]


def _send_once(instance_id: str, run_aws: AWSRunner) -> str:
    payload = _aws_json(
        [
            "aws",
            "ssm",
            "send-command",
            "--region",
            PROD_REGION,
            "--instance-ids",
            instance_id,
            "--document-name",
            "AWS-RunShellScript",
            "--comment",
            "TokenKey guarded partition maintenance",
            "--parameters",
            _canonical_json({"commands": [REMOTE_COMMAND]}),
            "--output",
            "json",
        ],
        run_aws,
    )
    command = payload.get("Command")
    if not isinstance(command, dict):
        raise PartitionMaintenanceError("SSM send-command response is incomplete")
    command_id = command.get("CommandId")
    if not isinstance(command_id, str) or COMMAND_ID_RE.fullmatch(command_id) is None:
        raise PartitionMaintenanceError("SSM send-command returned an invalid CommandId")
    if command.get("DocumentName") != "AWS-RunShellScript":
        raise PartitionMaintenanceError("SSM send-command returned the wrong document")
    if command.get("InstanceIds") != [instance_id]:
        raise PartitionMaintenanceError("SSM send-command returned a different instance")
    return command_id


def _poll_invocation(
    command_id: str,
    instance_id: str,
    run_aws: AWSRunner,
    sleep: Callable[[float], None],
) -> dict[str, Any]:
    last_error: PartitionMaintenanceError | None = None
    for attempt in range(POLL_ATTEMPTS):
        try:
            payload = _aws_json(
                [
                    "aws",
                    "ssm",
                    "get-command-invocation",
                    "--region",
                    PROD_REGION,
                    "--command-id",
                    command_id,
                    "--instance-id",
                    instance_id,
                    "--output",
                    "json",
                ],
                run_aws,
            )
        except PartitionMaintenanceError as exc:
            last_error = exc
            if attempt + 1 < POLL_ATTEMPTS:
                sleep(POLL_INTERVAL_SECONDS)
                continue
            break
        if payload.get("CommandId") != command_id or payload.get("InstanceId") != instance_id:
            raise PartitionMaintenanceError("SSM invocation identity does not match the submitted command")
        status = payload.get("Status")
        if status in {"Pending", "InProgress", "Delayed"}:
            sleep(POLL_INTERVAL_SECONDS)
            continue
        if status != "Success" or payload.get("ResponseCode") != 0:
            detail = str(payload.get("StandardErrorContent") or "").strip()
            raise PartitionMaintenanceError(
                f"SSM partition maintenance failed status={status!r}: {detail[:600]}"
            )
        if not isinstance(payload.get("StandardOutputContent"), str):
            raise PartitionMaintenanceError("SSM invocation success has no stdout")
        return payload
    if last_error is not None:
        raise PartitionMaintenanceError("SSM invocation could not be read") from last_error
    raise PartitionMaintenanceError("SSM partition maintenance did not reach a terminal state")


def _timestamp(value: Any) -> str:
    if not isinstance(value, str):
        raise PartitionMaintenanceError("remote receipt has no completion timestamp")
    try:
        parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise PartitionMaintenanceError("remote receipt completion timestamp is invalid") from exc
    if parsed.tzinfo is None:
        raise PartitionMaintenanceError("remote receipt completion timestamp has no timezone")
    return value


def _validate_remote_receipt(stdout: str) -> dict[str, Any]:
    lines = [line.strip() for line in stdout.splitlines() if line.strip()]
    try:
        payload = json.loads(lines[-1])
    except (IndexError, json.JSONDecodeError) as exc:
        raise PartitionMaintenanceError("remote partition maintenance receipt is invalid") from exc
    if (
        not isinstance(payload, dict)
        or payload.get("receipt_version") != 1
        or payload.get("mode") != "partition_maintenance"
        or payload.get("ok") is not True
        or payload.get("job_name") != "ops_partition_maintenance"
        or payload.get("deletion_authorized") is not False
    ):
        raise PartitionMaintenanceError("remote partition maintenance receipt failed safety validation")
    _timestamp(payload.get("completed_at"))
    tables = payload.get("tables")
    if not isinstance(tables, list) or not tables:
        raise PartitionMaintenanceError("remote partition maintenance receipt has no verified tables")
    names: set[str] = set()
    for item in tables:
        if not isinstance(item, dict):
            raise PartitionMaintenanceError("remote partition maintenance receipt has an invalid table result")
        table = item.get("table")
        count = item.get("range_count")
        if (
            not isinstance(table, str)
            or not table
            or table in names
            or isinstance(count, bool)
            or not isinstance(count, int)
            or count <= 0
        ):
            raise PartitionMaintenanceError("remote partition maintenance receipt has an invalid table result")
        names.add(table)
    return payload


def _atomic_create_json(path: pathlib.Path, value: dict[str, Any]) -> None:
    temporary: pathlib.Path | None = None
    try:
        fd, raw_temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
        temporary = pathlib.Path(raw_temporary)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(_canonical_json(value) + "\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.link(temporary, path)
        directory_fd = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    except FileExistsError as exc:
        raise PartitionMaintenanceError("receipt already exists; refuse to overwrite") from exc
    except OSError as exc:
        raise PartitionMaintenanceError("partition maintenance receipt could not be created") from exc
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def run(
    receipt_path: str | os.PathLike[str],
    confirmation: str,
    *,
    run_aws: AWSRunner = _default_aws_runner,
    sleep: Callable[[float], None] = time.sleep,
) -> dict[str, Any]:
    if confirmation != CONFIRMATION:
        raise PartitionMaintenanceError("partition maintenance confirmation mismatch")
    path = pathlib.Path(receipt_path).expanduser().resolve()
    if path.exists():
        raise PartitionMaintenanceError("receipt already exists; refuse to overwrite")
    path.parent.mkdir(parents=True, exist_ok=True)

    instance_id = _resolve_instance(run_aws)
    command_id = _send_once(instance_id, run_aws)
    invocation = _poll_invocation(command_id, instance_id, run_aws, sleep)
    remote_receipt = _validate_remote_receipt(invocation["StandardOutputContent"])
    receipt = {
        "controller_receipt_version": 1,
        "region": PROD_REGION,
        "stack": PROD_STACK,
        "instance_id": instance_id,
        "command_id": command_id,
        "recorded_at": dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z"),
        "remote_receipt": remote_receipt,
    }
    _atomic_create_json(path, receipt)
    return receipt


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="action", required=True)
    run_parser = commands.add_parser("run")
    run_parser.add_argument("--receipt", required=True)
    run_parser.add_argument("--confirm", required=True)
    return parser


def main(argv: Iterable[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.action != "run":  # pragma: no cover
            raise PartitionMaintenanceError("unsupported action")
        payload = run(args.receipt, args.confirm)
    except PartitionMaintenanceError as exc:
        print(f"partition maintenance refused: {exc}", file=sys.stderr)
        return 2
    print(_canonical_json(payload))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
