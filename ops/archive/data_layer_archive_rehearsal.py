#!/usr/bin/env python3
"""Local/non-production archive and restore rehearsal.

The rehearsal intentionally accepts only local SQLite files. Source databases
are opened read-only and the CLI has no purge/delete operation. Production
PostgreSQL, object storage, and runtime workflow integration belong to later,
separately approved phases.
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
import sqlite3
import sys
import tempfile
from typing import Any, Iterable
from urllib.parse import quote

SCHEMA_VERSION = 1
SOURCE_TABLE = "archive_rehearsal_records"
RESTORE_TABLE = "archive_rehearsal_restored"
DATASETS = ("usage", "ops", "qa")
DEFAULT_RETENTION_DAYS = {"usage": 90, "ops": 30, "qa": 2}
ENVIRONMENTS = ("local", "nonprod")


class RehearsalError(ValueError):
    """Fail-closed validation error for a rehearsal artifact or operation."""


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
    source_file_identity: dict[str, int],
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


def _batch_id(
    *,
    environment: str,
    sealed_at: str,
    source_path_sha256: str,
    source_file_identity: dict[str, int],
    retention_days: dict[str, int],
    artifacts: list[dict[str, Any]],
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
    return f"rehearsal-{stamp}-{digest}"


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
    if manifest.get("mode") != "nonprod_archive_rehearsal":
        raise RehearsalError("manifest is not a non-production rehearsal")
    if manifest.get("environment") not in ENVIRONMENTS:
        raise RehearsalError("manifest environment is not local/nonprod")
    if manifest.get("source_kind") != "local_sqlite_read_only":
        raise RehearsalError("manifest source is not a read-only local SQLite snapshot")
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
    if (
        not isinstance(source_path_sha256, str)
        or len(source_path_sha256) != 64
        or any(character not in "0123456789abcdef" for character in source_path_sha256)
    ):
        raise RehearsalError("manifest source_path_sha256 is invalid")
    source_file_identity = manifest.get("source_file_identity")
    if (
        not isinstance(source_file_identity, dict)
        or set(source_file_identity) != {"device", "inode"}
        or any(
            not isinstance(value, int) or isinstance(value, bool) or value < 0
            for value in source_file_identity.values()
        )
    ):
        raise RehearsalError("manifest source_file_identity is invalid")
    policy = manifest.get("retention_days")
    if not isinstance(policy, dict):
        raise RehearsalError("manifest retention_days is invalid")
    try:
        normalized_policy = retention_policy(policy["usage"], policy["ops"], policy["qa"])
    except (KeyError, TypeError) as exc:
        raise RehearsalError("manifest retention_days is invalid") from exc
    if policy != normalized_policy:
        raise RehearsalError("manifest retention_days is not canonical")
    artifacts = manifest.get("artifacts")
    if not isinstance(artifacts, list) or not artifacts:
        raise RehearsalError("manifest must contain at least one artifact")
    artifact_datasets = [
        entry.get("dataset") if isinstance(entry, dict) else None for entry in artifacts
    ]
    if artifact_datasets != [dataset for dataset in DATASETS if dataset in artifact_datasets]:
        raise RehearsalError("manifest artifacts are not in canonical dataset order")
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
        total_rows += len(records)
        total_bytes += len(raw)
    if total_rows != manifest.get("total_rows"):
        raise RehearsalError("manifest total_rows mismatch")
    if total_bytes != manifest.get("total_logical_bytes"):
        raise RehearsalError("manifest total_logical_bytes mismatch")
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
            source_path_sha256=source_path_sha256,
            source_file_identity=source_file_identity,
            retention_days=normalized_policy,
            artifacts=artifacts,
        )
    except (KeyError, TypeError) as exc:
        raise RehearsalError("manifest artifact identity fields are invalid") from exc
    if manifest.get("batch_id") != expected_batch_id:
        raise RehearsalError("manifest batch identity does not match its contents")
    return {
        "schema_version": SCHEMA_VERSION,
        "mode": "nonprod_archive_verify",
        "batch_id": manifest.get("batch_id"),
        "verified": True,
        "artifact_count": len(artifacts),
        "row_count": total_rows,
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
        else:  # pragma: no cover - argparse owns the command space.
            parser.error(f"unknown command {args.command}")
    except (OSError, RehearsalError, sqlite3.Error) as exc:
        print(f"archive rehearsal refused: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
