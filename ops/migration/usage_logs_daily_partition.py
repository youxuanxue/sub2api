#!/usr/bin/env python3
"""Operator CLI for explicit Fleet usage_logs daily partition cutovers."""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile
from collections.abc import Iterable
from typing import Any

import usage_logs_daily_partition_remote as remote


HERE = pathlib.Path(__file__).resolve().parent
REPO = HERE.parents[1]
RUN_PROBE = REPO / "ops" / "observability" / "run-probe.sh"
REMOTE = HERE / "usage_logs_daily_partition_remote.py"
REMOTE_WRAPPER = HERE / "usage_logs_daily_partition_remote.sh"
INSTANCE_RE = re.compile(r"(?:i|mi)-[0-9a-f]{17}")
RESOLVED_INSTANCE_RE = re.compile(
    r"\[run-probe\] resolved region=\S+ instance_id=((?:i|mi)-[0-9a-f]{17})"
)


class UsagePartitionControlError(RuntimeError):
    """Fail-closed operator controller error."""


def _canonical_json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=True, separators=(",", ":"), sort_keys=True)


def _atomic_json(path: pathlib.Path, value: dict[str, Any]) -> None:
    path = path.expanduser().resolve()
    if path.exists():
        raise UsagePartitionControlError("receipt already exists; refuse to overwrite")
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(_canonical_json(value) + "\n")
            handle.flush()
            os.fsync(handle.fileno())
        pathlib.Path(temporary).replace(path)
    except Exception:
        pathlib.Path(temporary).unlink(missing_ok=True)
        raise


def _run_remote(
    target: str,
    command: str,
    arguments: list[str],
    *,
    timeout_seconds: int,
    expected_instance_id: str | None = None,
) -> dict[str, Any]:
    target = remote._target(target)
    command_line = [
        "bash",
        str(RUN_PROBE),
        "--target",
        target,
        "--script",
        str(REMOTE_WRAPPER),
        "--with",
        str(REMOTE),
        "--timeout-seconds",
        str(timeout_seconds),
    ]
    if expected_instance_id is not None:
        if INSTANCE_RE.fullmatch(expected_instance_id) is None:
            raise UsagePartitionControlError("expected instance id is invalid")
        command_line.extend(["--expected-instance-id", expected_instance_id])
    command_line.extend(
        [
            "--env",
            f"REMOTE_COMMAND={command}",
            "--env",
            f"REMOTE_TARGET={target}",
            "--env",
            f"REMOTE_ARGS_JSON={_canonical_json(arguments)}",
        ]
    )
    try:
        completed = subprocess.run(
            command_line,
            capture_output=True,
            text=True,
            timeout=timeout_seconds + 30,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise UsagePartitionControlError("usage partition command could not run") from exc
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout or "probe failed").strip()
        raise UsagePartitionControlError(f"usage partition command failed: {detail[:600]}")
    instance_match = RESOLVED_INSTANCE_RE.search(completed.stderr)
    if not instance_match:
        raise UsagePartitionControlError("usage partition command did not prove its instance")
    try:
        payload = json.loads([line for line in completed.stdout.splitlines() if line.strip()][-1])
    except (IndexError, json.JSONDecodeError) as exc:
        raise UsagePartitionControlError("usage partition receipt is invalid") from exc
    if (
        not isinstance(payload, dict)
        or payload.get("target") != target
        or payload.get("deletion_authorized") is not False
    ):
        raise UsagePartitionControlError("usage partition receipt failed safety validation")
    return {**payload, "instance_id": instance_match.group(1)}


