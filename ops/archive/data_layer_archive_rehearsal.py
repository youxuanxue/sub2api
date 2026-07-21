#!/usr/bin/env python3
"""Local/non-production archive and restore rehearsal.

The original SQLite path remains available for deterministic unit tests. The
approved PostgreSQL phase is restricted to a localhost Docker database carrying
the dedicated rehearsal sentinel and has no purge/delete operation.
"""

from __future__ import annotations

import argparse
import datetime as dt
import gzip
import hashlib
import json
import os
import pathlib
import random
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import time
from typing import Any, Iterable
from urllib.parse import parse_qs, quote, unquote, urlparse

SCHEMA_VERSION = 1
SOURCE_TABLE = "archive_rehearsal_records"
RESTORE_TABLE = "archive_rehearsal_restored"
DATASETS = ("usage", "ops", "qa")
DEFAULT_RETENTION_DAYS = {"usage": 90, "ops": 30, "qa": 2}
ENVIRONMENTS = ("local", "nonprod")
POSTGRES_SOURCE_KIND = "local_docker_postgres_read_only"
POSTGRES_REHEARSAL_DATABASE = "tokenkey_archive_rehearsal"
POSTGRES_RESTORE_PREFIX = "tokenkey_archive_restore_"
POSTGRES_SENTINEL_TABLE = "archive_rehearsal_sentinel"
POSTGRES_SENTINEL_LABEL = "tokenkey_archive_rehearsal"
PROD_CANARY_MODE = "prod_archive_export_canary"
PROD_CANARY_SOURCE_KIND = "stage0_prod_docker_postgres_read_only"
PROD_CANARY_DATABASE = "tokenkey"
PROD_CANARY_STACK = "tokenkey-prod-stage0"
PROD_CANARY_CONTAINER = "tokenkey-postgres"
PROD_CANARY_TABLES = ("ops_system_logs", "ops_error_logs")
POSTGRES_TABLES = ("usage_logs", "ops_system_logs", "ops_error_logs", "qa_records")
POSTGRES_DATASETS = {
    "usage_logs": "usage",
    "ops_system_logs": "ops",
    "ops_error_logs": "ops",
    "qa_records": "qa",
}
DEFAULT_POSTGRES_TIMEOUT_SECONDS = 30
DEFAULT_POSTGRES_MAX_ROWS = 100_000


class RehearsalError(ValueError):
    """Fail-closed validation error for a rehearsal artifact or operation."""


