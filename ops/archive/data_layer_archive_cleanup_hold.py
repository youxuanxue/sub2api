#!/usr/bin/env python3
"""Control the production cleanup hold required by archive operations."""

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


HERE = pathlib.Path(__file__).resolve().parent
REPO = HERE.parents[1]
RUN_PROBE = REPO / "ops" / "observability" / "run-probe.sh"
REMOTE = HERE / "data_layer_archive_cleanup_hold_remote.py"
REMOTE_WRAPPER = HERE / "data_layer_archive_cleanup_hold_remote.sh"
HOLD_CONFIRMATION = "tokenkey-prod-archive-cleanup-hold-v1"
RELEASE_CONFIRMATION = "tokenkey-prod-archive-cleanup-release-v1"
INSTANCE_RE = re.compile(r"i-[0-9a-f]{17}")
RESOLVED_INSTANCE_RE = re.compile(
    r"\[run-probe\] resolved region=\S+ instance_id=(i-[0-9a-f]{17})"
)


class HoldControlError(RuntimeError):
    """Fail-closed cleanup hold controller error."""


def _canonical_json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=True, separators=(",", ":"), sort_keys=True)


def _atomic_json(path: pathlib.Path, value: dict[str, Any]) -> None:
    path = path.expanduser().resolve()
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


def _run_remote(command: str, arguments: list[str]) -> dict[str, Any]:
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
                "120",
                "--env",
                f"REMOTE_COMMAND={command}",
                "--env",
                f"REMOTE_ARGS_JSON={_canonical_json(arguments)}",
            ],
            capture_output=True,
            text=True,
            timeout=150,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise HoldControlError("production cleanup hold probe could not run") from exc
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout or "probe failed").strip()
        raise HoldControlError(f"production cleanup hold probe failed: {detail[:500]}")
    instance_match = RESOLVED_INSTANCE_RE.search(completed.stderr)
    if not instance_match:
        raise HoldControlError("production cleanup hold probe did not prove its instance")
    lines = [line for line in completed.stdout.splitlines() if line.strip()]
    try:
        payload = json.loads(lines[-1])
    except (IndexError, TypeError, json.JSONDecodeError) as exc:
        raise HoldControlError("production cleanup hold receipt is invalid") from exc
    if not isinstance(payload, dict) or payload.get("deletion_authorized") is not False:
        raise HoldControlError("production cleanup hold receipt failed safety validation")
    return {**payload, "instance_id": instance_match.group(1)}


def _load_receipt(path: str | os.PathLike[str]) -> dict[str, Any]:
    try:
        value = json.loads(
            pathlib.Path(path).expanduser().resolve().read_text(encoding="utf-8")
        )
    except (OSError, json.JSONDecodeError) as exc:
        raise HoldControlError("cleanup hold receipt cannot be read") from exc
    if (
        not isinstance(value, dict)
        or value.get("mode") != "prod_archive_cleanup_hold"
        or value.get("environment") != "prod"
        or value.get("hold_active") is not True
        or value.get("reload_proven") is not True
        or value.get("deletion_authorized") is not False
        or not isinstance(value.get("hold_started_at"), str)
        or INSTANCE_RE.fullmatch(str(value.get("instance_id", ""))) is None
    ):
        raise HoldControlError("cleanup hold receipt failed validation")
    return value


def verify_receipt_for_instance(
    path: str | os.PathLike[str], instance_id: str
) -> dict[str, Any]:
    receipt = _load_receipt(path)
    if receipt["instance_id"] != instance_id:
        raise HoldControlError("cleanup hold receipt targets a different production instance")
    return receipt


def apply(receipt_path: str | os.PathLike[str], confirmation: str) -> dict[str, Any]:
    if confirmation != HOLD_CONFIRMATION:
        raise HoldControlError("cleanup hold confirmation token is invalid")
    payload = _run_remote("apply", ["--confirm", confirmation])
    _atomic_json(pathlib.Path(receipt_path), payload)
    verified = verify(receipt_path)
    combined = {**payload, "verified_at": verified["server_clock"]}
    _atomic_json(pathlib.Path(receipt_path), combined)
    return combined


def verify(receipt_path: str | os.PathLike[str]) -> dict[str, Any]:
    receipt = _load_receipt(receipt_path)
    payload = _run_remote(
        "verify", ["--hold-started-at", receipt["hold_started_at"]]
    )
    if payload["instance_id"] != receipt["instance_id"]:
        raise HoldControlError("cleanup hold verification reached a different instance")
    if (
        payload.get("hold_active") is not True
        or payload.get("no_cleanup_after_hold") is not True
        or payload.get("runtime_disabled_proven") is not True
    ):
        raise HoldControlError("cleanup hold verification failed")
    return payload


def release(receipt_path: str | os.PathLike[str], confirmation: str) -> dict[str, Any]:
    if confirmation != RELEASE_CONFIRMATION:
        raise HoldControlError("cleanup release confirmation token is invalid")
    receipt = _load_receipt(receipt_path)
    previous = receipt.get("previous_cleanup_enabled")
    if not isinstance(previous, bool):
        raise HoldControlError("cleanup hold receipt has no previous enabled state")
    payload = _run_remote(
        "release",
        [
            "--confirm",
            confirmation,
            "--previous-cleanup-enabled",
            str(previous).lower(),
            "--hold-started-at",
            receipt["hold_started_at"],
        ],
    )
    if payload["instance_id"] != receipt["instance_id"]:
        raise HoldControlError("cleanup hold release reached a different instance")
    return payload


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("plan", help="read the current production cleanup state")
    apply_parser = commands.add_parser("apply", help="disable cleanup and write a receipt")
    apply_parser.add_argument("--receipt", required=True)
    apply_parser.add_argument("--confirm", required=True)
    verify_parser = commands.add_parser("verify", help="verify an existing hold receipt")
    verify_parser.add_argument("--receipt", required=True)
    release_parser = commands.add_parser("release", help="restore the pre-hold cleanup state")
    release_parser.add_argument("--receipt", required=True)
    release_parser.add_argument("--confirm", required=True)
    return parser


def main(argv: Iterable[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.command == "plan":
            payload = _run_remote("plan", [])
        elif args.command == "apply":
            payload = apply(args.receipt, args.confirm)
        elif args.command == "verify":
            payload = verify(args.receipt)
        elif args.command == "release":
            payload = release(args.receipt, args.confirm)
        else:  # pragma: no cover
            raise HoldControlError(f"unsupported command: {args.command}")
        print(_canonical_json(payload))
    except HoldControlError as exc:
        print(f"production archive cleanup hold refused: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
