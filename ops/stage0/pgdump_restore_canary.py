#!/usr/bin/env python3
"""Restore and verify the newest real Fleet S3 PostgreSQL dump."""

from __future__ import annotations

import argparse
import datetime as dt
import fcntl
import gzip
import hashlib
import json
import os
import pathlib
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.parse
from collections.abc import Callable, Iterable
from typing import Any

from pgdump_restore_canary_contract import PRECIOUS_TABLES, precious_counts_valid


TARGET_RE = re.compile(r"(?:prod|edge:[a-z][a-z0-9]{1,15})")
OBJECT_RE = re.compile(r"tokenkey-\d{8}T\d{6}Z\.sql\.gz")
LIVE_POSTGRES = "tokenkey-postgres"
FRESHNESS = dt.timedelta(hours=3)
CAPACITY_HEADROOM_BYTES = 1024**3
POSTGRES_READY_ATTEMPTS = 180
POSTGRES_READY_SLEEP_SECONDS = 1
RunCommand = Callable[..., subprocess.CompletedProcess[str]]


class CanaryError(RuntimeError):
    """The restore canary could not prove a usable recovery object."""


def _default_run(args: list[str], **kwargs: Any) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, **kwargs)


def _raw_run(
    run: RunCommand, args: list[str], *, timeout: int = 120
) -> subprocess.CompletedProcess[str]:
    try:
        return run(
            args,
            capture_output=True,
            text=True,
            check=False,
            timeout=timeout,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise CanaryError(f"command could not run: {args[0]} {args[1]}") from exc


def _run(run: RunCommand, args: list[str], *, timeout: int = 120) -> str:
    completed = _raw_run(run, args, timeout=timeout)
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout or "command failed").strip()
        raise CanaryError(f"command failed: {args[0]} {args[1]}: {detail[:400]}")
    return completed.stdout.strip()


def _container_ready_diagnostics(run: RunCommand, container: str) -> str:
    inspect = _raw_run(
        run,
        [
            "docker",
            "inspect",
            "--format",
            "status={{.State.Status}} exit={{.State.ExitCode}} oom={{.State.OOMKilled}}",
            container,
        ],
        timeout=10,
    )
    logs = _raw_run(run, ["docker", "logs", "--tail", "40", container], timeout=15)
    inspect_text = (inspect.stdout or inspect.stderr or "inspect-unavailable").strip()
    log_text = (logs.stderr or logs.stdout or "logs-unavailable").strip()
    return f"{inspect_text}; logs={log_text[-400:]}"


def _wait_temporary_postgres(
    run: RunCommand,
    container: str,
    sleep: Callable[[float], None],
) -> None:
    last_error = "pg_isready failed"
    for attempt in range(POSTGRES_READY_ATTEMPTS):
        try:
            # Wait for postmaster listen only. Requiring -d tokenkey races CREATE DATABASE
            # on 0.5–1 CPU ARM hosts and made the first fleet run fail in 60s.
            _run(run, ["docker", "exec", container, "pg_isready"], timeout=10)
            return
        except CanaryError as exc:
            last_error = str(exc)
            if attempt == POSTGRES_READY_ATTEMPTS - 1:
                raise CanaryError(
                    "temporary PostgreSQL did not become ready: "
                    f"{_container_ready_diagnostics(run, container)}; last={last_error[:200]}"
                ) from exc
            sleep(POSTGRES_READY_SLEEP_SECONDS)


def _count_sql() -> str:
    fields = ",".join(
        f"'{table}',(SELECT count(*) FROM {table})" for table in PRECIOUS_TABLES
    )
    return f"SELECT json_build_object({fields});"


def _counts(run: RunCommand, container: str) -> dict[str, int]:
    raw = _run(
        run,
        [
            "docker", "exec", container, "psql", "-U", "tokenkey", "-d",
            "tokenkey", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-c",
            _count_sql(),
        ],
    )
    try:
        value = json.loads([line for line in raw.splitlines() if line.strip()][-1])
    except (IndexError, json.JSONDecodeError) as exc:
        raise CanaryError("core-table count result is invalid") from exc
    if not precious_counts_valid(value):
        raise CanaryError("precious-table count result is incomplete or invalid")
    return value


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