def _positive_limit(name: str, value: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value <= 0:
        raise RehearsalError(f"{name} must be a positive integer")
    return value


def _sql_literal(value: str) -> str:
    """Quote a value for the fixed, locally generated psql statements."""
    return "'" + value.replace("'", "''") + "'"


def _postgres_dsn_info(dsn: str, *, target: bool) -> dict[str, Any]:
    if not isinstance(dsn, str) or not dsn.startswith(("postgresql://", "postgres://")):
        raise RehearsalError("PostgreSQL DSN must use a postgresql:// URI")
    parsed = urlparse(dsn)
    try:
        host = parsed.hostname
        port = parsed.port or 5432
    except ValueError as exc:
        raise RehearsalError("PostgreSQL DSN port is invalid") from exc
    if host not in {"localhost", "127.0.0.1", "::1"}:
        raise RehearsalError("PostgreSQL rehearsal only accepts a localhost Docker endpoint")
    query_keys = set(parse_qs(parsed.query, keep_blank_values=True))
    if query_keys & {
        "host",
        "hostaddr",
        "port",
        "dbname",
        "service",
        "servicefile",
        "options",
    }:
        raise RehearsalError("PostgreSQL DSN host overrides are not accepted")
    database = unquote(parsed.path.lstrip("/"))
    if target:
        if not database.startswith(POSTGRES_RESTORE_PREFIX):
            raise RehearsalError(
                f"restore database must start with {POSTGRES_RESTORE_PREFIX}"
            )
    elif database != POSTGRES_REHEARSAL_DATABASE:
        raise RehearsalError(
            f"source database must be {POSTGRES_REHEARSAL_DATABASE!r}"
        )
    if not parsed.username:
        raise RehearsalError("PostgreSQL DSN must include a user")
    if parsed.fragment:
        raise RehearsalError("PostgreSQL DSN fragments are not accepted")
    return {
        "host": host,
        "port": port,
        "database": database,
        "user": unquote(parsed.username),
        "dsn_sha256": _sha256(
            _canonical_json(
                {
                    "host": host,
                    "port": port,
                    "database": database,
                    "user": unquote(parsed.username),
                }
            ).encode("utf-8")
        ),
    }


def _run_psql(
    dsn: str,
    sql: str,
    *,
    timeout_seconds: int,
    read_only: bool,
) -> list[str]:
    if shutil.which("psql") is None:
        raise RehearsalError("psql is required for PostgreSQL rehearsal")
    _positive_limit("PostgreSQL timeout", timeout_seconds)
    begin = "BEGIN READ ONLY;" if read_only else "BEGIN;"
    statement_timeout_ms = timeout_seconds * 1000
    wrapped = (
        f"{begin} SET LOCAL lock_timeout = '2s'; "
        f"SET LOCAL statement_timeout = '{statement_timeout_ms}ms'; "
        f"{sql}; COMMIT;"
    )
    parsed = urlparse(dsn)
    psql_dsn = dsn
    environment = os.environ.copy()
    environment["PGCONNECT_TIMEOUT"] = str(timeout_seconds)
    if parsed.password:
        host_part = parsed.netloc.rsplit("@", 1)[-1]
        user = quote(unquote(parsed.username or ""), safe="")
        psql_dsn = parsed._replace(netloc=f"{user}@{host_part}").geturl()
        environment["PGPASSWORD"] = unquote(parsed.password)
    try:
        completed = subprocess.run(
            [
                "psql",
                psql_dsn,
                "-X",
                "-q",
                "-t",
                "-A",
                "-P",
                "pager=off",
                "-v",
                "ON_ERROR_STOP=1",
            ],
            input=wrapped,
            capture_output=True,
            text=True,
            env=environment,
            timeout=timeout_seconds + 5,
            check=False,
        )
    except subprocess.TimeoutExpired as exc:
        raise RehearsalError("PostgreSQL statement timed out") from exc
    if completed.returncode != 0:
        detail = (completed.stderr or "PostgreSQL command failed").strip()
        detail = detail.replace(dsn, "<redacted-postgres-dsn>")
        detail = detail.replace(psql_dsn, "<redacted-postgres-dsn>")
        if parsed.password:
            detail = detail.replace(parsed.password, "***")
            detail = detail.replace(unquote(parsed.password), "***")
        raise RehearsalError(f"PostgreSQL rehearsal query failed: {detail[:400]}")
    return [line for line in completed.stdout.splitlines() if line]


def _postgres_sentinel(dsn: str, *, timeout_seconds: int) -> None:
    rows = _run_psql(
        dsn,
        f"SELECT label FROM {POSTGRES_SENTINEL_TABLE} "
        f"WHERE label = {_sql_literal(POSTGRES_SENTINEL_LABEL)} LIMIT 1",
        timeout_seconds=timeout_seconds,
        read_only=True,
    )
    if rows != [POSTGRES_SENTINEL_LABEL]:
        raise RehearsalError(
            "PostgreSQL source is missing the archive rehearsal sentinel label"
        )


def _postgres_record_query(
    table: str, *, cutoff: str, limit: int
) -> str:
    if table not in POSTGRES_TABLES:
        raise RehearsalError(f"unsupported PostgreSQL source table {table!r}")
    return (
        "SELECT json_build_object("
        f"'dataset', {_sql_literal(POSTGRES_DATASETS[table])}, "
        "'record_id', row_data.id::text, "
        "'created_at', row_data.created_at, "
        "'payload', to_jsonb(row_data)"
        ")::text "
        f"FROM (SELECT * FROM {table} "
        f"WHERE created_at < {_sql_literal(cutoff)}::timestamptz "
        f"ORDER BY created_at, id LIMIT {limit}) AS row_data"
    )


def _postgres_candidates(
    dsn: str,
    *,
    as_of: str,
    retention_days: dict[str, int],
    timeout_seconds: int,
    max_rows: int,
) -> tuple[dict[str, list[dict[str, Any]]], int, dict[str, Any]]:
    info = _postgres_dsn_info(dsn, target=False)
    normalized_as_of = _timestamp(_utc(as_of))
    policy = retention_policy(
        retention_days["usage"], retention_days["ops"], retention_days["qa"]
    )
    _postgres_sentinel(dsn, timeout_seconds=timeout_seconds)
    candidates = {dataset: [] for dataset in DATASETS}
    source_rows = 0
    started = time.monotonic()
    table_rows: dict[str, int] = {}
    for table in POSTGRES_TABLES:
        count_rows = _run_psql(
            dsn,
            f"SELECT count(*)::bigint FROM {table}",
            timeout_seconds=timeout_seconds,
            read_only=True,
        )
        try:
            table_count = int(count_rows[0]) if count_rows else 0
        except ValueError as exc:
            raise RehearsalError(f"invalid row count returned for {table}") from exc
        table_rows[table] = table_count
        source_rows += table_count
        dataset = POSTGRES_DATASETS[table]
        cutoff = _timestamp(
            _utc(normalized_as_of) - dt.timedelta(days=policy[dataset])
        )
        lines = _run_psql(
            dsn,
            _postgres_record_query(table, cutoff=cutoff, limit=max_rows + 1),
            timeout_seconds=timeout_seconds,
            read_only=True,
        )
        if len(lines) > max_rows:
            raise RehearsalError(
                f"PostgreSQL candidate rows exceed max_rows={max_rows} for {table}"
            )
        for line in lines:
            try:
                value = json.loads(line, parse_constant=_reject_json_constant)
                if not isinstance(value, dict):
                    raise ValueError("row is not an object")
                if value.get("dataset") != dataset:
                    raise ValueError("row dataset does not match the source table")
                record = _record_from_row(
                    (
                        dataset,
                        (
                            f"{table}:{value.get('record_id')}"
                            if table in {"ops_system_logs", "ops_error_logs"}
                            else value.get("record_id")
                        ),
                        value.get("created_at"),
                        _canonical_json(value.get("payload")),
                    )
                )
            except (TypeError, ValueError, json.JSONDecodeError, RehearsalError) as exc:
                raise RehearsalError(f"invalid PostgreSQL row from {table}") from exc
            candidates[dataset].append(record)
    seen: set[tuple[str, str]] = set()
    for dataset, records in candidates.items():
        for record in records:
            key = (dataset, record["record_id"])
            if key in seen:
                raise RehearsalError(
                    f"duplicate PostgreSQL source record {dataset}/{record['record_id']}"
                )
            seen.add(key)
        records.sort(key=lambda record: (record["created_at"], record["record_id"]))
    candidate_rows = sum(len(records) for records in candidates.values())
    if candidate_rows > max_rows:
        raise RehearsalError(f"PostgreSQL candidate rows exceed max_rows={max_rows}")
    elapsed_ms = round((time.monotonic() - started) * 1000, 3)
    return candidates, source_rows, {
        "source_database": info["database"],
        "source_dsn_sha256": info["dsn_sha256"],
        "table_rows": table_rows,
        "query_elapsed_ms": elapsed_ms,
    }


def _compression_metrics(artifacts: list[dict[str, Any]]) -> dict[str, Any]:
    logical = sum(int(item["logical_bytes"]) for item in artifacts)
    compressed = sum(int(item["artifact_bytes"]) for item in artifacts)
    ratio = round(compressed / logical, 6) if logical else None
    return {
        "logical_bytes": logical,
        "artifact_bytes": compressed,
        "compression_ratio": ratio,
        "compression_savings_ratio": round(1 - ratio, 6) if ratio is not None else None,
    }


def _canonical_json(value: Any) -> str:
    return json.dumps(
        value,
        sort_keys=True,
        separators=(",", ":"),
        ensure_ascii=True,
        allow_nan=False,
    )


def _reject_json_constant(value: str) -> None:
    raise ValueError(f"non-finite JSON constant {value}")


def _sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def _utc(value: str) -> dt.datetime:
    try:
        parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise RehearsalError(f"invalid timestamp {value!r}") from exc
    if parsed.tzinfo is None:
        raise RehearsalError(f"timestamp must include a timezone: {value!r}")
    return parsed.astimezone(dt.timezone.utc)


def _timestamp(value: dt.datetime) -> str:
    value = value.astimezone(dt.timezone.utc)
    return value.isoformat(timespec="microseconds").replace("+00:00", "Z")


def _positive_days(name: str, value: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value <= 0:
        raise RehearsalError(f"{name} must be a positive integer")
    return value


def retention_policy(usage: int, ops: int, qa: int) -> dict[str, int]:
    return {
        "usage": _positive_days("usage retention", usage),
        "ops": _positive_days("ops retention", ops),
        "qa": _positive_days("qa retention", qa),
    }


def _local_path(value: str | os.PathLike[str], *, must_exist: bool) -> pathlib.Path:
    raw = os.fspath(value)
    if "://" in raw or raw.startswith(("postgres:", "postgresql:")):
        raise RehearsalError("only local filesystem paths are accepted")
    unresolved = pathlib.Path(raw).expanduser()
    if unresolved.is_symlink():
        raise RehearsalError(f"symlink paths are not accepted: {unresolved}")
    path = unresolved.resolve()
    if must_exist and not path.is_file():
        raise RehearsalError(f"local SQLite source does not exist: {path}")
    return path


def _source_connection(path: pathlib.Path) -> sqlite3.Connection:
    uri = f"file:{quote(str(path))}?mode=ro"
    connection = sqlite3.connect(uri, uri=True)
    connection.execute("PRAGMA query_only = ON")
    return connection


def _validate_source_schema(connection: sqlite3.Connection) -> None:
    columns = {
        row[1]
        for row in connection.execute(f"PRAGMA table_info({SOURCE_TABLE})").fetchall()
    }
    required = {"dataset", "record_id", "created_at", "payload_json"}
    if not required.issubset(columns):
        missing = sorted(required - columns)
        raise RehearsalError(
            f"source table {SOURCE_TABLE} missing columns: {', '.join(missing)}"
        )


def _record_from_row(row: tuple[Any, ...]) -> dict[str, Any]:
    dataset, record_id, created_at, payload_json = row
    if dataset not in DATASETS:
        raise RehearsalError(f"unsupported dataset {dataset!r}")
    if not isinstance(record_id, str) or not record_id:
        raise RehearsalError("record_id must be a non-empty string")
    try:
        payload = json.loads(
            payload_json,
            parse_constant=_reject_json_constant,
        )
        _canonical_json(payload)
    except (TypeError, ValueError, json.JSONDecodeError) as exc:
        raise RehearsalError(f"record {dataset}/{record_id} has invalid payload_json") from exc
    return {
        "dataset": dataset,
        "record_id": record_id,
        "created_at": _timestamp(_utc(str(created_at))),
        "payload": payload,
    }


def _record_line(record: dict[str, Any]) -> bytes:
    return (_canonical_json(record) + "\n").encode("utf-8")


def _record_from_doc(value: Any) -> dict[str, Any]:
    if not isinstance(value, dict) or set(value) != {
        "dataset",
        "record_id",
        "created_at",
        "payload",
    }:
        raise RehearsalError("archive record fields are invalid")
    normalized = _record_from_row(
        (
            value["dataset"],
            value["record_id"],
            value["created_at"],
            _canonical_json(value["payload"]),
        )
    )
    if normalized != value:
        raise RehearsalError("archive record is not normalized")
    return normalized


def collect_candidates(
    source: str | os.PathLike[str],
    *,
    as_of: str,
    retention_days: dict[str, int],
) -> tuple[dict[str, list[dict[str, Any]]], int]:
    source_path = _local_path(source, must_exist=True)
    cutoff_base = _utc(as_of)
    normalized_retention = retention_policy(
        retention_days["usage"], retention_days["ops"], retention_days["qa"]
    )
    candidates = {dataset: [] for dataset in DATASETS}
    seen: set[tuple[str, str]] = set()
    connection = _source_connection(source_path)
    try:
        _validate_source_schema(connection)
        rows = connection.execute(
            f"SELECT dataset, record_id, created_at, payload_json "
            f"FROM {SOURCE_TABLE} ORDER BY dataset, created_at, record_id"
        ).fetchall()
    finally:
        connection.close()

    for row in rows:
        record = _record_from_row(row)
        key = (record["dataset"], record["record_id"])
        if key in seen:
            raise RehearsalError(f"duplicate source record {key[0]}/{key[1]}")
        seen.add(key)
        cutoff = cutoff_base - dt.timedelta(days=normalized_retention[key[0]])
        if _utc(record["created_at"]) < cutoff:
            candidates[key[0]].append(record)
    for records in candidates.values():
        records.sort(key=lambda record: (record["created_at"], record["record_id"]))
    return candidates, len(rows)


def _dry_run_report(
    candidates: dict[str, list[dict[str, Any]]],
    source_rows: int,
    *,
    environment: str,
    normalized_as_of: str,
    retention_days: dict[str, int],
    source_path_sha256: str,
    source_file_identity: dict[str, Any],
) -> dict[str, Any]:
    base = _utc(normalized_as_of)
    datasets = []
    for dataset in DATASETS:
        raw = b"".join(_record_line(record) for record in candidates[dataset])
        datasets.append(
            {
                "dataset": dataset,
                "retention_days": retention_days[dataset],
                "cutoff_exclusive": _timestamp(
                    base - dt.timedelta(days=retention_days[dataset])
                ),
                "candidate_rows": len(candidates[dataset]),
                "candidate_logical_bytes": len(raw),
            }
        )
    return {
        "schema_version": SCHEMA_VERSION,
        "mode": "nonprod_archive_dry_run",
        "environment": environment,
        "as_of": normalized_as_of,
        "source_kind": "local_sqlite_read_only",
        "source_path_sha256": source_path_sha256,
        "source_file_identity": source_file_identity,
        "source_rows": source_rows,
        "candidate_rows": sum(item["candidate_rows"] for item in datasets),
        "candidate_logical_bytes": sum(
            item["candidate_logical_bytes"] for item in datasets
        ),
        "datasets": datasets,
        "source_mutated": False,
        "deletion_authorized": False,
    }


def dry_run(
    source: str | os.PathLike[str],
    *,
    environment: str,
    as_of: str,
    retention_days: dict[str, int],
) -> dict[str, Any]:
    if environment not in ENVIRONMENTS:
        raise RehearsalError(f"environment must be one of {ENVIRONMENTS}")
    source_path = _local_path(source, must_exist=True)
    source_stat = source_path.stat()
    normalized_as_of = _timestamp(_utc(as_of))
    normalized_retention = retention_policy(
        retention_days["usage"], retention_days["ops"], retention_days["qa"]
    )
    candidates, source_rows = collect_candidates(
        source_path, as_of=normalized_as_of, retention_days=normalized_retention
    )
    return _dry_run_report(
        candidates,
        source_rows,
        environment=environment,
        normalized_as_of=normalized_as_of,
        retention_days=normalized_retention,
        source_path_sha256=_sha256(str(source_path).encode("utf-8")),
        source_file_identity={"device": source_stat.st_dev, "inode": source_stat.st_ino},
    )


def _artifact_entry(dataset: str, records: list[dict[str, Any]]) -> tuple[dict[str, Any], bytes]:
    raw = b"".join(_record_line(record) for record in records)
    compressed = gzip.compress(raw, compresslevel=9, mtime=0)
    entry = {
        "dataset": dataset,
        "path": f"{dataset}.jsonl.gz",
        "row_count": len(records),
        "min_created_at": records[0]["created_at"] if records else None,
        "max_created_at": records[-1]["created_at"] if records else None,
        "logical_bytes": len(raw),
        "logical_sha256": _sha256(raw),
        "artifact_bytes": len(compressed),
        "artifact_sha256": _sha256(compressed),
    }
    return entry, compressed


def _write_sealed_batch(
    *,
    candidates: dict[str, list[dict[str, Any]]],
    archive_root: str | os.PathLike[str],
    manifest: dict[str, Any],
) -> dict[str, Any]:
    artifacts: list[dict[str, Any]] = []
    bodies: dict[str, bytes] = {}
    for dataset in DATASETS:
        if not candidates[dataset]:
            continue
        entry, compressed = _artifact_entry(dataset, candidates[dataset])
        artifacts.append(entry)
        bodies[entry["path"]] = compressed
    manifest["artifacts"] = artifacts
    manifest["total_rows"] = sum(entry["row_count"] for entry in artifacts)
    manifest["total_logical_bytes"] = sum(
        entry["logical_bytes"] for entry in artifacts
    )
    manifest["metrics"] = {
        **_compression_metrics(artifacts),
        "candidate_rows": manifest["total_rows"],
    }

    root = _local_path(archive_root, must_exist=False)
    root.mkdir(parents=True, exist_ok=True)
    batch_dir = root / manifest["batch_id"]
    if batch_dir.exists():
        verified = verify_batch(batch_dir)
        expected_manifest_sha = _sha256(
            (_canonical_json(manifest) + "\n").encode("utf-8")
        )
        if verified["manifest_sha256"] != expected_manifest_sha:
            raise RehearsalError(
                f"existing batch {manifest['batch_id']} does not match source snapshot"
            )
        return {**manifest, "batch_dir": str(batch_dir), "idempotent_reuse": True}

    temporary = pathlib.Path(tempfile.mkdtemp(prefix=f".{manifest['batch_id']}-", dir=root))
    try:
        for relative, body in bodies.items():
            (temporary / relative).write_bytes(body)
        _atomic_json(temporary / "manifest.json", manifest)
        temporary.replace(batch_dir)
    except Exception:
        for child in temporary.glob("*"):
            child.unlink(missing_ok=True)
        temporary.rmdir()
        raise
    verify_batch(batch_dir)
    return {**manifest, "batch_dir": str(batch_dir), "idempotent_reuse": False}


def _batch_id(
    *,
    environment: str,
    sealed_at: str,
    source_path_sha256: str,
    source_file_identity: dict[str, int],
    retention_days: dict[str, int],
    artifacts: list[dict[str, Any]],
    prefix: str = "rehearsal",
) -> str:
    identity = {
        "schema_version": SCHEMA_VERSION,
        "environment": environment,
        "as_of": sealed_at,
        "source_path_sha256": source_path_sha256,
        "source_file_identity": source_file_identity,
        "retention_days": retention_days,
        "artifacts": [
            {
                "dataset": entry["dataset"],
                "row_count": entry["row_count"],
                "logical_sha256": entry["logical_sha256"],
            }
            for entry in artifacts
        ],
    }
    digest = _sha256(_canonical_json(identity).encode("utf-8"))[:12]
    stamp = sealed_at.replace("-", "").replace(":", "")
    return f"{prefix}-{stamp}-{digest}"


def _atomic_json(path: pathlib.Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    body = (_canonical_json(payload) + "\n").encode("utf-8")
    temporary: pathlib.Path | None = None
    try:
        with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as handle:
            temporary = pathlib.Path(handle.name)
            handle.write(body)
            handle.flush()
            os.fsync(handle.fileno())
        temporary.replace(path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def seal_batch(
    source: str | os.PathLike[str],
    archive_root: str | os.PathLike[str],
    *,
    environment: str,
    as_of: str,
    retention_days: dict[str, int],
) -> dict[str, Any]:
    if environment not in ENVIRONMENTS:
        raise RehearsalError(f"environment must be one of {ENVIRONMENTS}")
    source_path = _local_path(source, must_exist=True)
    source_stat = source_path.stat()
    normalized_as_of = _timestamp(_utc(as_of))
    normalized_retention = retention_policy(
        retention_days["usage"], retention_days["ops"], retention_days["qa"]
    )
    candidates, source_rows = collect_candidates(
        source_path, as_of=normalized_as_of, retention_days=normalized_retention
    )
    report = _dry_run_report(
        candidates,
        source_rows,
        environment=environment,
        normalized_as_of=normalized_as_of,
        retention_days=normalized_retention,
        source_path_sha256=_sha256(str(source_path).encode("utf-8")),
        source_file_identity={"device": source_stat.st_dev, "inode": source_stat.st_ino},
    )
    if report["candidate_rows"] == 0:
        raise RehearsalError("cannot seal an empty rehearsal batch")
    artifacts: list[dict[str, Any]] = []
    bodies: dict[str, bytes] = {}
    for dataset in DATASETS:
        if not candidates[dataset]:
            continue
        entry, compressed = _artifact_entry(dataset, candidates[dataset])
        artifacts.append(entry)
        bodies[entry["path"]] = compressed

    batch_id = _batch_id(
        environment=environment,
        sealed_at=report["as_of"],
        source_path_sha256=report["source_path_sha256"],
        source_file_identity=report["source_file_identity"],
        retention_days=normalized_retention,
        artifacts=artifacts,
    )
    manifest = {
        "schema_version": SCHEMA_VERSION,
        "mode": "nonprod_archive_rehearsal",
        "environment": environment,
        "batch_id": batch_id,
        "sealed_at": report["as_of"],
        "source_kind": "local_sqlite_read_only",
        "source_path_sha256": report["source_path_sha256"],
        "source_file_identity": report["source_file_identity"],
        "retention_days": normalized_retention,
        "source_rows": report["source_rows"],
        "total_rows": sum(entry["row_count"] for entry in artifacts),
        "total_logical_bytes": sum(entry["logical_bytes"] for entry in artifacts),
        "artifacts": artifacts,
        "source_mutated": False,
        "deletion_authorized": False,
    }

    root = _local_path(archive_root, must_exist=False)
    root.mkdir(parents=True, exist_ok=True)
    batch_dir = root / batch_id
    if batch_dir.exists():
        verified = verify_batch(batch_dir)
        if verified["manifest_sha256"] != _sha256(
            (_canonical_json(manifest) + "\n").encode("utf-8")
        ):
            raise RehearsalError(f"existing batch {batch_id} does not match source snapshot")
        return {**manifest, "batch_dir": str(batch_dir), "idempotent_reuse": True}

    temporary = pathlib.Path(tempfile.mkdtemp(prefix=f".{batch_id}-", dir=root))
    try:
        for relative, body in bodies.items():
            (temporary / relative).write_bytes(body)
        _atomic_json(temporary / "manifest.json", manifest)
        temporary.replace(batch_dir)
    except Exception:
        for child in temporary.glob("*"):
            child.unlink(missing_ok=True)
        temporary.rmdir()
        raise
    verify_batch(batch_dir)
    return {**manifest, "batch_dir": str(batch_dir), "idempotent_reuse": False}


def _postgres_dry_run_report(
    candidates: dict[str, list[dict[str, Any]]],
    source_rows: int,
    *,
    as_of: str,
    retention_days: dict[str, int],
    source_info: dict[str, Any],
) -> dict[str, Any]:
    base = _utc(as_of)
    datasets = []
    for dataset in DATASETS:
        raw = b"".join(_record_line(record) for record in candidates[dataset])
        datasets.append(
            {
                "dataset": dataset,
                "retention_days": retention_days[dataset],
                "cutoff_exclusive": _timestamp(
                    base - dt.timedelta(days=retention_days[dataset])
                ),
                "candidate_rows": len(candidates[dataset]),
                "candidate_logical_bytes": len(raw),
            }
        )
    logical_bytes = sum(item["candidate_logical_bytes"] for item in datasets)
    return {
        "schema_version": SCHEMA_VERSION,
        "mode": "postgres_archive_dry_run",
        "environment": "nonprod",
        "as_of": _timestamp(base),
        "source_kind": POSTGRES_SOURCE_KIND,
        "source_database": source_info["source_database"],
        "source_dsn_sha256": source_info["source_dsn_sha256"],
        "source_rows": source_rows,
        "candidate_rows": sum(item["candidate_rows"] for item in datasets),
        "candidate_logical_bytes": logical_bytes,
        "datasets": datasets,
        "table_rows": source_info["table_rows"],
        "query_elapsed_ms": source_info["query_elapsed_ms"],
        "source_mutated": False,
        "deletion_authorized": False,
    }


def postgres_dry_run(
    source_dsn: str,
    *,
    as_of: str,
    retention_days: dict[str, int],
    timeout_seconds: int = DEFAULT_POSTGRES_TIMEOUT_SECONDS,
    max_rows: int = DEFAULT_POSTGRES_MAX_ROWS,
) -> tuple[dict[str, Any], dict[str, list[dict[str, Any]]], dict[str, Any]]:
    _postgres_dsn_info(source_dsn, target=False)
    _positive_limit("PostgreSQL max_rows", max_rows)
    candidates, source_rows, source_info = _postgres_candidates(
        source_dsn,
        as_of=as_of,
        retention_days=retention_days,
        timeout_seconds=timeout_seconds,
        max_rows=max_rows,
    )
    report = _postgres_dry_run_report(
        candidates,
        source_rows,
        as_of=as_of,
        retention_days=retention_policy(
            retention_days["usage"], retention_days["ops"], retention_days["qa"]
        ),
        source_info=source_info,
    )
    return report, candidates, source_info


def seal_postgres_batch(
    source_dsn: str,
    archive_root: str | os.PathLike[str],
    *,
    as_of: str,
    retention_days: dict[str, int],
    timeout_seconds: int = DEFAULT_POSTGRES_TIMEOUT_SECONDS,
    max_rows: int = DEFAULT_POSTGRES_MAX_ROWS,
) -> dict[str, Any]:
    report, candidates, source_info = postgres_dry_run(
        source_dsn,
        as_of=as_of,
        retention_days=retention_days,
        timeout_seconds=timeout_seconds,
        max_rows=max_rows,
    )
    if report["candidate_rows"] == 0:
        raise RehearsalError("cannot seal an empty PostgreSQL rehearsal batch")
    normalized_retention = retention_policy(
        retention_days["usage"], retention_days["ops"], retention_days["qa"]
    )
    artifacts_preview = [
        _artifact_entry(dataset, candidates[dataset])[0]
        for dataset in DATASETS
        if candidates[dataset]
    ]
    source_identity = {
        "database": source_info["source_database"],
        "dsn_sha256": source_info["source_dsn_sha256"],
    }
    batch_id = _batch_id(
        environment="nonprod",
        sealed_at=report["as_of"],
        source_path_sha256=source_info["source_dsn_sha256"],
        source_file_identity=source_identity,
        retention_days=normalized_retention,
        artifacts=artifacts_preview,
    )
    manifest = {
        "schema_version": SCHEMA_VERSION,
        "mode": "nonprod_archive_rehearsal",
        "environment": "nonprod",
        "batch_id": batch_id,
        "sealed_at": report["as_of"],
        "source_kind": POSTGRES_SOURCE_KIND,
        "source_database": source_info["source_database"],
        "source_dsn_sha256": source_info["source_dsn_sha256"],
        "source_file_identity": source_identity,
        "retention_days": normalized_retention,
        "source_rows": report["source_rows"],
        "source_mutated": False,
        "deletion_authorized": False,
    }
    return _write_sealed_batch(
        candidates=candidates, archive_root=archive_root, manifest=manifest
    )


def _safe_artifact(batch_dir: pathlib.Path, relative: str) -> pathlib.Path:
    if pathlib.PurePosixPath(relative).name != relative:
        raise RehearsalError(f"artifact path must be a basename: {relative!r}")
    unresolved = batch_dir / relative
    if unresolved.is_symlink():
        raise RehearsalError(f"artifact symlinks are not accepted: {relative!r}")
    path = unresolved.resolve()
    if path.parent != batch_dir.resolve():
        raise RehearsalError(f"artifact escapes batch directory: {relative!r}")
    return path


def _parse_artifact(
    batch_dir: pathlib.Path, entry: dict[str, Any]
) -> tuple[list[dict[str, Any]], bytes]:
    path = _safe_artifact(batch_dir, str(entry.get("path", "")))
    if not path.is_file() or path.is_symlink():
        raise RehearsalError(f"artifact missing: {path.name}")
    compressed = path.read_bytes()
    if _sha256(compressed) != entry.get("artifact_sha256"):
        raise RehearsalError(f"artifact checksum mismatch: {path.name}")
    if len(compressed) != entry.get("artifact_bytes"):
        raise RehearsalError(f"artifact byte count mismatch: {path.name}")
    try:
        raw = gzip.decompress(compressed)
    except (gzip.BadGzipFile, EOFError) as exc:
        raise RehearsalError(f"artifact is not valid gzip: {path.name}") from exc
    if _sha256(raw) != entry.get("logical_sha256"):
        raise RehearsalError(f"logical checksum mismatch: {path.name}")
    if len(raw) != entry.get("logical_bytes"):
        raise RehearsalError(f"logical byte count mismatch: {path.name}")

    records: list[dict[str, Any]] = []
    for number, line in enumerate(raw.splitlines(keepends=True), start=1):
        try:
            value = json.loads(line)
            normalized = _record_from_doc(value)
        except (TypeError, ValueError, json.JSONDecodeError, RehearsalError) as exc:
            raise RehearsalError(f"invalid JSONL at {path.name}:{number}") from exc
        if _record_line(normalized) != line:
            raise RehearsalError(f"non-canonical record at {path.name}:{number}")
        if normalized["dataset"] != entry.get("dataset"):
            raise RehearsalError(f"dataset mismatch at {path.name}:{number}")
        records.append(normalized)
    if len(records) != entry.get("row_count"):
        raise RehearsalError(f"row count mismatch: {path.name}")
    keys = [(record["created_at"], record["record_id"]) for record in records]
    if keys != sorted(keys) or len(keys) != len(set(keys)):
        raise RehearsalError(f"artifact records are unsorted or duplicated: {path.name}")
    minimum = records[0]["created_at"] if records else None
    maximum = records[-1]["created_at"] if records else None
    if minimum != entry.get("min_created_at") or maximum != entry.get("max_created_at"):
        raise RehearsalError(f"artifact time range mismatch: {path.name}")
    return records, raw


def verify_batch(batch: str | os.PathLike[str]) -> dict[str, Any]:
    batch_dir = pathlib.Path(batch).expanduser().resolve()
    manifest_path = batch_dir / "manifest.json"
    if not batch_dir.is_dir() or not manifest_path.is_file() or manifest_path.is_symlink():
        raise RehearsalError(f"batch manifest missing: {manifest_path}")
    manifest_bytes = manifest_path.read_bytes()
    try:
        manifest = json.loads(manifest_bytes)
    except json.JSONDecodeError as exc:
        raise RehearsalError("manifest is not valid JSON") from exc
    if not isinstance(manifest, dict):
        raise RehearsalError("manifest must be a JSON object")
    if manifest.get("schema_version") != SCHEMA_VERSION:
        raise RehearsalError("unsupported manifest schema_version")
    mode = manifest.get("mode")
    source_kind = manifest.get("source_kind")
    if mode == "nonprod_archive_rehearsal":
        if manifest.get("environment") not in ENVIRONMENTS:
            raise RehearsalError("manifest environment is not local/nonprod")
        if source_kind not in {"local_sqlite_read_only", POSTGRES_SOURCE_KIND}:
            raise RehearsalError(
                "manifest source is not an approved read-only rehearsal snapshot"
            )
    elif mode == PROD_CANARY_MODE:
        if manifest.get("environment") != "prod":
            raise RehearsalError("production canary manifest environment must be prod")
        if source_kind != PROD_CANARY_SOURCE_KIND:
            raise RehearsalError("production canary source kind is invalid")
    else:
        raise RehearsalError(
            "manifest is not an approved archive rehearsal or production canary"
        )
    if manifest.get("source_mutated") is not False:
        raise RehearsalError("manifest must state that the source was not mutated")
    if manifest.get("deletion_authorized") is not False:
        raise RehearsalError("manifest must explicitly keep deletion unauthorized")
    if manifest.get("batch_id") != batch_dir.name:
        raise RehearsalError("manifest batch_id does not match its directory")
    sealed_at = manifest.get("sealed_at")
    if not isinstance(sealed_at, str) or _timestamp(_utc(sealed_at)) != sealed_at:
        raise RehearsalError("manifest sealed_at is not canonical UTC")
    source_path_sha256 = manifest.get("source_path_sha256")
    source_dsn_sha256 = manifest.get("source_dsn_sha256")
    source_identity_sha256 = manifest.get("source_identity_sha256")
    source_file_identity = manifest.get("source_file_identity")
    if source_kind == "local_sqlite_read_only":
        if (
            not isinstance(source_path_sha256, str)
            or len(source_path_sha256) != 64
            or any(character not in "0123456789abcdef" for character in source_path_sha256)
        ):
            raise RehearsalError("manifest source_path_sha256 is invalid")
        if (
            not isinstance(source_file_identity, dict)
            or set(source_file_identity) != {"device", "inode"}
            or any(
                not isinstance(value, int) or isinstance(value, bool) or value < 0
                for value in source_file_identity.values()
            )
        ):
            raise RehearsalError("manifest source_file_identity is invalid")
    elif source_kind == POSTGRES_SOURCE_KIND:
        if (
            not isinstance(source_dsn_sha256, str)
            or len(source_dsn_sha256) != 64
            or any(character not in "0123456789abcdef" for character in source_dsn_sha256)
        ):
            raise RehearsalError("manifest source_dsn_sha256 is invalid")
        if manifest.get("source_database") != POSTGRES_REHEARSAL_DATABASE:
            raise RehearsalError("manifest source database is not the rehearsal database")
        if (
            not isinstance(source_file_identity, dict)
            or set(source_file_identity) != {"database", "dsn_sha256"}
            or source_file_identity.get("database") != POSTGRES_REHEARSAL_DATABASE
            or source_file_identity.get("dsn_sha256") != source_dsn_sha256
        ):
            raise RehearsalError("manifest PostgreSQL source identity is invalid")
    else:
        expected_identity_keys = {
            "container",
            "database",
            "instance_id",
            "stack",
            "table",
        }
        if (
            not isinstance(source_identity_sha256, str)
            or len(source_identity_sha256) != 64
            or any(
                character not in "0123456789abcdef"
                for character in source_identity_sha256
            )
        ):
            raise RehearsalError("production canary source identity checksum is invalid")
        if (
            not isinstance(source_file_identity, dict)
            or set(source_file_identity) != expected_identity_keys
            or manifest.get("source_database") != PROD_CANARY_DATABASE
            or source_file_identity.get("container") != PROD_CANARY_CONTAINER
            or source_file_identity.get("database") != PROD_CANARY_DATABASE
            or source_file_identity.get("stack") != PROD_CANARY_STACK
            or source_file_identity.get("table") not in PROD_CANARY_TABLES
        ):
            raise RehearsalError("production canary source identity is invalid")
        instance_id = source_file_identity.get("instance_id")
        if (
            not isinstance(instance_id, str)
            or not instance_id.startswith("i-")
            or len(instance_id) != 19
            or any(character not in "0123456789abcdef" for character in instance_id[2:])
        ):
            raise RehearsalError("production canary instance identity is invalid")
        expected_identity_sha256 = _sha256(
            _canonical_json(source_file_identity).encode("utf-8")
        )
        if source_identity_sha256 != expected_identity_sha256:
            raise RehearsalError("production canary source identity checksum mismatch")
    policy = manifest.get("retention_days")
    if not isinstance(policy, dict):
        raise RehearsalError("manifest retention_days is invalid")
    try:
        normalized_policy = retention_policy(policy["usage"], policy["ops"], policy["qa"])
    except (KeyError, TypeError) as exc:
        raise RehearsalError("manifest retention_days is invalid") from exc
    if policy != normalized_policy:
        raise RehearsalError("manifest retention_days is not canonical")
    if mode == PROD_CANARY_MODE and normalized_policy != DEFAULT_RETENTION_DAYS:
        raise RehearsalError("production canary retention policy is not approved")
    canary = manifest.get("canary")
    canary_cutoff: dt.datetime | None = None
    if mode == PROD_CANARY_MODE:
        expected_canary_keys = {
            "cutoff_exclusive",
            "lock_timeout_ms",
            "max_logical_bytes",
            "max_rows",
            "query_elapsed_ms",
            "server_clock",
            "statement_timeout_seconds",
            "table",
        }
        if not isinstance(canary, dict) or set(canary) != expected_canary_keys:
            raise RehearsalError("production canary bounds are invalid")
        if canary.get("table") != source_file_identity.get("table"):
            raise RehearsalError("production canary table does not match source identity")
        try:
            canary_cutoff = _utc(canary["cutoff_exclusive"])
            server_clock = _utc(canary["server_clock"])
        except (KeyError, TypeError, AttributeError) as exc:
            raise RehearsalError("production canary timestamps are invalid") from exc
        if _timestamp(canary_cutoff) != canary["cutoff_exclusive"]:
            raise RehearsalError("production canary cutoff is not canonical UTC")
        if _timestamp(server_clock) != canary["server_clock"]:
            raise RehearsalError("production canary server clock is not canonical UTC")
        if _utc(sealed_at) != server_clock:
            raise RehearsalError("production canary seal time must use the server clock")
        expected_cutoff = server_clock - dt.timedelta(days=DEFAULT_RETENTION_DAYS["ops"])
        if canary_cutoff != expected_cutoff:
            raise RehearsalError("production canary cutoff is not the approved cold waterline")
        integer_bounds = {
            "lock_timeout_ms": (100, 100),
            "max_logical_bytes": (1, 64 * 1024 * 1024),
            "max_rows": (1, 10_000),
            "statement_timeout_seconds": (1, 30),
        }
        for key, (minimum, maximum) in integer_bounds.items():
            value = canary.get(key)
            if (
                not isinstance(value, int)
                or isinstance(value, bool)
                or not minimum <= value <= maximum
            ):
                raise RehearsalError(f"production canary {key} is out of bounds")
        elapsed = canary.get("query_elapsed_ms")
        if (
            not isinstance(elapsed, (int, float))
            or isinstance(elapsed, bool)
            or elapsed < 0
        ):
            raise RehearsalError("production canary query_elapsed_ms is invalid")
        staging_prefix = manifest.get("staging_s3_prefix")
        if (
            not isinstance(staging_prefix, str)
            or not staging_prefix.startswith("s3://")
            or "/prod/pgdump/archive-canary/" not in staging_prefix
            or not staging_prefix.endswith(f"/{manifest.get('batch_id')}")
            or ".." in staging_prefix
        ):
            raise RehearsalError("production canary S3 staging prefix is invalid")
    artifacts = manifest.get("artifacts")
    if not isinstance(artifacts, list) or not artifacts:
        raise RehearsalError("manifest must contain at least one artifact")
    artifact_datasets = [
        entry.get("dataset") if isinstance(entry, dict) else None for entry in artifacts
    ]
    if artifact_datasets != [dataset for dataset in DATASETS if dataset in artifact_datasets]:
        raise RehearsalError("manifest artifacts are not in canonical dataset order")
    if mode == PROD_CANARY_MODE and artifact_datasets != ["ops"]:
        raise RehearsalError("production canary must contain exactly one ops artifact")
    total_rows = 0
    total_bytes = 0
    datasets: set[str] = set()
    for entry in artifacts:
        if not isinstance(entry, dict) or entry.get("dataset") not in DATASETS:
            raise RehearsalError("manifest contains an invalid artifact entry")
        if entry["dataset"] in datasets:
            raise RehearsalError(f"duplicate artifact dataset {entry['dataset']}")
        datasets.add(entry["dataset"])
        records, raw = _parse_artifact(batch_dir, entry)
        if mode == PROD_CANARY_MODE:
            assert canary_cutoff is not None
            table_prefix = f"{source_file_identity['table']}:"
            if any(
                _utc(record["created_at"]) >= canary_cutoff
                or not record["record_id"].startswith(table_prefix)
                for record in records
            ):
                raise RehearsalError("production canary artifact contains hot or foreign rows")
        total_rows += len(records)
        total_bytes += len(raw)
    if total_rows != manifest.get("total_rows"):
        raise RehearsalError("manifest total_rows mismatch")
    if total_bytes != manifest.get("total_logical_bytes"):
        raise RehearsalError("manifest total_logical_bytes mismatch")
    if mode == PROD_CANARY_MODE:
        assert isinstance(canary, dict)
        if total_rows > canary["max_rows"]:
            raise RehearsalError("production canary row count exceeds its manifest bound")
        if total_bytes > canary["max_logical_bytes"]:
            raise RehearsalError("production canary byte count exceeds its manifest bound")
    source_rows = manifest.get("source_rows")
    if (
        not isinstance(source_rows, int)
        or isinstance(source_rows, bool)
        or source_rows < total_rows
    ):
        raise RehearsalError("manifest source_rows is invalid")
    try:
        expected_batch_id = _batch_id(
            environment=manifest["environment"],
            sealed_at=sealed_at,
            source_path_sha256=(
                source_path_sha256
                if source_kind == "local_sqlite_read_only"
                else (
                    source_dsn_sha256
                    if source_kind == POSTGRES_SOURCE_KIND
                    else source_identity_sha256
                )
            ),
            source_file_identity=source_file_identity,
            retention_days=normalized_policy,
            artifacts=artifacts,
            prefix="prod-canary" if mode == PROD_CANARY_MODE else "rehearsal",
        )
    except (KeyError, TypeError) as exc:
        raise RehearsalError("manifest artifact identity fields are invalid") from exc
    if manifest.get("batch_id") != expected_batch_id:
        raise RehearsalError("manifest batch identity does not match its contents")
    return {
        "schema_version": SCHEMA_VERSION,
        "mode": (
            "prod_archive_export_verify"
            if mode == PROD_CANARY_MODE
            else "nonprod_archive_verify"
        ),
        "batch_id": manifest.get("batch_id"),
        "source_kind": source_kind,
        "verified": True,
        "artifact_count": len(artifacts),
        "row_count": total_rows,
        "metrics": manifest.get("metrics", _compression_metrics(artifacts)),
        "manifest_sha256": _sha256(manifest_bytes),
        "deletion_authorized": False,
    }


def restore_random(
    batch: str | os.PathLike[str],
    target: str | os.PathLike[str],
    *,
    seed: int,
) -> dict[str, Any]:
    verification = verify_batch(batch)
    batch_dir = pathlib.Path(batch).expanduser().resolve()
    manifest_bytes = (batch_dir / "manifest.json").read_bytes()
    if _sha256(manifest_bytes) != verification["manifest_sha256"]:
        raise RehearsalError("manifest changed after verification")
    manifest = json.loads(manifest_bytes)
    if manifest.get("source_kind") != "local_sqlite_read_only":
        raise RehearsalError(
            "PostgreSQL rehearsal batches require restore-postgres-random"
        )
    entry = random.Random(seed).choice(manifest["artifacts"])
    records, raw = _parse_artifact(batch_dir, entry)
    target_path = _local_path(target, must_exist=False)
    if batch_dir == target_path or batch_dir in target_path.parents:
        raise RehearsalError("restore target must be outside the sealed batch")
    if _sha256(str(target_path).encode("utf-8")) == manifest["source_path_sha256"]:
        raise RehearsalError("restore target must not be the sealed batch source")
    if target_path.exists():
        target_stat = target_path.stat()
        if {
            "device": target_stat.st_dev,
            "inode": target_stat.st_ino,
        } == manifest["source_file_identity"]:
            raise RehearsalError("restore target must not reference the sealed source file")
    target_path.parent.mkdir(parents=True, exist_ok=True)

    connection = sqlite3.connect(target_path)
    inserted = 0
    try:
        connection.execute(
            f"CREATE TABLE IF NOT EXISTS {RESTORE_TABLE} ("
            "batch_id TEXT NOT NULL, dataset TEXT NOT NULL, record_id TEXT NOT NULL, "
            "created_at TEXT NOT NULL, payload_json TEXT NOT NULL, "
            "PRIMARY KEY (batch_id, dataset, record_id))"
        )
        connection.execute("BEGIN IMMEDIATE")
        for record in records:
            cursor = connection.execute(
                f"INSERT OR IGNORE INTO {RESTORE_TABLE} "
                "(batch_id, dataset, record_id, created_at, payload_json) "
                "VALUES (?, ?, ?, ?, ?)",
                (
                    manifest["batch_id"],
                    record["dataset"],
                    record["record_id"],
                    record["created_at"],
                    _canonical_json(record["payload"]),
                ),
            )
            inserted += cursor.rowcount
        restored_rows = connection.execute(
            f"SELECT dataset, record_id, created_at, payload_json FROM {RESTORE_TABLE} "
            "WHERE batch_id = ? AND dataset = ? ORDER BY created_at, record_id",
            (manifest["batch_id"], entry["dataset"]),
        ).fetchall()
        restored = [_record_from_row(row) for row in restored_rows]
        restored_raw = b"".join(_record_line(record) for record in restored)
        if len(restored) != entry["row_count"] or _sha256(restored_raw) != _sha256(raw):
            raise RehearsalError("restored rows do not match the sealed artifact")
        connection.commit()
    except Exception:
        connection.rollback()
        raise
    finally:
        connection.close()

    return {
        "schema_version": SCHEMA_VERSION,
        "mode": "nonprod_random_restore",
        "batch_id": manifest["batch_id"],
        "seed": seed,
        "selected_dataset": entry["dataset"],
        "expected_rows": entry["row_count"],
        "restored_rows": len(records),
        "inserted_rows": inserted,
        "logical_sha256": entry["logical_sha256"],
        "verified": True,
        "idempotent_reuse": inserted == 0,
        "deletion_authorized": False,
    }


def _postgres_fetch_restored(
    target_dsn: str,
    *,
    batch_id: str,
    dataset: str,
    timeout_seconds: int,
) -> list[dict[str, Any]]:
    lines = _run_psql(
        target_dsn,
        "SELECT json_build_object("
        "'dataset', dataset, 'record_id', record_id, 'created_at', created_at, "
        "'payload', payload_json::jsonb)::text "
        "FROM archive_rehearsal_restored "
        f"WHERE batch_id = {_sql_literal(batch_id)} "
        f"AND dataset = {_sql_literal(dataset)} ORDER BY created_at, record_id",
        timeout_seconds=timeout_seconds,
        read_only=True,
    )
    restored: list[dict[str, Any]] = []
    for line in lines:
        try:
            value = json.loads(line, parse_constant=_reject_json_constant)
            restored.append(
                _record_from_row(
                    (
                        value.get("dataset"),
                        value.get("record_id"),
                        value.get("created_at"),
                        _canonical_json(value.get("payload")),
                    )
                )
            )
        except (TypeError, ValueError, json.JSONDecodeError, RehearsalError) as exc:
            raise RehearsalError("target PostgreSQL restore rows are invalid") from exc
    return restored


def restore_postgres_random(
    batch: str | os.PathLike[str],
    target_dsn: str,
    *,
    seed: int,
    timeout_seconds: int = DEFAULT_POSTGRES_TIMEOUT_SECONDS,
) -> dict[str, Any]:
    started = time.monotonic()
    verification = verify_batch(batch)
    if verification.get("source_kind") not in {
        POSTGRES_SOURCE_KIND,
        PROD_CANARY_SOURCE_KIND,
    }:
        raise RehearsalError("restore-postgres-random requires a PostgreSQL batch")
    batch_dir = pathlib.Path(batch).expanduser().resolve()
    manifest_bytes = (batch_dir / "manifest.json").read_bytes()
    if _sha256(manifest_bytes) != verification["manifest_sha256"]:
        raise RehearsalError("manifest changed after verification")
    manifest = json.loads(manifest_bytes)
    target_info = _postgres_dsn_info(target_dsn, target=True)
    if target_info["database"] == manifest.get("source_database"):
        raise RehearsalError("restore target must be an independent PostgreSQL database")
    entry = random.Random(seed).choice(manifest["artifacts"])
    records, raw = _parse_artifact(batch_dir, entry)

    _run_psql(
        target_dsn,
        "CREATE TABLE IF NOT EXISTS archive_rehearsal_restored ("
        "batch_id TEXT NOT NULL, dataset TEXT NOT NULL, record_id TEXT NOT NULL, "
        "created_at TEXT NOT NULL, payload_json TEXT NOT NULL, "
        "PRIMARY KEY (batch_id, dataset, record_id))",
        timeout_seconds=timeout_seconds,
        read_only=False,
    )
    existing = _postgres_fetch_restored(
        target_dsn,
        batch_id=manifest["batch_id"],
        dataset=entry["dataset"],
        timeout_seconds=timeout_seconds,
    )
    expected_by_key = {
        (record["dataset"], record["record_id"]): record for record in records
    }
    existing_by_key = {
        (record["dataset"], record["record_id"]): record for record in existing
    }
    if (
        any(
            existing_by_key.get(key) != value
            for key, value in expected_by_key.items()
            if key in existing_by_key
        )
        or any(key not in expected_by_key for key in existing_by_key)
        or len(existing) > len(records)
    ):
        raise RehearsalError("restored rows do not match the sealed artifact")
    missing = [
        record
        for key, record in expected_by_key.items()
        if key not in existing_by_key
    ]
    inserted = 0
    if missing:
        statements = []
        for record in missing:
            statements.append(
                "INSERT INTO archive_rehearsal_restored "
                "(batch_id, dataset, record_id, created_at, payload_json) VALUES ("
                f"{_sql_literal(manifest['batch_id'])}, "
                f"{_sql_literal(record['dataset'])}, "
                f"{_sql_literal(record['record_id'])}, "
                f"{_sql_literal(record['created_at'])}, "
                f"{_sql_literal(_canonical_json(record['payload']))}) "
                "ON CONFLICT (batch_id, dataset, record_id) DO NOTHING RETURNING 1"
            )
        inserted = len(
            _run_psql(
                target_dsn,
                "; ".join(statements),
                timeout_seconds=timeout_seconds,
                read_only=False,
            )
        )

    restored = _postgres_fetch_restored(
        target_dsn,
        batch_id=manifest["batch_id"],
        dataset=entry["dataset"],
        timeout_seconds=timeout_seconds,
    )
    restored_raw = b"".join(_record_line(record) for record in restored)
    if len(restored) != entry["row_count"] or _sha256(restored_raw) != _sha256(raw):
        raise RehearsalError("restored rows do not match the sealed artifact")
    metrics = {
        **_compression_metrics([entry]),
        "restore_elapsed_ms": round((time.monotonic() - started) * 1000, 3),
        "expected_rows": entry["row_count"],
        "restored_rows": len(restored),
    }
    return {
        "schema_version": SCHEMA_VERSION,
        "mode": (
            "prod_archive_export_canary_postgres_restore"
            if verification.get("source_kind") == PROD_CANARY_SOURCE_KIND
            else "nonprod_postgres_random_restore"
        ),
        "batch_id": manifest["batch_id"],
        "target_database": target_info["database"],
        "seed": seed,
        "selected_dataset": entry["dataset"],
        "expected_rows": entry["row_count"],
        "restored_rows": len(restored),
        "inserted_rows": inserted,
        "logical_sha256": entry["logical_sha256"],
        "verified": True,
        "idempotent_reuse": inserted == 0,
        "metrics": metrics,
        "deletion_authorized": False,
    }


def snapshot_postgres(
    source_dsn: str,
    target_dsn: str,
    archive_root: str | os.PathLike[str],
    *,
    as_of: str,
    seed: int,
    retention_days: dict[str, int],
    timeout_seconds: int = DEFAULT_POSTGRES_TIMEOUT_SECONDS,
    max_rows: int = DEFAULT_POSTGRES_MAX_ROWS,
) -> dict[str, Any]:
    started = time.monotonic()
    source_info = _postgres_dsn_info(source_dsn, target=False)
    target_info = _postgres_dsn_info(target_dsn, target=True)
    if target_info["database"] == source_info["database"]:
        raise RehearsalError("restore target must be independent of the source database")
    dry_report, _, query_info = postgres_dry_run(
        source_dsn,
        as_of=as_of,
        retention_days=retention_days,
        timeout_seconds=timeout_seconds,
        max_rows=max_rows,
    )
    sealed = seal_postgres_batch(
        source_dsn,
        archive_root,
        as_of=as_of,
        retention_days=retention_days,
        timeout_seconds=timeout_seconds,
        max_rows=max_rows,
    )
    verified = verify_batch(sealed["batch_dir"])
    restored = restore_postgres_random(
        sealed["batch_dir"],
        target_dsn,
        seed=seed,
        timeout_seconds=timeout_seconds,
    )
    return {
        "schema_version": SCHEMA_VERSION,
        "mode": "nonprod_postgres_archive_rehearsal",
        "environment": "nonprod",
        "source_database": source_info["database"],
        "target_database": target_info["database"],
        "dry_run": dry_report,
        "seal": {
            "batch_id": sealed["batch_id"],
            "batch_dir": sealed["batch_dir"],
            "total_rows": sealed["total_rows"],
            "metrics": sealed.get("metrics", {}),
            "idempotent_reuse": sealed["idempotent_reuse"],
        },
        "verify": verified,
        "restore": restored,
        "metrics": {
            "total_elapsed_ms": round((time.monotonic() - started) * 1000, 3),
            "source_rows": dry_report["source_rows"],
            "candidate_rows": dry_report["candidate_rows"],
            "logical_bytes": sealed["metrics"]["logical_bytes"],
            "artifact_bytes": sealed["metrics"]["artifact_bytes"],
            "compression_ratio": sealed["metrics"]["compression_ratio"],
            "query_elapsed_ms": query_info["query_elapsed_ms"],
            "restore_verified": restored["verified"],
        },
        "source_mutated": False,
        "deletion_authorized": False,
    }


def _guard_output(
    output: str | None,
    *,
    protected_files: Iterable[pathlib.Path] = (),
    protected_dirs: Iterable[pathlib.Path] = (),
) -> pathlib.Path | None:
    if not output:
        return None
    path = _local_path(output, must_exist=False)
    files = {item.expanduser().resolve() for item in protected_files}
    directories = {item.expanduser().resolve() for item in protected_dirs}
    if path in files or any(
        directory == path or directory in path.parents for directory in directories
    ):
        raise RehearsalError(f"output path overlaps protected rehearsal input: {path}")
    return path


def _write_or_print(payload: dict[str, Any], output: pathlib.Path | None) -> None:
    if output:
        _atomic_json(output, payload)
    print(_canonical_json(payload))


def _add_common(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--source", required=True, help="local SQLite source file")
    parser.add_argument("--environment", choices=ENVIRONMENTS, required=True)
    parser.add_argument("--as-of", required=True, help="timezone-aware sealing waterline")
    parser.add_argument(
        "--usage-retention-days", type=int, default=DEFAULT_RETENTION_DAYS["usage"]
    )
    parser.add_argument(
        "--ops-retention-days", type=int, default=DEFAULT_RETENTION_DAYS["ops"]
    )
    parser.add_argument(
        "--qa-retention-days", type=int, default=DEFAULT_RETENTION_DAYS["qa"]
    )


def _add_postgres_common(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "--environment",
        choices=("nonprod",),
        default="nonprod",
        help="PostgreSQL rehearsal is intentionally nonprod-only",
    )
    parser.add_argument(
        "--source-dsn",
        "--dsn",
        dest="source_dsn",
        required=True,
        help="localhost PostgreSQL rehearsal DSN",
    )
    parser.add_argument(
        "--target-dsn",
        "--target",
        dest="target_dsn",
        required=True,
        help="independent local restore database DSN",
    )
    parser.add_argument("--archive-root", required=True)
    parser.add_argument("--as-of", required=True, help="timezone-aware sealing waterline")
    parser.add_argument("--seed", type=int, required=True)
    parser.add_argument(
        "--timeout-seconds",
        type=int,
        default=DEFAULT_POSTGRES_TIMEOUT_SECONDS,
    )
    parser.add_argument(
        "--max-rows", type=int, default=DEFAULT_POSTGRES_MAX_ROWS
    )
    parser.add_argument(
        "--usage-retention-days", type=int, default=DEFAULT_RETENTION_DAYS["usage"]
    )
    parser.add_argument(
        "--ops-retention-days", type=int, default=DEFAULT_RETENTION_DAYS["ops"]
    )
    parser.add_argument(
        "--qa-retention-days", type=int, default=DEFAULT_RETENTION_DAYS["qa"]
    )
    parser.add_argument("--output")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    dry = commands.add_parser("dry-run", help="report cold candidates without export")
    _add_common(dry)
    dry.add_argument("--output")
    seal = commands.add_parser("seal", help="seal a deterministic nonprod batch")
    _add_common(seal)
    seal.add_argument("--archive-root", required=True)
    seal.add_argument("--output")
    verify = commands.add_parser("verify", help="verify manifest, rows, and checksums")
    verify.add_argument("--batch", required=True)
    verify.add_argument("--output")
    restore = commands.add_parser(
        "restore-random", help="restore one seeded random artifact into local SQLite"
    )
    restore.add_argument("--batch", required=True)
    restore.add_argument("--target", required=True)
    restore.add_argument("--seed", type=int, required=True)
    restore.add_argument("--receipt")
    postgres = commands.add_parser(
        "snapshot-postgres",
        help="run the isolated non-production PostgreSQL rehearsal end to end",
    )
    _add_postgres_common(postgres)
    return parser


def _policy(args: argparse.Namespace) -> dict[str, int]:
    return retention_policy(
        args.usage_retention_days, args.ops_retention_days, args.qa_retention_days
    )


def main(argv: Iterable[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "dry-run":
            output = _guard_output(
                args.output,
                protected_files=(_local_path(args.source, must_exist=True),),
            )
            payload = dry_run(
                args.source,
                environment=args.environment,
                as_of=args.as_of,
                retention_days=_policy(args),
            )
            _write_or_print(payload, output)
        elif args.command == "seal":
            output = _guard_output(
                args.output,
                protected_files=(_local_path(args.source, must_exist=True),),
            )
            payload = seal_batch(
                args.source,
                args.archive_root,
                environment=args.environment,
                as_of=args.as_of,
                retention_days=_policy(args),
            )
            output = _guard_output(
                str(output) if output else None,
                protected_dirs=(pathlib.Path(payload["batch_dir"]),),
            )
            _write_or_print(payload, output)
        elif args.command == "verify":
            output = _guard_output(
                args.output,
                protected_dirs=(pathlib.Path(args.batch),),
            )
            _write_or_print(verify_batch(args.batch), output)
        elif args.command == "restore-random":
            output = _guard_output(
                args.receipt,
                protected_files=(_local_path(args.target, must_exist=False),),
                protected_dirs=(pathlib.Path(args.batch),),
            )
            _write_or_print(
                restore_random(args.batch, args.target, seed=args.seed), output
            )
        elif args.command == "snapshot-postgres":
            _postgres_dsn_info(args.source_dsn, target=False)
            _postgres_dsn_info(args.target_dsn, target=True)
            output = _guard_output(args.output, protected_dirs=(pathlib.Path(args.archive_root),))
            payload = snapshot_postgres(
                args.source_dsn,
                args.target_dsn,
                args.archive_root,
                as_of=args.as_of,
                seed=args.seed,
                retention_days=_policy(args),
                timeout_seconds=args.timeout_seconds,
                max_rows=args.max_rows,
            )
            _write_or_print(payload, output)
        else:  # pragma: no cover - argparse owns the command space.
            parser.error(f"unknown command {args.command}")
    except (OSError, RehearsalError, sqlite3.Error) as exc:
        print(f"archive rehearsal refused: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
