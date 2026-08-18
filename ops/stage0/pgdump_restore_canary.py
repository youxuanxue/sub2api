#!/usr/bin/env python3
"""Create, restore, and verify an isolated precious-class PostgreSQL dump."""

from __future__ import annotations

import argparse
import datetime as dt
import fcntl
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile
import time
from collections.abc import Callable, Iterable
from typing import Any


CORE_TABLES = (
    "users",
    "accounts",
    "api_keys",
    "groups",
    "settings",
    "usage_billing_dedup",
)
EXCLUDE_DATA_GLOBS = (
    "ops_system_logs*",
    "usage_logs*",
    "ops_error_logs*",
    "qa_records*",
)
TARGET_RE = re.compile(r"(?:prod|edge:[a-z][a-z0-9]{1,15})")
LIVE_POSTGRES = "tokenkey-postgres"
MIN_DUMP_BYTES = 2048
RunCommand = Callable[..., subprocess.CompletedProcess[str]]


class CanaryError(RuntimeError):
    """The restore canary could not prove a usable dump."""


def _default_run(args: list[str], **kwargs: Any) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, **kwargs)


def _run(
    run: RunCommand,
    args: list[str],
    *,
    timeout: int = 120,
) -> str:
    try:
        completed = run(
            args,
            capture_output=True,
            text=True,
            check=False,
            timeout=timeout,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise CanaryError(f"command could not run: {args[0]} {args[1]}") from exc
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout or "command failed").strip()
        raise CanaryError(f"command failed: {args[0]} {args[1]}: {detail[:400]}")
    return completed.stdout.strip()


def _best_effort(run: RunCommand, args: list[str]) -> None:
    try:
        run(args, capture_output=True, text=True, check=False, timeout=30)
    except (OSError, subprocess.TimeoutExpired):
        pass


def _count_sql() -> str:
    fields = ",".join(
        f"'{table}',(SELECT count(*) FROM {table})" for table in CORE_TABLES
    )
    return f"SELECT json_build_object({fields});"


def _counts(run: RunCommand, container: str) -> dict[str, int]:
    raw = _run(
        run,
        [
            "docker",
            "exec",
            container,
            "psql",
            "-U",
            "tokenkey",
            "-d",
            "tokenkey",
            "-X",
            "-A",
            "-t",
            "-v",
            "ON_ERROR_STOP=1",
            "-c",
            _count_sql(),
        ],
    )
    try:
        value = json.loads([line for line in raw.splitlines() if line.strip()][-1])
    except (IndexError, json.JSONDecodeError) as exc:
        raise CanaryError("core-table count result is invalid") from exc
    if not isinstance(value, dict) or set(value) != set(CORE_TABLES):
        raise CanaryError("core-table count result is incomplete")
    for table, count in value.items():
        if isinstance(count, bool) or not isinstance(count, int) or count < 0:
            raise CanaryError(f"core-table count is invalid for {table}")
    return value


def _validate_counts(
    before: dict[str, int],
    after: dict[str, int],
    restored: dict[str, int],
) -> None:
    for table in CORE_TABLES:
        lower = min(before[table], after[table])
        upper = max(before[table], after[table])
        if not lower <= restored[table] <= upper:
            raise CanaryError(
                f"restored count for {table} is outside source window "
                f"[{lower}, {upper}]: {restored[table]}"
            )


def _atomic_receipt(path: pathlib.Path, value: dict[str, Any]) -> None:
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(value, handle, ensure_ascii=True, separators=(",", ":"), sort_keys=True)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        directory_fd = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        pathlib.Path(temporary).unlink(missing_ok=True)