def _uncompressed_size(path: pathlib.Path) -> int:
    total = 0
    try:
        with gzip.open(path, "rb") as handle:
            for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                total += len(chunk)
    except (OSError, EOFError) as exc:
        raise CanaryError("downloaded S3 object is not a valid gzip stream") from exc
    if total <= 0:
        raise CanaryError("downloaded S3 object has an empty SQL stream")
    return total


def _parse_time(value: object) -> dt.datetime:
    if not isinstance(value, str):
        raise CanaryError("S3 object LastModified is invalid")
    try:
        parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise CanaryError("S3 object LastModified is invalid") from exc
    if parsed.tzinfo is None:
        raise CanaryError("S3 object LastModified has no timezone")
    return parsed.astimezone(dt.timezone.utc)


def _s3_location(target: str, env_path: pathlib.Path) -> tuple[str, str]:
    try:
        lines = env_path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise CanaryError(f"cannot read host env: {env_path}") from exc
    values = [line.partition("=")[2] for line in lines if line.startswith("TOKENKEY_PGDUMP_S3_URI=")]
    if len(values) != 1 or not values[0]:
        raise CanaryError("TOKENKEY_PGDUMP_S3_URI must appear exactly once and be non-empty")
    raw = values[0]
    if raw.strip() != raw or any(char in raw for char in "'\"`$\\"):
        raise CanaryError("TOKENKEY_PGDUMP_S3_URI contains unsupported shell syntax")
    parsed = urllib.parse.urlparse(raw)
    if parsed.scheme != "s3" or not parsed.netloc or parsed.params or parsed.query or parsed.fragment:
        raise CanaryError("TOKENKEY_PGDUMP_S3_URI must be an s3://bucket/prefix URI")
    prefix = parsed.path.strip("/")
    expected = "prod/pgdump" if target == "prod" else f"edge/{target.split(':', 1)[1]}/pgdump"
    if prefix != expected:
        raise CanaryError(f"configured pgdump prefix {prefix!r} does not match target {target}")
    return parsed.netloc, prefix


def _select_object(
    run: RunCommand,
    *,
    bucket: str,
    prefix: str,
    current_time: dt.datetime,
) -> tuple[str, str, int]:
    raw = _run(
        run,
        [
            "aws", "s3api", "list-objects-v2", "--bucket", bucket, "--prefix",
            f"{prefix}/", "--output", "json",
        ],
    )
    try:
        payload = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise CanaryError("S3 object listing is invalid JSON") from exc
    contents = payload.get("Contents", []) if isinstance(payload, dict) else []
    candidates: list[tuple[dt.datetime, str, str, int]] = []
    for item in contents if isinstance(contents, list) else []:
        if not isinstance(item, dict):
            continue
        key = item.get("Key")
        size = item.get("Size")
        if not isinstance(key, str) or pathlib.PurePosixPath(key).parent.as_posix() != prefix:
            continue
        if OBJECT_RE.fullmatch(pathlib.PurePosixPath(key).name) is None:
            continue
        if isinstance(size, bool) or not isinstance(size, int) or size <= 0:
            continue
        modified_raw = item.get("LastModified")
        modified = _parse_time(modified_raw)
        candidates.append((modified, key, str(modified_raw), size))
    if not candidates:
        raise CanaryError("no matching tokenkey-YYYYMMDDTHHMMSSZ.sql.gz object")
    modified, key, modified_raw, size = max(candidates, key=lambda item: item[0])
    age = current_time.astimezone(dt.timezone.utc) - modified
    if age < dt.timedelta(0) or age > FRESHNESS:
        raise CanaryError(f"newest S3 pgdump object is stale or future-dated: age={age}")
    return key, modified_raw, size


def _unlink_dump(path: pathlib.Path) -> None:
    path.unlink()


def _container_missing(result: subprocess.CompletedProcess[str]) -> bool:
    detail = (result.stderr or result.stdout).lower()
    return result.returncode != 0 and bool(
        re.search(r"no such (?:container|object)", detail)
    )