def _load_prepare_receipt(
    path: str | os.PathLike[str], target: str
) -> dict[str, Any]:
    target = remote._target(target)
    try:
        value = json.loads(pathlib.Path(path).expanduser().resolve().read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise UsagePartitionControlError("usage partition prepare receipt cannot be read") from exc
    if (
        not isinstance(value, dict)
        or value.get("mode") != "usage_logs_daily_partition_prepare"
        or value.get("target") != target
        or value.get("bound_validated") is not True
        or value.get("source_rows_copied") is not False
        or value.get("deletion_authorized") is not False
        or isinstance(value.get("row_count_before"), bool)
        or not isinstance(value.get("row_count_before"), int)
        or value.get("row_count_before") < 0
        or INSTANCE_RE.fullmatch(str(value.get("instance_id", ""))) is None
        or value.get("required_cutover_confirmation")
        != remote.cutover_confirmation_prefix(target)
        + str(value.get("legacy_upper_exclusive"))
    ):
        raise UsagePartitionControlError("usage partition prepare receipt failed validation")
    remote._upper(str(value["legacy_upper_exclusive"]))
    return value


def prepare(
    target: str, receipt_path: str | os.PathLike[str], confirmation: str
) -> dict[str, Any]:
    target = remote._target(target)
    payload = _run_remote(
        target, "prepare", ["--confirm", confirmation], timeout_seconds=1200
    )
    if payload.get("mode") != "usage_logs_daily_partition_prepare":
        raise UsagePartitionControlError("remote prepare returned the wrong receipt mode")
    _atomic_json(pathlib.Path(receipt_path), payload)
    return payload


def abort(
    target: str,
    receipt_path: str | os.PathLike[str],
    upper: str,
    confirmation: str,
) -> dict[str, Any]:
    target = remote._target(target)
    upper = remote._upper(upper)
    if confirmation != remote.abort_confirmation_prefix(target) + upper:
        raise UsagePartitionControlError(
            "abort confirmation must exactly match the partition upper bound"
        )
    payload = _run_remote(
        target,
        "abort",
        [
            "--legacy-upper-exclusive",
            upper,
            "--confirm",
            confirmation,
        ],
        timeout_seconds=120,
    )
    if (
        payload.get("mode") != "usage_logs_daily_partition_abort"
        or payload.get("legacy_upper_exclusive") != upper
        or payload.get("bound_removed") is not True
        or payload.get("source_rows_copied") is not False
    ):
        raise UsagePartitionControlError("remote abort returned incomplete proof")
    _atomic_json(pathlib.Path(receipt_path), payload)
    return payload


def _validate_verification(
    verification: object, minimum_legacy_row_count: int
) -> None:
    if (
        not isinstance(verification, dict)
        or verification.get("partitioned") is not True
        or verification.get("legacy_attached") is not True
        or verification.get("daily_partitions_attached") is not True
        or verification.get("no_parent_global_unique") is not True
        or verification.get("no_incoming_legacy_fk") is not True
        or verification.get("constraints_preserved") is not True
        or isinstance(verification.get("legacy_row_count"), bool)
        or not isinstance(verification.get("legacy_row_count"), int)
        or verification.get("legacy_row_count") < minimum_legacy_row_count
        or isinstance(verification.get("parent_row_count"), bool)
        or not isinstance(verification.get("parent_row_count"), int)
        or verification.get("parent_row_count")
        < verification.get("legacy_row_count", 0)
    ):
        raise UsagePartitionControlError(
            "usage partition did not return complete verification"
        )


def cutover(
    target: str,
    prepare_receipt_path: str | os.PathLike[str],
    cutover_receipt_path: str | os.PathLike[str],
    confirmation: str,
) -> dict[str, Any]:
    target = remote._target(target)
    prepared = _load_prepare_receipt(prepare_receipt_path, target)
    if confirmation != prepared["required_cutover_confirmation"]:
        raise UsagePartitionControlError("cutover confirmation must exactly match the prepare receipt")
    upper = prepared["legacy_upper_exclusive"]
    payload = _run_remote(
        target,
        "cutover",
        [
            "--legacy-upper-exclusive",
            upper,
            "--minimum-legacy-row-count",
            str(prepared["row_count_before"]),
            "--confirm",
            confirmation,
        ],
        timeout_seconds=900,
        expected_instance_id=prepared["instance_id"],
    )
    if payload.get("instance_id") != prepared["instance_id"]:
        raise UsagePartitionControlError("cutover reached a different production instance")
    verification = payload.get("verification")
    if payload.get("mode") != "usage_logs_daily_partition_cutover":
        raise UsagePartitionControlError("cutover returned the wrong receipt mode")
    _validate_verification(verification, prepared["row_count_before"])
    combined = {
        **payload,
        "prepare_receipt": str(pathlib.Path(prepare_receipt_path).expanduser().resolve()),
        "row_count_before": prepared["row_count_before"],
    }
    _atomic_json(pathlib.Path(cutover_receipt_path), combined)
    return combined


def verify(
    target: str, prepare_receipt_path: str | os.PathLike[str]
) -> dict[str, Any]:
    target = remote._target(target)
    prepared = _load_prepare_receipt(prepare_receipt_path, target)
    payload = _run_remote(
        target,
        "verify",
        [
            "--legacy-upper-exclusive",
            prepared["legacy_upper_exclusive"],
            "--minimum-legacy-row-count",
            str(prepared["row_count_before"]),
        ],
        timeout_seconds=900,
        expected_instance_id=prepared["instance_id"],
    )
    if payload.get("instance_id") != prepared["instance_id"]:
        raise UsagePartitionControlError("verify reached a different production instance")
    if payload.get("mode") != "usage_logs_daily_partition_verify":
        raise UsagePartitionControlError("verify returned the wrong receipt mode")
    _validate_verification(payload, prepared["row_count_before"])
    return payload


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    status_parser = commands.add_parser("status")
    status_parser.add_argument("--target", required=True)
    prepare_parser = commands.add_parser("prepare")
    prepare_parser.add_argument("--target", required=True)
    prepare_parser.add_argument("--receipt", required=True)
    prepare_parser.add_argument("--confirm", required=True)
    abort_parser = commands.add_parser("abort")
    abort_parser.add_argument("--target", required=True)
    abort_parser.add_argument("--receipt", required=True)
    abort_parser.add_argument("--legacy-upper-exclusive", required=True)
    abort_parser.add_argument("--confirm", required=True)
    cutover_parser = commands.add_parser("cutover")
    cutover_parser.add_argument("--target", required=True)
    cutover_parser.add_argument("--prepare-receipt", required=True)
    cutover_parser.add_argument("--cutover-receipt", required=True)
    cutover_parser.add_argument("--confirm", required=True)
    verify_parser = commands.add_parser("verify")
    verify_parser.add_argument("--target", required=True)
    verify_parser.add_argument("--prepare-receipt", required=True)
    return parser


def main(argv: Iterable[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.command == "status":
            payload = _run_remote(args.target, "status", [], timeout_seconds=120)
        elif args.command == "prepare":
            payload = prepare(args.target, args.receipt, args.confirm)
        elif args.command == "abort":
            payload = abort(
                args.target,
                args.receipt,
                args.legacy_upper_exclusive,
                args.confirm,
            )
        elif args.command == "cutover":
            payload = cutover(
                args.target,
                args.prepare_receipt,
                args.cutover_receipt,
                args.confirm,
            )
        elif args.command == "verify":
            payload = verify(args.target, args.prepare_receipt)
        else:  # pragma: no cover
            raise UsagePartitionControlError(f"unsupported command: {args.command}")
        print(_canonical_json(payload))
    except (UsagePartitionControlError, remote.UsagePartitionError) as exc:
        print(f"usage_logs partition operator refused: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