def _sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def run_canary(
    target: str,
    *,
    receipt_root: pathlib.Path = pathlib.Path("/var/lib/tokenkey/pgdump-canary"),
    run: RunCommand = _default_run,
    sleep: Callable[[float], None] = time.sleep,
) -> dict[str, Any]:
    if TARGET_RE.fullmatch(target) is None:
        raise CanaryError("target must be prod or edge:<id>")

    receipt_root = receipt_root.expanduser().resolve()
    receipt_root.mkdir(parents=True, exist_ok=True)
    lock_path = receipt_root / "canary.lock"
    lock_handle = lock_path.open("a+", encoding="utf-8")
    try:
        try:
            fcntl.flock(lock_handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise CanaryError("another restore canary is already running") from exc

        temporary = pathlib.Path(
            tempfile.mkdtemp(prefix=".restore-", dir=receipt_root)
        )
        data_dir = temporary / "postgres-data"
        data_dir.mkdir()
        data_dir.chmod(0o777)
        dump_path = temporary / "restore.dump"
        suffix = f"{os.getpid()}"
        restore_container = f"tokenkey-pgdump-canary-{suffix}"
        remote_dump = f"/tmp/tokenkey-pgdump-canary-{suffix}.dump"
        restore_started = False
        try:
            running = _run(
                run,
                ["docker", "inspect", "--format", "{{.State.Running}}", LIVE_POSTGRES],
            )
            if running != "true":
                raise CanaryError("live PostgreSQL container is not running")
            image = _run(
                run,
                ["docker", "inspect", "--format", "{{.Config.Image}}", LIVE_POSTGRES],
            )
            if not image:
                raise CanaryError("live PostgreSQL image is empty")

            before = _counts(run, LIVE_POSTGRES)
            dump_command = [
                "docker",
                "exec",
                LIVE_POSTGRES,
                "pg_dump",
                "-U",
                "tokenkey",
                "-d",
                "tokenkey",
                "--format=custom",
                "--no-owner",
                "--no-privileges",
                f"--file={remote_dump}",
            ]
            dump_command.extend(
                f"--exclude-table-data={pattern}" for pattern in EXCLUDE_DATA_GLOBS
            )
            _run(run, dump_command, timeout=900)
            _run(
                run,
                ["docker", "cp", f"{LIVE_POSTGRES}:{remote_dump}", str(dump_path)],
                timeout=120,
            )
            artifact_bytes = dump_path.stat().st_size
            if artifact_bytes < MIN_DUMP_BYTES:
                raise CanaryError(f"pg_dump artifact is too small: {artifact_bytes} bytes")
            after = _counts(run, LIVE_POSTGRES)

            _run(
                run,
                [
                    "docker",
                    "run",
                    "--detach",
                    "--name",
                    restore_container,
                    "--pull=never",
                    "--network=none",
                    "--cpus=0.50",
                    "--memory=640m",
                    "--memory-swap=1024m",
                    "--env",
                    "POSTGRES_HOST_AUTH_METHOD=trust",
                    "--env",
                    "POSTGRES_USER=tokenkey",
                    "--env",
                    "POSTGRES_DB=tokenkey",
                    "--volume",
                    f"{data_dir}:/var/lib/postgresql/data",
                    image,
                ],
                timeout=120,
            )
            restore_started = True
            for attempt in range(60):
                try:
                    _run(
                        run,
                        [
                            "docker",
                            "exec",
                            restore_container,
                            "pg_isready",
                            "-U",
                            "tokenkey",
                            "-d",
                            "tokenkey",
                        ],
                        timeout=10,
                    )
                    break
                except CanaryError:
                    if attempt == 59:
                        raise CanaryError("temporary PostgreSQL did not become ready")
                    sleep(1)

            _run(
                run,
                ["docker", "cp", str(dump_path), f"{restore_container}:/tmp/restore.dump"],
                timeout=120,
            )
            _run(
                run,
                [
                    "docker",
                    "exec",
                    restore_container,
                    "pg_restore",
                    "-U",
                    "tokenkey",
                    "-d",
                    "tokenkey",
                    "--no-owner",
                    "--no-privileges",
                    "--exit-on-error",
                    "/tmp/restore.dump",
                ],
                timeout=1200,
            )
            restored = _counts(run, restore_container)
            _validate_counts(before, after, restored)

            digest = _sha256(dump_path)
            receipt: dict[str, Any] = {
                "schema_version": 1,
                "mode": "pgdump_restore_canary",
                "target": target,
                "completed_at": dt.datetime.now(dt.timezone.utc)
                .isoformat()
                .replace("+00:00", "Z"),
                "source_container": LIVE_POSTGRES,
                "restore_image": image,
                "source_counts": before,
                "source_counts_after": after,
                "restored_counts": restored,
                "artifact_bytes": artifact_bytes,
                "artifact_sha256": digest,
                "source_mutated": False,
                "deletion_authorized": False,
            }
            _atomic_receipt(receipt_root / "latest.json", receipt)
            return receipt
        finally:
            if restore_started:
                _best_effort(run, ["docker", "rm", "-f", restore_container])
            _best_effort(run, ["docker", "exec", LIVE_POSTGRES, "rm", "-f", remote_dump])
            shutil.rmtree(temporary, ignore_errors=True)
    finally:
        lock_handle.close()


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", required=True)
    parser.add_argument(
        "--receipt-root",
        default=os.environ.get(
            "TOKENKEY_PGDUMP_CANARY_ROOT", "/var/lib/tokenkey/pgdump-canary"
        ),
    )
    return parser


def main(argv: Iterable[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        receipt = run_canary(
            args.target, receipt_root=pathlib.Path(args.receipt_root)
        )
    except CanaryError as exc:
        print(f"pgdump restore canary failed: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(receipt, ensure_ascii=True, separators=(",", ":"), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
