#!/usr/bin/env python3
"""Operator CLI for the explicit production usage_logs daily partition cutover."""

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
INSTANCE_RE = re.compile(r"i-[0-9a-f]{17}")
RESOLVED_INSTANCE_RE = re.compile(
    r"\[run-probe\] resolved region=\S+ instance_id=(i-[0-9a-f]{17})"
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


def _run_remote(command: str, arguments: list[str], *, timeout_seconds: int) -> dict[str, Any]:
    try:
        completed = subprocess.run(
            [
                "bash",
                str(RUN_PROBE),
                "--target",
                "prod",
                "--script",
                str(REMOTE_WRAPPER),
                "--with",
                str(REMOTE),
                "--timeout-seconds",
                str(timeout_seconds),
                "--env",
                f"REMOTE_COMMAND={command}",
                "--env",
                f"REMOTE_ARGS_JSON={_canonical_json(arguments)}",
            ],
            capture_output=True,
            text=True,
            timeout=timeout_seconds + 30,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise UsagePartitionControlError("production usage partition command could not run") from exc
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout or "probe failed").strip()
        raise UsagePartitionControlError(f"production usage partition command failed: {detail[:600]}")
    instance_match = RESOLVED_INSTANCE_RE.search(completed.stderr)
    if not instance_match:
        raise UsagePartitionControlError("production usage partition command did not prove its instance")
    try:
        payload = json.loads([line for line in completed.stdout.splitlines() if line.strip()][-1])
    except (IndexError, json.JSONDecodeError) as exc:
        raise UsagePartitionControlError("production usage partition receipt is invalid") from exc
    if not isinstance(payload, dict) or payload.get("deletion_authorized") is not False:
        raise UsagePartitionControlError("production usage partition receipt failed safety validation")
    return {**payload, "instance_id": instance_match.group(1)}


def _load_prepare_receipt(path: str | os.PathLike[str]) -> dict[str, Any]:
    try:
        value = json.loads(pathlib.Path(path).expanduser().resolve().read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise UsagePartitionControlError("usage partition prepare receipt cannot be read") from exc
    if (
        not isinstance(value, dict)
        or value.get("mode") != "prod_usage_logs_daily_partition_prepare"
        or value.get("environment") != "prod"
        or value.get("bound_validated") is not True
        or value.get("source_rows_copied") is not False
        or value.get("deletion_authorized") is not False
        or isinstance(value.get("row_count_before"), bool)
        or not isinstance(value.get("row_count_before"), int)
        or value.get("row_count_before") < 0
        or INSTANCE_RE.fullmatch(str(value.get("instance_id", ""))) is None
        or value.get("required_cutover_confirmation")
        != remote.CUTOVER_CONFIRMATION_PREFIX + str(value.get("legacy_upper_exclusive"))
    ):
        raise UsagePartitionControlError("usage partition prepare receipt failed validation")
    remote._upper(str(value["legacy_upper_exclusive"]))
    return value


def prepare(receipt_path: str | os.PathLike[str], confirmation: str) -> dict[str, Any]:
    payload = _run_remote(
        "prepare", ["--confirm", confirmation], timeout_seconds=1200
    )
    if payload.get("mode") != "prod_usage_logs_daily_partition_prepare":
        raise UsagePartitionControlError("remote prepare returned the wrong receipt mode")
    _atomic_json(pathlib.Path(receipt_path), payload)
    return payload


def abort(
    receipt_path: str | os.PathLike[str],
    upper: str,
    confirmation: str,
) -> dict[str, Any]:
    upper = remote._upper(upper)
    if confirmation != remote.ABORT_CONFIRMATION_PREFIX + upper:
        raise UsagePartitionControlError(
            "abort confirmation must exactly match the partition upper bound"
        )
    payload = _run_remote(
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
        payload.get("mode") != "prod_usage_logs_daily_partition_abort"
        or payload.get("legacy_upper_exclusive") != upper
        or payload.get("bound_removed") is not True
        or payload.get("source_rows_copied") is not False
    ):
        raise UsagePartitionControlError("remote abort returned incomplete proof")
    _atomic_json(pathlib.Path(receipt_path), payload)
    return payload


def cutover(
    prepare_receipt_path: str | os.PathLike[str],
    cutover_receipt_path: str | os.PathLike[str],
    confirmation: str,
) -> dict[str, Any]:
    prepared = _load_prepare_receipt(prepare_receipt_path)
    if confirmation != prepared["required_cutover_confirmation"]:
        raise UsagePartitionControlError("cutover confirmation must exactly match the prepare receipt")
    upper = prepared["legacy_upper_exclusive"]
    payload = _run_remote(
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
    )
    if payload.get("instance_id") != prepared["instance_id"]:
        raise UsagePartitionControlError("cutover reached a different production instance")
    verification = payload.get("verification")
    if (
        payload.get("mode") != "prod_usage_logs_daily_partition_cutover"
        or not isinstance(verification, dict)
        or verification.get("partitioned") is not True
        or verification.get("legacy_attached") is not True
        or verification.get("no_parent_global_unique") is not True
        or verification.get("no_incoming_legacy_fk") is not True
        or verification.get("constraints_preserved") is not True
        or isinstance(verification.get("legacy_row_count"), bool)
        or not isinstance(verification.get("legacy_row_count"), int)
        or verification.get("legacy_row_count") < prepared["row_count_before"]
        or isinstance(verification.get("parent_row_count"), bool)
        or not isinstance(verification.get("parent_row_count"), int)
        or verification.get("parent_row_count") < verification.get("legacy_row_count", 0)
    ):
        raise UsagePartitionControlError("cutover did not return complete verification")
    combined = {
        **payload,
        "prepare_receipt": str(pathlib.Path(prepare_receipt_path).expanduser().resolve()),
        "row_count_before": prepared["row_count_before"],
    }
    _atomic_json(pathlib.Path(cutover_receipt_path), combined)
    return combined


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("status")
    prepare_parser = commands.add_parser("prepare")
    prepare_parser.add_argument("--receipt", required=True)
    prepare_parser.add_argument("--confirm", required=True)
    abort_parser = commands.add_parser("abort")
    abort_parser.add_argument("--receipt", required=True)
    abort_parser.add_argument("--legacy-upper-exclusive", required=True)
    abort_parser.add_argument("--confirm", required=True)
    cutover_parser = commands.add_parser("cutover")
    cutover_parser.add_argument("--prepare-receipt", required=True)
    cutover_parser.add_argument("--cutover-receipt", required=True)
    cutover_parser.add_argument("--confirm", required=True)
    verify_parser = commands.add_parser("verify")
    verify_parser.add_argument("--prepare-receipt", required=True)
    return parser


def main(argv: Iterable[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.command == "status":
            payload = _run_remote("status", [], timeout_seconds=120)
        elif args.command == "prepare":
            payload = prepare(args.receipt, args.confirm)
        elif args.command == "abort":
            payload = abort(
                args.receipt,
                args.legacy_upper_exclusive,
                args.confirm,
            )
        elif args.command == "cutover":
            payload = cutover(
                args.prepare_receipt,
                args.cutover_receipt,
                args.confirm,
            )
        elif args.command == "verify":
            prepared = _load_prepare_receipt(args.prepare_receipt)
            payload = _run_remote(
                "verify",
                [
                    "--legacy-upper-exclusive",
                    prepared["legacy_upper_exclusive"],
                    "--minimum-legacy-row-count",
                    str(prepared["row_count_before"]),
                ],
                timeout_seconds=900,
            )
        else:  # pragma: no cover
            raise UsagePartitionControlError(f"unsupported command: {args.command}")
        print(_canonical_json(payload))
    except (UsagePartitionControlError, remote.UsagePartitionError) as exc:
        print(f"usage_logs partition operator refused: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