def _cleanup(
    run: RunCommand,
    *,
    container: str,
    container_creation_attempted: bool,
    dump_path: pathlib.Path | None,
    data_dir: pathlib.Path | None,
    temporary: pathlib.Path | None,
) -> list[str]:
    errors: list[str] = []
    if container_creation_attempted:
        try:
            removed = _raw_run(run, ["docker", "rm", "-f", container], timeout=60)
            if removed.returncode != 0 and not _container_missing(removed):
                errors.append(f"docker rm failed: {(removed.stderr or removed.stdout).strip()[:200]}")
        except CanaryError as exc:
            errors.append(str(exc))
        try:
            inspected = _raw_run(run, ["docker", "inspect", container], timeout=30)
            if inspected.returncode == 0:
                errors.append("temporary PostgreSQL container still exists")
            elif not _container_missing(inspected):
                errors.append(
                    f"docker inspect failed: {(inspected.stderr or inspected.stdout).strip()[:200]}"
                )
        except CanaryError as exc:
            errors.append(str(exc))
    if dump_path is not None and dump_path.exists():
        try:
            _unlink_dump(dump_path)
        except OSError as exc:
            errors.append(f"download cleanup failed: {exc}")
        if dump_path.exists():
            errors.append("downloaded S3 object still exists")
    if data_dir is not None and data_dir.exists():
        try:
            shutil.rmtree(data_dir)
        except OSError as exc:
            errors.append(f"data directory cleanup failed: {exc}")
        if data_dir.exists():
            errors.append("temporary PostgreSQL data directory still exists")
    if temporary is not None and temporary.exists():
        try:
            shutil.rmtree(temporary)
        except OSError as exc:
            errors.append(f"temporary directory cleanup failed: {exc}")
        if temporary.exists():
            errors.append("private canary temporary directory still exists")
    return errors


def run_canary(
    target: str,
    *,
    receipt_root: pathlib.Path = pathlib.Path("/var/lib/tokenkey/pgdump-canary"),
    env_path: pathlib.Path = pathlib.Path("/var/lib/tokenkey/.env"),
    expected_source_path: pathlib.Path | None = None,
    run: RunCommand = _default_run,
    sleep: Callable[[float], None] = time.sleep,
    now: Callable[[], dt.datetime] = lambda: dt.datetime.now(dt.timezone.utc),
    disk_usage: Callable[[pathlib.Path], Any] = shutil.disk_usage,
) -> dict[str, Any]:
    if TARGET_RE.fullmatch(target) is None:
        raise CanaryError("target must be prod or edge:<id>")
    bucket, prefix = _s3_location(target, env_path)
    if expected_source_path is not None:
        expected_source_path = expected_source_path.expanduser().resolve()
        if OBJECT_RE.fullmatch(expected_source_path.name) is None:
            raise CanaryError("expected local dump name is invalid")
        if not expected_source_path.is_file():
            raise CanaryError("expected local dump does not exist")
    current_time = now()
    if current_time.tzinfo is None:
        raise CanaryError("current time must be timezone-aware")

    receipt_root = receipt_root.expanduser().resolve()
    receipt_root.mkdir(parents=True, exist_ok=True)
    lock_handle = (receipt_root / "canary.lock").open("a+", encoding="utf-8")
    try:
        try:
            fcntl.flock(lock_handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise CanaryError("another restore canary is already running") from exc

        temporary: pathlib.Path | None = None
        dump_path: pathlib.Path | None = None
        data_dir: pathlib.Path | None = None
        container_creation_attempted = False
        container = f"tokenkey-pgdump-canary-{os.getpid()}"
        pending_receipt: dict[str, Any] | None = None
        primary_error: CanaryError | None = None
        try:
            key, last_modified, compressed_bytes = _select_object(
                run, bucket=bucket, prefix=prefix, current_time=current_time
            )
            if expected_source_path is not None:
                expected_key = f"{prefix}/{expected_source_path.name}"
                if key != expected_key:
                    raise CanaryError(f"newest S3 object is not the expected fresh dump: {expected_key}")
            temporary = pathlib.Path(tempfile.mkdtemp(prefix=".restore-", dir=receipt_root))
            temporary.chmod(0o700)
            dump_path = temporary / pathlib.PurePosixPath(key).name
            source_s3_uri = f"s3://{bucket}/{key}"
            _run(run, ["aws", "s3", "cp", source_s3_uri, str(dump_path), "--only-show-errors"], timeout=900)
            observed_bytes = dump_path.stat().st_size
            if observed_bytes != compressed_bytes:
                raise CanaryError(
                    f"S3 size mismatch: metadata={compressed_bytes} downloaded={observed_bytes}"
                )
            artifact_sha256 = _sha256(dump_path)
            source_local_sha256: str | None = None
            if expected_source_path is not None:
                source_local_bytes = expected_source_path.stat().st_size
                if source_local_bytes != observed_bytes:
                    raise CanaryError(
                        "S3 round-trip size mismatch: "
                        f"local={source_local_bytes} downloaded={observed_bytes}"
                    )
                source_local_sha256 = _sha256(expected_source_path)
                if source_local_sha256 != artifact_sha256:
                    raise CanaryError("S3 round-trip SHA-256 mismatch")
            uncompressed_bytes = _uncompressed_size(dump_path)
            required_free_bytes = compressed_bytes + (2 * uncompressed_bytes) + CAPACITY_HEADROOM_BYTES
            observed_free_bytes = int(disk_usage(receipt_root).free)
            if observed_free_bytes < required_free_bytes:
                raise CanaryError(
                    f"insufficient restore capacity: free={observed_free_bytes} required={required_free_bytes}"
                )

            running = _run(run, ["docker", "inspect", "--format", "{{.State.Running}}", LIVE_POSTGRES])
            if running != "true":
                raise CanaryError("live PostgreSQL container is not running")
            image = _run(run, ["docker", "inspect", "--format", "{{.Config.Image}}", LIVE_POSTGRES])
            if not image:
                raise CanaryError("live PostgreSQL image is empty")
            live_counts = _counts(run, LIVE_POSTGRES)

            data_dir = temporary / "postgres-data"
            data_dir.mkdir()
            data_dir.chmod(0o777)
            container_creation_attempted = True
            _run(
                run,
                [
                    "docker", "run", "--detach", "--name", container, "--pull=never",
                    "--network=none", "--cpus=1.00", "--memory=1024m",
                    "--memory-swap=1536m", "--env", "POSTGRES_HOST_AUTH_METHOD=trust",
                    "--env", "POSTGRES_USER=tokenkey", "--env", "POSTGRES_DB=tokenkey",
                    # postgres:18+ official images refuse a bind mount on
                    # /var/lib/postgresql/data and require one mount at the parent.
                    "--volume", f"{data_dir}:/var/lib/postgresql", image,
                ],
            )
            _wait_temporary_postgres(run, container, sleep)

            pipeline = (
                f"gunzip -c -- {shlex.quote(str(dump_path))} | "
                f"docker exec -i {shlex.quote(container)} psql -U tokenkey -d tokenkey "
                "-v ON_ERROR_STOP=1"
            )
            _run(run, ["bash", "-o", "pipefail", "-c", pipeline], timeout=1200)
            restored_counts = _counts(run, container)
            pending_receipt = {
                "schema_version": 2,
                "mode": "pgdump_restore_canary",
                "target": target,
                "source_s3_uri": source_s3_uri,
                "source_last_modified": last_modified,
                "compressed_bytes": compressed_bytes,
                "uncompressed_bytes": uncompressed_bytes,
                "required_free_bytes": required_free_bytes,
                "observed_free_bytes": observed_free_bytes,
                "artifact_sha256": artifact_sha256,
                "restore_image": image,
                "live_counts": live_counts,
                "restored_counts": restored_counts,
                "cleanup_verified": True,
                "source_mutated": False,
                "deletion_authorized": False,
            }
            if source_local_sha256 is not None:
                pending_receipt.update(
                    {
                        "source_local_sha256": source_local_sha256,
                        "s3_round_trip_verified": True,
                    }
                )
        except CanaryError as exc:
            primary_error = exc
        except OSError as exc:
            primary_error = CanaryError(f"local canary filesystem failure: {exc}")

        cleanup_errors = _cleanup(
            run,
            container=container,
            container_creation_attempted=container_creation_attempted,
            dump_path=dump_path,
            data_dir=data_dir,
            temporary=temporary,
        )
        if cleanup_errors:
            detail = "; ".join(cleanup_errors)
            if primary_error is not None:
                raise CanaryError(f"{primary_error}; cleanup failed: {detail}")
            raise CanaryError(f"cleanup failed: {detail}")
        if primary_error is not None:
            raise primary_error
        if pending_receipt is None:
            raise CanaryError("restore completed without receipt evidence")
        completed_at = now()
        if not isinstance(completed_at, dt.datetime) or completed_at.tzinfo is None:
            raise CanaryError("completion time must be timezone-aware")
        pending_receipt["completed_at"] = (
            completed_at.astimezone(dt.timezone.utc)
            .isoformat()
            .replace("+00:00", "Z")
        )
        _atomic_receipt(receipt_root / "latest.json", pending_receipt)
        return pending_receipt
    finally:
        lock_handle.close()


def run_fresh_dump_canary(
    target: str,
    *,
    receipt_root: pathlib.Path = pathlib.Path("/var/lib/tokenkey/pgdump-canary"),
    env_path: pathlib.Path = pathlib.Path("/var/lib/tokenkey/.env"),
    dump_dir: pathlib.Path = pathlib.Path("/var/lib/tokenkey/pgdump"),
    dump_script: pathlib.Path = pathlib.Path("/usr/local/bin/tokenkey-pgdump.sh"),
    run: RunCommand = _default_run,
    sleep: Callable[[float], None] = time.sleep,
    now: Callable[[], dt.datetime] = lambda: dt.datetime.now(dt.timezone.utc),
    disk_usage: Callable[[pathlib.Path], Any] = shutil.disk_usage,
) -> dict[str, Any]:
    if TARGET_RE.fullmatch(target) is None:
        raise CanaryError("target must be prod or edge:<id>")
    _s3_location(target, env_path)
    dump_dir = dump_dir.expanduser().resolve()
    if not dump_dir.is_dir():
        raise CanaryError(f"pgdump directory does not exist: {dump_dir}")

    def snapshot() -> dict[str, tuple[int, int, int]]:
        result: dict[str, tuple[int, int, int]] = {}
        for path in dump_dir.glob("tokenkey-*.sql.gz"):
            if OBJECT_RE.fullmatch(path.name) is None or not path.is_file():
                continue
            stat = path.stat()
            result[path.name] = (stat.st_ino, stat.st_size, stat.st_mtime_ns)
        return result

    before = snapshot()
    _run(run, [str(dump_script)], timeout=1200)
    changed = [
        dump_dir / name
        for name, identity in snapshot().items()
        if before.get(name) != identity
    ]
    if len(changed) != 1:
        raise CanaryError(f"fresh pgdump did not produce exactly one identifiable file: {len(changed)}")
    return run_canary(
        target,
        receipt_root=receipt_root,
        env_path=env_path,
        expected_source_path=changed[0],
        run=run,
        sleep=sleep,
        now=now,
        disk_usage=disk_usage,
    )


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", required=True)
    parser.add_argument(
        "--create-dump",
        action="store_true",
        help="create one real dump and prove its S3 round-trip before restoring it",
    )
    parser.add_argument(
        "--receipt-root",
        default=os.environ.get("TOKENKEY_PGDUMP_CANARY_ROOT", "/var/lib/tokenkey/pgdump-canary"),
    )
    return parser


def main(argv: Iterable[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        runner = run_fresh_dump_canary if args.create_dump else run_canary
        receipt = runner(args.target, receipt_root=pathlib.Path(args.receipt_root))
    except CanaryError as exc:
        print(f"pgdump restore canary failed: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(receipt, ensure_ascii=True, separators=(",", ":"), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
