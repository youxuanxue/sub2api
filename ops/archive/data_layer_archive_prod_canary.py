#!/usr/bin/env python3
"""Bounded production export-only archive canary.

The operator entry resolves the fixed Stage0 prod instance, runs one read-only
ops export through SSM, stages the sealed batch in the existing encrypted
pgdump bucket, then downloads and restores it into an independent local
PostgreSQL database. It has no source deletion or partition-management path.
"""

from __future__ import annotations

import argparse
import base64
import datetime as dt
import gzip
import io
import json
import os
import pathlib
import selectors
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
from collections.abc import Callable, Iterable
from typing import Any
from urllib.parse import urlparse

import data_layer_archive_cleanup_hold as cleanup_hold
import data_layer_archive_cleanup_hold_remote as cleanup_hold_remote
import data_layer_archive_rehearsal as rehearsal


PROD_REGION = "us-east-1"
PROD_STACK = rehearsal.PROD_CANARY_STACK
BACKUP_STACK = "tokenkey-stage0-backups"
BACKUP_BUCKET_OUTPUT = "BucketName"
BACKUP_RETENTION_DAYS = 7
PROD_CONFIRMATION = "tokenkey-prod-archive-export-only-v1"
S3_KEY_BASE = "prod/pgdump/archive-canary"
AWS_COMMAND_TIMEOUT_SECONDS = 300
SSM_PARAMETERS_MAX_BYTES = 20 * 1024
OPS_RETENTION_DAYS = 30
LOCK_TIMEOUT_MS = 100
DEFAULT_TIMEOUT_SECONDS = 20
HARD_TIMEOUT_SECONDS = 30
DEFAULT_MAX_ROWS = 1_000
HARD_MAX_ROWS = 10_000
DEFAULT_MAX_LOGICAL_BYTES = 16 * 1024 * 1024
HARD_MAX_LOGICAL_BYTES = 64 * 1024 * 1024
MAX_CLOCK_SKEW_SECONDS = 600
TERMINAL_SSM_STATUSES = {"Success", "Cancelled", "TimedOut", "Failed"}

QueryRunner = Callable[[str, int, int], list[str]]
CommandRunner = Callable[[list[str]], str]


class CanaryError(rehearsal.RehearsalError):
    """Fail-closed production canary error."""


def _canonical_timestamp(value: str) -> str:
    return rehearsal._timestamp(rehearsal._utc(value))


def _validated_request(
    *,
    table: str,
    as_of: str,
    timeout_seconds: int,
    max_rows: int,
    max_logical_bytes: int,
) -> dict[str, Any]:
    if table not in rehearsal.PROD_CANARY_TABLES:
        raise CanaryError(f"table must be one of {rehearsal.PROD_CANARY_TABLES}")
    if (
        isinstance(timeout_seconds, bool)
        or not isinstance(timeout_seconds, int)
        or not 1 <= timeout_seconds <= HARD_TIMEOUT_SECONDS
    ):
        raise CanaryError(
            f"timeout_seconds must be between 1 and {HARD_TIMEOUT_SECONDS}"
        )
    if (
        isinstance(max_rows, bool)
        or not isinstance(max_rows, int)
        or not 1 <= max_rows <= HARD_MAX_ROWS
    ):
        raise CanaryError(f"max_rows must be between 1 and {HARD_MAX_ROWS}")
    if (
        isinstance(max_logical_bytes, bool)
        or not isinstance(max_logical_bytes, int)
        or not 1 <= max_logical_bytes <= HARD_MAX_LOGICAL_BYTES
    ):
        raise CanaryError(
            "max_logical_bytes must be between 1 and "
            f"{HARD_MAX_LOGICAL_BYTES}"
        )
    normalized_as_of = _canonical_timestamp(as_of)
    cutoff = rehearsal._timestamp(
        rehearsal._utc(normalized_as_of) - dt.timedelta(days=OPS_RETENTION_DAYS)
    )
    return {
        "table": table,
        "as_of": normalized_as_of,
        "cutoff_exclusive": cutoff,
        "timeout_seconds": timeout_seconds,
        "max_rows": max_rows,
        "max_logical_bytes": max_logical_bytes,
    }


def build_plan(
    *,
    table: str,
    as_of: str,
    timeout_seconds: int,
    max_rows: int,
    max_logical_bytes: int,
) -> dict[str, Any]:
    request = _validated_request(
        table=table,
        as_of=as_of,
        timeout_seconds=timeout_seconds,
        max_rows=max_rows,
        max_logical_bytes=max_logical_bytes,
    )
    return {
        "schema_version": rehearsal.SCHEMA_VERSION,
        "mode": "prod_archive_export_canary_plan",
        "environment": "prod",
        "region": PROD_REGION,
        "stack": PROD_STACK,
        "backup_stack": BACKUP_STACK,
        "source_container": rehearsal.PROD_CANARY_CONTAINER,
        "source_database": rehearsal.PROD_CANARY_DATABASE,
        "retention_days": OPS_RETENTION_DAYS,
        "lock_timeout_ms": LOCK_TIMEOUT_MS,
        "staging_key_base": S3_KEY_BASE,
        "staging_retention_days": BACKUP_RETENTION_DAYS,
        "source_mutated": False,
        "deletion_authorized": False,
        "execution_authorized": False,
        "cleanup_hold_required": True,
        "required_confirmation": PROD_CONFIRMATION,
        **request,
    }


def _validated_source_key(key: dict[str, Any], *, table: str) -> dict[str, Any]:
    if not isinstance(key, dict) or set(key) != {"created_at", "id"}:
        raise CanaryError("production source key is invalid")
    created_at = _canonical_timestamp(str(key["created_at"]))
    source_id = key["id"]
    if isinstance(source_id, bool) or not isinstance(source_id, int):
        raise CanaryError("production source key id is invalid")
    if source_id <= 0 or source_id > rehearsal.POSTGRES_BIGINT_MAX:
        raise CanaryError("production source key id is out of range")
    record_id = rehearsal._prod_canary_record_id(table, str(source_id))
    return {"created_at": created_at, "id": source_id, "record_id": record_id}


def _cursor_predicate(cursor_after: dict[str, Any] | None, *, table: str) -> str:
    if cursor_after is None:
        return ""
    key = _validated_source_key(cursor_after, table=table)
    return (
        " AND (created_at, id) > ("
        f"{rehearsal._sql_literal(key['created_at'])}::timestamptz, "
        f"{key['id']}::bigint)"
    )


def _stage0_record_query(
    table: str,
    *,
    cutoff: str,
    limit: int,
    cursor_after: dict[str, Any] | None = None,
    upper_exclusive: str | None = None,
) -> str:
    if table not in rehearsal.PROD_CANARY_TABLES:
        raise CanaryError(f"unsupported production canary table {table!r}")
    upper_clause = ""
    if upper_exclusive is not None:
        upper_clause = (
            f" AND created_at < {rehearsal._sql_literal(upper_exclusive)}::timestamptz"
        )
    return (
        "SELECT json_build_object("
        "'dataset', 'ops', "
        "'record_id', row_data.id::text, "
        "'created_at', to_char(row_data.created_at AT TIME ZONE 'UTC', "
        "'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'), "
        "'payload', to_jsonb(row_data)"
        ")::text "
        f"FROM (SELECT * FROM {table} "
        f"WHERE created_at < {rehearsal._sql_literal(cutoff)}::timestamptz"
        f"{upper_clause}{_cursor_predicate(cursor_after, table=table)} "
        f"ORDER BY created_at, id LIMIT {limit}) AS row_data"
    )


def _bounded_command_output(
    command: list[str], *, timeout_seconds: int, output_limit: int
) -> tuple[bytes, bytes, int]:
    try:
        process = subprocess.Popen(
            command,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
    except OSError as exc:
        raise CanaryError(f"command unavailable: {command[0]}") from exc
    if process.stdout is None or process.stderr is None:  # pragma: no cover
        process.kill()
        process.wait()
        raise CanaryError("bounded command pipes are unavailable")

    deadline = time.monotonic() + timeout_seconds
    stdout = bytearray()
    stderr = bytearray()
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ, "stdout")
    selector.register(process.stderr, selectors.EVENT_READ, "stderr")
    try:
        while selector.get_map():
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise CanaryError("production canary PostgreSQL statement timed out")
            events = selector.select(timeout=remaining)
            if not events:
                continue
            for key, _ in events:
                chunk = os.read(key.fileobj.fileno(), 64 * 1024)
                if not chunk:
                    selector.unregister(key.fileobj)
                    continue
                if key.data == "stdout":
                    stdout.extend(chunk)
                    if len(stdout) > output_limit:
                        raise CanaryError(
                            f"production canary query output exceeds {output_limit} bytes"
                        )
                elif len(stderr) < 4096:
                    stderr.extend(chunk[: 4096 - len(stderr)])
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise CanaryError("production canary PostgreSQL statement timed out")
        try:
            returncode = process.wait(timeout=remaining)
        except subprocess.TimeoutExpired as exc:
            raise CanaryError(
                "production canary PostgreSQL statement timed out"
            ) from exc
    finally:
        selector.close()
        process.stdout.close()
        process.stderr.close()
        if process.poll() is None:
            process.kill()
            process.wait()
    return bytes(stdout), bytes(stderr), returncode


def _run_stage0_psql(sql: str, timeout_seconds: int, output_limit: int) -> list[str]:
    if shutil.which("docker") is None:
        raise CanaryError("docker is required on the Stage0 production host")
    pgoptions = (
        "-c default_transaction_read_only=on "
        f"-c lock_timeout={LOCK_TIMEOUT_MS}ms "
        f"-c statement_timeout={timeout_seconds}s"
    )
    wrapped = (
        "BEGIN READ ONLY; "
        f"SET LOCAL lock_timeout = '{LOCK_TIMEOUT_MS}ms'; "
        f"SET LOCAL statement_timeout = '{timeout_seconds}s'; "
        f"{sql}; COMMIT;"
    )
    command = [
        "docker",
        "exec",
        "-i",
        "-e",
        f"PGOPTIONS={pgoptions}",
        rehearsal.PROD_CANARY_CONTAINER,
        "psql",
        "-U",
        "tokenkey",
        "-d",
        rehearsal.PROD_CANARY_DATABASE,
        "-X",
        "-q",
        "-t",
        "-A",
        "-P",
        "pager=off",
        "-v",
        "ON_ERROR_STOP=1",
        "-c",
        wrapped,
    ]
    raw_stdout, raw_stderr, returncode = _bounded_command_output(
        command,
        timeout_seconds=timeout_seconds + 5,
        output_limit=output_limit,
    )
    if returncode != 0:
        detail = raw_stderr.decode("utf-8", errors="replace").strip()
        raise CanaryError(f"production canary PostgreSQL query failed: {detail[:400]}")
    try:
        decoded = raw_stdout.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise CanaryError("production canary PostgreSQL output is not UTF-8") from exc
    return [line for line in decoded.splitlines() if line]


def _collect_prod_candidates(
    query_runner: QueryRunner,
    *,
    request: dict[str, Any],
    cursor_after: dict[str, Any] | None = None,
    upper_exclusive: str | None = None,
) -> tuple[dict[str, list[dict[str, Any]]], dict[str, Any]]:
    started = time.monotonic()
    probe_sql = (
        "SELECT json_build_object("
        "'database', current_database(), "
        "'read_only', current_setting('transaction_read_only'), "
        "'server_clock', to_char(clock_timestamp() AT TIME ZONE 'UTC', "
        "'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"')"
        ")::text"
    )
    probe_lines = query_runner(probe_sql, request["timeout_seconds"], 4096)
    if len(probe_lines) != 1:
        raise CanaryError("production source proof returned an unexpected row count")
    try:
        proof = json.loads(probe_lines[0])
        if not isinstance(proof, dict) or set(proof) != {
            "database",
            "read_only",
            "server_clock",
        }:
            raise TypeError("production source proof must be an exact JSON object")
        server_clock = _canonical_timestamp(proof["server_clock"])
    except (KeyError, TypeError, ValueError, json.JSONDecodeError) as exc:
        raise CanaryError("production source proof is invalid") from exc
    if proof.get("database") != rehearsal.PROD_CANARY_DATABASE:
        raise CanaryError("production canary connected to the wrong database")
    if proof.get("read_only") != "on":
        raise CanaryError("production canary source session is not read-only")
    as_of = request.get("as_of")
    if as_of is None:
        as_of = server_clock
    else:
        skew = abs(
            (
                rehearsal._utc(as_of)
                - rehearsal._utc(server_clock)
            ).total_seconds()
        )
        if skew > MAX_CLOCK_SKEW_SECONDS:
            raise CanaryError("production canary as_of is stale relative to database clock")
    cutoff_exclusive = rehearsal._timestamp(
        rehearsal._utc(server_clock) - dt.timedelta(days=OPS_RETENTION_DAYS)
    )

    output_limit = min(
        HARD_MAX_LOGICAL_BYTES * 2,
        request["max_logical_bytes"] * 2 + 1024 * 1024,
    )
    lines = query_runner(
        _stage0_record_query(
            request["table"],
            cutoff=cutoff_exclusive,
            limit=request["max_rows"] + 1,
            cursor_after=cursor_after,
            upper_exclusive=upper_exclusive,
        ),
        request["timeout_seconds"],
        output_limit,
    )
    more_cold_rows_after_sample = len(lines) > request["max_rows"]
    lines = lines[: request["max_rows"]]
    if not lines:
        raise CanaryError("production canary found no cold rows")

    records: list[dict[str, Any]] = []
    for line in lines:
        try:
            value = json.loads(line, parse_constant=rehearsal._reject_json_constant)
            if (
                not isinstance(value, dict)
                or set(value) != {"dataset", "record_id", "created_at", "payload"}
                or value.get("dataset") != "ops"
                or not isinstance(value.get("record_id"), str)
                or not value["record_id"]
                or not isinstance(value.get("created_at"), str)
            ):
                raise TypeError("production source row must be an exact ops record")
            record = rehearsal._record_from_row(
                (
                    "ops",
                    rehearsal._prod_canary_record_id(
                        request["table"], value["record_id"]
                    ),
                    value.get("created_at"),
                    rehearsal._canonical_json(value.get("payload")),
                )
            )
        except (
            TypeError,
            ValueError,
            json.JSONDecodeError,
            rehearsal.RehearsalError,
        ) as exc:
            raise CanaryError("production canary returned an invalid source row") from exc
        if rehearsal._utc(record["created_at"]) >= rehearsal._utc(cutoff_exclusive):
            raise CanaryError("production canary source returned a hot row")
        records.append(record)
    records.sort(key=lambda item: (item["created_at"], item["record_id"]))
    keys = [(item["created_at"], item["record_id"]) for item in records]
    if len(keys) != len(set(keys)):
        raise CanaryError("production canary source returned duplicate rows")
    logical_bytes = sum(len(rehearsal._record_line(record)) for record in records)
    if logical_bytes > request["max_logical_bytes"]:
        raise CanaryError(
            "production canary logical bytes exceed "
            f"max_logical_bytes={request['max_logical_bytes']}"
        )
    candidates = {dataset: [] for dataset in rehearsal.DATASETS}
    candidates["ops"] = records
    first_key = rehearsal._prod_canary_source_key(records[0], request["table"])
    last_key = rehearsal._prod_canary_source_key(records[-1], request["table"])
    return candidates, {
        "server_clock": server_clock,
        "cutoff_exclusive": cutoff_exclusive,
        "query_elapsed_ms": round((time.monotonic() - started) * 1000, 3),
        "candidate_rows": len(records),
        "candidate_logical_bytes": logical_bytes,
        "sample_first_key": first_key,
        "sample_last_key": last_key,
        "more_cold_rows_after_sample": more_cold_rows_after_sample,
        "cursor_before": cursor_after,
        "cursor_after": last_key,
        "upper_exclusive": upper_exclusive,
    }


def _s3_location(uri: str) -> tuple[str, str]:
    parsed = urlparse(uri)
    key = parsed.path.lstrip("/").rstrip("/")
    if (
        parsed.scheme != "s3"
        or not parsed.netloc
        or not key
        or parsed.params
        or parsed.query
        or parsed.fragment
        or ".." in key.split("/")
    ):
        raise CanaryError(f"invalid S3 URI: {uri!r}")
    return parsed.netloc, key


def _validated_staging_base(uri: str) -> str:
    bucket, key = _s3_location(uri)
    if key != S3_KEY_BASE:
        raise CanaryError(f"S3 staging key must be exactly {S3_KEY_BASE}")
    return f"s3://{bucket}/{key}"


def seal_prod_canary_batch(
    archive_root: str | os.PathLike[str],
    *,
    table: str,
    as_of: str,
    instance_id: str,
    staging_s3_base_uri: str,
    query_runner: QueryRunner = _run_stage0_psql,
    timeout_seconds: int = DEFAULT_TIMEOUT_SECONDS,
    max_rows: int = DEFAULT_MAX_ROWS,
    max_logical_bytes: int = DEFAULT_MAX_LOGICAL_BYTES,
) -> dict[str, Any]:
    request = _validated_request(
        table=table,
        as_of=as_of,
        timeout_seconds=timeout_seconds,
        max_rows=max_rows,
        max_logical_bytes=max_logical_bytes,
    )
    if (
        not isinstance(instance_id, str)
        or not instance_id.startswith("i-")
        or len(instance_id) != 19
        or any(character not in "0123456789abcdef" for character in instance_id[2:])
    ):
        raise CanaryError("production instance id is invalid")
    staging_base = _validated_staging_base(staging_s3_base_uri)
    candidates, metrics = _collect_prod_candidates(query_runner, request=request)
    artifacts_preview = [rehearsal._artifact_entry("ops", candidates["ops"])[0]]
    source_identity = {
        "container": rehearsal.PROD_CANARY_CONTAINER,
        "database": rehearsal.PROD_CANARY_DATABASE,
        "instance_id": instance_id,
        "stack": PROD_STACK,
        "table": table,
    }
    source_identity_sha256 = rehearsal._sha256(
        rehearsal._canonical_json(source_identity).encode("utf-8")
    )
    sealed_at = metrics["server_clock"]
    batch_id = rehearsal._batch_id(
        environment="prod",
        sealed_at=sealed_at,
        source_path_sha256=source_identity_sha256,
        source_file_identity=source_identity,
        retention_days=dict(rehearsal.DEFAULT_RETENTION_DAYS),
        artifacts=artifacts_preview,
        prefix="prod-canary",
    )
    manifest = {
        "schema_version": rehearsal.SCHEMA_VERSION,
        "mode": rehearsal.PROD_CANARY_MODE,
        "environment": "prod",
        "batch_id": batch_id,
        "sealed_at": sealed_at,
        "source_kind": rehearsal.PROD_CANARY_SOURCE_KIND,
        "source_database": rehearsal.PROD_CANARY_DATABASE,
        "source_identity_sha256": source_identity_sha256,
        "source_file_identity": source_identity,
        "retention_days": dict(rehearsal.DEFAULT_RETENTION_DAYS),
        "source_rows": metrics["candidate_rows"],
        "staging_s3_prefix": f"{staging_base}/{batch_id}",
        "canary": {
            "table": table,
            "cutoff_exclusive": metrics["cutoff_exclusive"],
            "server_clock": metrics["server_clock"],
            "lock_timeout_ms": LOCK_TIMEOUT_MS,
            "statement_timeout_seconds": timeout_seconds,
            "max_rows": max_rows,
            "max_logical_bytes": max_logical_bytes,
            "query_elapsed_ms": metrics["query_elapsed_ms"],
            "selection_order": ["created_at", "id"],
            "sample_first_key": metrics["sample_first_key"],
            "sample_last_key": metrics["sample_last_key"],
            "more_cold_rows_after_sample": metrics["more_cold_rows_after_sample"],
        },
        "source_mutated": False,
        "deletion_authorized": False,
    }
    return rehearsal._write_sealed_batch(
        candidates=candidates,
        archive_root=archive_root,
        manifest=manifest,
    )


def _command_output(args: list[str]) -> str:
    try:
        completed = subprocess.run(
            args,
            capture_output=True,
            text=True,
            timeout=AWS_COMMAND_TIMEOUT_SECONDS,
            check=False,
        )
    except subprocess.TimeoutExpired as exc:
        raise CanaryError(f"command timed out: {args[0]}") from exc
    except OSError as exc:
        raise CanaryError(f"command unavailable: {args[0]}") from exc
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout or "command failed").strip()
        raise CanaryError(f"command failed ({args[0]}): {detail[:400]}")
    return completed.stdout


def _head_s3_object(
    uri: str,
    *,
    expected_bytes: int,
    expected_sha256: str,
    command_runner: CommandRunner,
) -> dict[str, Any]:
    bucket, key = _s3_location(uri)
    try:
        head = json.loads(
            command_runner(
                [
                    "aws",
                    "s3api",
                    "head-object",
                    "--region",
                    PROD_REGION,
                    "--bucket",
                    bucket,
                    "--key",
                    key,
                    "--output",
                    "json",
                ]
            )
        )
        if not isinstance(head, dict):
            raise TypeError("S3 head-object response must be a JSON object")
    except (TypeError, ValueError, json.JSONDecodeError) as exc:
        raise CanaryError("S3 head-object returned invalid JSON") from exc
    if head.get("ContentLength") != expected_bytes:
        raise CanaryError(f"S3 object byte count mismatch: {uri}")
    if head.get("ServerSideEncryption") not in {"AES256", "aws:kms"}:
        raise CanaryError(f"S3 object is not server-side encrypted: {uri}")
    metadata = head.get("Metadata")
    if not isinstance(metadata, dict) or metadata.get("sha256") != expected_sha256:
        raise CanaryError(f"S3 object checksum metadata mismatch: {uri}")
    return {
        "uri": uri,
        "bytes": expected_bytes,
        "sha256": expected_sha256,
        "server_side_encryption": head["ServerSideEncryption"],
    }


def upload_committed_batch(
    batch: str | os.PathLike[str],
    *,
    command_runner: CommandRunner = _command_output,
) -> dict[str, Any]:
    verification = rehearsal.verify_batch(batch)
    if verification.get("source_kind") != rehearsal.PROD_CANARY_SOURCE_KIND:
        raise CanaryError("only production archive batches may be uploaded")
    batch_dir = pathlib.Path(batch).expanduser().resolve()
    manifest_path = batch_dir / "manifest.json"
    manifest_bytes = manifest_path.read_bytes()
    manifest = json.loads(manifest_bytes)
    prefix = manifest["staging_s3_prefix"]
    uploaded: list[dict[str, Any]] = []

    def upload(path: pathlib.Path, sha256: str) -> dict[str, Any]:
        uri = f"{prefix}/{path.name}"
        command_runner(
            [
                "aws",
                "s3",
                "cp",
                "--region",
                PROD_REGION,
                "--only-show-errors",
                "--sse",
                "AES256",
                "--metadata",
                f"sha256={sha256}",
                str(path),
                uri,
            ]
        )
        return _head_s3_object(
            uri,
            expected_bytes=path.stat().st_size,
            expected_sha256=sha256,
            command_runner=command_runner,
        )

    for entry in manifest["artifacts"]:
        artifact = rehearsal._safe_artifact(batch_dir, entry["path"])
        uploaded.append(upload(artifact, entry["artifact_sha256"]))
    manifest_sha256 = rehearsal._sha256(manifest_bytes)
    uploaded.append(upload(manifest_path, manifest_sha256))
    return {
        "mode": "prod_archive_export_canary_upload",
        "batch_id": manifest["batch_id"],
        "s3_prefix": prefix,
        "manifest_sha256": manifest_sha256,
        "objects": uploaded,
        "manifest_uploaded_last": uploaded[-1]["uri"].endswith("/manifest.json"),
        "source_mutated": False,
        "deletion_authorized": False,
    }


def host_export(
    *,
    table: str,
    as_of: str,
    instance_id: str,
    staging_s3_base_uri: str,
    hold_started_at: str,
    confirmation: str,
    timeout_seconds: int,
    max_rows: int,
    max_logical_bytes: int,
) -> dict[str, Any]:
    if confirmation != PROD_CONFIRMATION:
        raise CanaryError("production export refused: confirmation token is invalid")
    hold_verification = cleanup_hold_remote.verify_hold(hold_started_at)
    with tempfile.TemporaryDirectory(prefix="tokenkey-prod-archive-canary-") as temp:
        sealed = seal_prod_canary_batch(
            pathlib.Path(temp) / "batches",
            table=table,
            as_of=as_of,
            instance_id=instance_id,
            staging_s3_base_uri=staging_s3_base_uri,
            timeout_seconds=timeout_seconds,
            max_rows=max_rows,
            max_logical_bytes=max_logical_bytes,
        )
        uploaded = upload_committed_batch(sealed["batch_dir"])
        return {
            **uploaded,
            "canary": sealed["canary"],
            "metrics": sealed["metrics"],
            "cleanup_hold": {
                "hold_started_at": hold_started_at,
                "verified_at": hold_verification["server_clock"],
                "hold_active": hold_verification["hold_active"],
                "no_cleanup_after_hold": hold_verification["no_cleanup_after_hold"],
                "runtime_disabled_proven": hold_verification[
                    "runtime_disabled_proven"
                ],
            },
        }


def _aws_json(args: list[str]) -> Any:
    try:
        return json.loads(_command_output(["aws", *args, "--output", "json"]))
    except json.JSONDecodeError as exc:
        raise CanaryError("AWS CLI returned invalid JSON") from exc


def _stack_output(stack: str, output_key: str) -> str:
    payload = _aws_json(
        [
            "cloudformation",
            "describe-stacks",
            "--region",
            PROD_REGION,
            "--stack-name",
            stack,
        ]
    )
    try:
        outputs = payload["Stacks"][0]["Outputs"]
        value = next(
            item["OutputValue"] for item in outputs if item["OutputKey"] == output_key
        )
    except (KeyError, IndexError, StopIteration, TypeError) as exc:
        raise CanaryError(f"stack {stack} has no {output_key} output") from exc
    if not isinstance(value, str) or not value:
        raise CanaryError(f"stack {stack} returned an invalid {output_key}")
    return value


def _stack_parameter(stack: str, parameter_key: str) -> str:
    payload = _aws_json(
        [
            "cloudformation",
            "describe-stacks",
            "--region",
            PROD_REGION,
            "--stack-name",
            stack,
        ]
    )
    try:
        parameters = payload["Stacks"][0]["Parameters"]
        value = next(
            item["ParameterValue"]
            for item in parameters
            if item["ParameterKey"] == parameter_key
        )
    except (KeyError, IndexError, StopIteration, TypeError) as exc:
        raise CanaryError(f"stack {stack} has no {parameter_key} parameter") from exc
    if not isinstance(value, str) or not value:
        raise CanaryError(f"stack {stack} returned an invalid {parameter_key}")
    return value


def _prod_instance() -> str:
    instance_id = _stack_output(PROD_STACK, "InstanceId")
    if (
        not instance_id.startswith("i-")
        or len(instance_id) != 19
        or any(character not in "0123456789abcdef" for character in instance_id[2:])
    ):
        raise CanaryError("prod stack returned an invalid EC2 instance id")
    payload = _aws_json(
        [
            "ec2",
            "describe-instances",
            "--region",
            PROD_REGION,
            "--instance-ids",
            instance_id,
        ]
    )
    try:
        reservations = payload["Reservations"]
        if len(reservations) != 1 or len(reservations[0]["Instances"]) != 1:
            raise ValueError("describe-instances must return exactly one instance")
        instance = reservations[0]["Instances"][0]
        resolved_instance_id = instance["InstanceId"]
        tags = {item["Key"]: item["Value"] for item in instance["Tags"]}
        state = instance["State"]["Name"]
    except (KeyError, IndexError, TypeError, ValueError) as exc:
        raise CanaryError("prod instance metadata is incomplete") from exc
    if resolved_instance_id != instance_id:
        raise CanaryError("prod instance metadata does not match the stack output")
    if tags.get("Project") != "tokenkey" or tags.get("Environment") != "prod":
        raise CanaryError("prod instance tags do not match Project=tokenkey/Environment=prod")
    if state != "running":
        raise CanaryError(f"prod instance is not running: {state}")
    return instance_id


def _remote_bundle_sources() -> dict[str, bytes]:
    return {
        "data_layer_archive_prod_canary.py": pathlib.Path(__file__).read_bytes(),
        "data_layer_archive_rehearsal.py": pathlib.Path(rehearsal.__file__).read_bytes(),
        "data_layer_archive_cleanup_hold.py": pathlib.Path(cleanup_hold.__file__).read_bytes(),
        "data_layer_archive_cleanup_hold_remote.py": pathlib.Path(
            cleanup_hold_remote.__file__
        ).read_bytes(),
    }


def _remote_bundle_archive() -> bytes:
    tar_buffer = io.BytesIO()
    with tarfile.open(fileobj=tar_buffer, mode="w") as archive:
        for name, source in sorted(_remote_bundle_sources().items()):
            info = tarfile.TarInfo(name)
            info.size = len(source)
            info.mode = 0o600
            info.mtime = 0
            archive.addfile(info, io.BytesIO(source))
    return gzip.compress(tar_buffer.getvalue(), compresslevel=9, mtime=0)


def stage_remote_bundle(
    staging_s3_base_uri: str,
    *,
    command_runner: CommandRunner = _command_output,
) -> dict[str, Any]:
    staging_base = _validated_staging_base(staging_s3_base_uri)
    archive = _remote_bundle_archive()
    checksum = rehearsal._sha256(archive)
    uri = f"{staging_base}/control/{checksum}.tar.gz"
    with tempfile.TemporaryDirectory(prefix="tokenkey-prod-canary-control-") as temp:
        path = pathlib.Path(temp) / "control.tar.gz"
        path.write_bytes(archive)
        command_runner(
            [
                "aws",
                "s3",
                "cp",
                "--region",
                PROD_REGION,
                "--only-show-errors",
                "--sse",
                "AES256",
                "--metadata",
                f"sha256={checksum}",
                str(path),
                uri,
            ]
        )
        verified = _head_s3_object(
            uri,
            expected_bytes=len(archive),
            expected_sha256=checksum,
            command_runner=command_runner,
        )
    return {
        "mode": "prod_archive_export_canary_control_bundle",
        **verified,
        "deletion_authorized": False,
    }


def _remote_host_command(
    *,
    table: str,
    as_of: str,
    instance_id: str,
    staging_s3_base_uri: str,
    bundle_s3_uri: str,
    bundle_sha256: str,
    hold_started_at: str,
    timeout_seconds: int,
    max_rows: int,
    max_logical_bytes: int,
) -> str:
    bundle_bucket, bundle_key = _s3_location(bundle_s3_uri)
    staging_bucket, staging_key = _s3_location(staging_s3_base_uri)
    if bundle_bucket != staging_bucket or not bundle_key.startswith(
        f"{staging_key}/control/"
    ):
        raise CanaryError("remote control bundle is outside the approved staging prefix")
    if (
        len(bundle_sha256) != 64
        or any(character not in "0123456789abcdef" for character in bundle_sha256)
        or not bundle_key.endswith(f"/{bundle_sha256}.tar.gz")
    ):
        raise CanaryError("remote control bundle checksum binding is invalid")
    args = [
        "python3",
        "$WORK/data_layer_archive_prod_canary.py",
        "host-export",
        "--table",
        table,
        "--as-of",
        as_of,
        "--instance-id",
        instance_id,
        "--staging-s3-base-uri",
        staging_s3_base_uri,
        "--hold-started-at",
        hold_started_at,
        "--timeout-seconds",
        str(timeout_seconds),
        "--max-rows",
        str(max_rows),
        "--max-logical-bytes",
        str(max_logical_bytes),
        "--confirm",
        PROD_CONFIRMATION,
    ]
    rendered_args = " ".join(
        item if item.startswith("$WORK/") else shlex.quote(item) for item in args
    )
    names = sorted(_remote_bundle_sources())
    cleanup_paths = " ".join(f'"$WORK/{name}"' for name in names)
    lines = [
        "set -euo pipefail",
        'WORK="$(mktemp -d /tmp/tokenkey-prod-archive-canary.XXXXXX)"',
        'ARCHIVE="$WORK/control.tar.gz"',
        "cleanup() {",
        f'  rm -f "$ARCHIVE" {cleanup_paths}',
        '  rmdir "$WORK" 2>/dev/null || true',  # preflight-allow: swallow
        "}",
        "trap cleanup EXIT",
        f"aws s3 cp --region {PROD_REGION} --only-show-errors "
        f"{shlex.quote(bundle_s3_uri)} \"$ARCHIVE\"",
        'ACTUAL_SHA256="$(sha256sum "$ARCHIVE" | awk \'{print $1}\')"',
        f'test "$ACTUAL_SHA256" = {shlex.quote(bundle_sha256)}',
        'tar -xzf "$ARCHIVE" -C "$WORK"',
        'rm -f "$ARCHIVE"',
    ]
    lines.append(f'PYTHONPATH="$WORK" {rendered_args}')
    return "\n".join(lines)


def _ssm_parameters(remote_script: str, timeout_seconds: int) -> str:
    script_b64 = base64.b64encode(remote_script.encode("utf-8")).decode("ascii")
    remote = (
        'SCRIPT="$(mktemp /tmp/tokenkey-prod-canary-run.XXXXXX)"\n'
        "cleanup() { rm -f \"$SCRIPT\"; }\n"
        "trap cleanup EXIT\n"
        f"printf %s {shlex.quote(script_b64)} | base64 -d > \"$SCRIPT\"\n"
        'bash "$SCRIPT"'
    )
    params = json.dumps(
        {
            "commands": ["set -u -o pipefail", remote],
            "executionTimeout": [str(timeout_seconds)],
        }
    )
    if len(params.encode("utf-8")) > SSM_PARAMETERS_MAX_BYTES:
        raise CanaryError("production canary SSM parameters exceed the safe payload bound")
    return params


def _run_ssm(instance_id: str, remote_script: str, *, timeout_seconds: int) -> dict[str, Any]:
    params = _ssm_parameters(remote_script, timeout_seconds)
    send = _aws_json(
        [
            "ssm",
            "send-command",
            "--region",
            PROD_REGION,
            "--instance-ids",
            instance_id,
            "--document-name",
            "AWS-RunShellScript",
            "--comment",
            "tokenkey prod archive export-only canary",
            "--timeout-seconds",
            str(timeout_seconds),
            "--parameters",
            params,
        ]
    )
    try:
        command_id = send["Command"]["CommandId"]
    except (KeyError, TypeError) as exc:
        raise CanaryError("SSM send-command returned no command id") from exc
    deadline = time.monotonic() + timeout_seconds
    invocation: dict[str, Any] | None = None
    while time.monotonic() < deadline:
        try:
            candidate = _aws_json(
                [
                    "ssm",
                    "get-command-invocation",
                    "--region",
                    PROD_REGION,
                    "--command-id",
                    command_id,
                    "--instance-id",
                    instance_id,
                ]
            )
        except CanaryError as exc:
            if "InvocationDoesNotExist" not in str(exc):
                raise
        else:
            if not isinstance(candidate, dict):
                raise CanaryError("SSM invocation response is invalid")
            invocation = candidate
            if candidate.get("Status") in TERMINAL_SSM_STATUSES:
                break
        time.sleep(5)
    if invocation is None or invocation.get("Status") not in TERMINAL_SSM_STATUSES:
        try:
            _command_output(
                [
                    "aws",
                    "ssm",
                    "cancel-command",
                    "--region",
                    PROD_REGION,
                    "--command-id",
                    command_id,
                    "--instance-ids",
                    instance_id,
                ]
            )
        except CanaryError as exc:
            raise CanaryError(
                "production canary SSM exceeded its controller deadline and "
                f"cancellation failed: {exc}"
            ) from exc
        raise CanaryError(
            "production canary SSM exceeded its controller deadline and was cancelled"
        )
    if invocation.get("Status") != "Success":
        status = invocation.get("Status") if invocation else "TimedOut"
        detail = (invocation or {}).get("StandardErrorContent", "")
        raise CanaryError(f"production canary SSM failed: {status} {detail[:400]}")
    stdout = invocation.get("StandardOutputContent")
    if not isinstance(stdout, str):
        raise CanaryError("production canary SSM returned no stdout")
    lines = [line for line in stdout.splitlines() if line.strip()]
    try:
        receipt = json.loads(lines[-1])
    except (IndexError, TypeError, json.JSONDecodeError) as exc:
        raise CanaryError("production canary SSM receipt is invalid") from exc
    if not isinstance(receipt, dict) or receipt.get("deletion_authorized") is not False:
        raise CanaryError("production canary SSM receipt failed safety validation")
    return receipt


def _validated_remote_receipt(
    receipt: dict[str, Any], *, staging_base: str, expected_hold_started_at: str
) -> dict[str, Any]:
    batch_id = receipt.get("batch_id")
    if not isinstance(batch_id, str) or not batch_id.startswith("prod-canary-"):
        raise CanaryError("production canary SSM receipt has an invalid batch id")
    expected_prefix = f"{staging_base}/{batch_id}"
    manifest_sha256 = receipt.get("manifest_sha256")
    objects = receipt.get("objects")
    hold = receipt.get("cleanup_hold")
    if (
        receipt.get("mode") != "prod_archive_export_canary_upload"
        or receipt.get("s3_prefix") != expected_prefix
        or receipt.get("manifest_uploaded_last") is not True
        or receipt.get("source_mutated") is not False
        or receipt.get("deletion_authorized") is not False
        or not isinstance(manifest_sha256, str)
        or len(manifest_sha256) != 64
        or any(character not in "0123456789abcdef" for character in manifest_sha256)
        or not isinstance(objects, list)
        or not objects
        or not isinstance(objects[-1], dict)
        or objects[-1].get("uri") != f"{expected_prefix}/manifest.json"
        or objects[-1].get("sha256") != manifest_sha256
        or objects[-1].get("server_side_encryption") not in {"AES256", "aws:kms"}
        or not isinstance(hold, dict)
        or hold.get("hold_started_at") != expected_hold_started_at
        or hold.get("hold_active") is not True
        or hold.get("no_cleanup_after_hold") is not True
        or hold.get("runtime_disabled_proven") is not True
        or not isinstance(hold.get("verified_at"), str)
    ):
        raise CanaryError("production canary SSM receipt failed commit validation")
    return receipt


def _download_committed_batch(
    s3_prefix: str,
    batch_id: str,
    evidence_root: str | os.PathLike[str],
    *,
    command_runner: CommandRunner = _command_output,
    expected_manifest_sha256: str,
) -> pathlib.Path:
    _, key = _s3_location(s3_prefix)
    if not key.endswith(f"/{batch_id}") or not (
        batch_id.startswith("prod-canary-") or batch_id.startswith("prod-export-")
    ):
        raise CanaryError("S3 prefix and production archive batch id do not match")
    root = rehearsal._local_path(evidence_root, must_exist=False)
    root.mkdir(parents=True, exist_ok=True)
    batch_dir = root / batch_id

    def verify_downloaded(path: pathlib.Path) -> None:
        verification = rehearsal.verify_batch(path)
        if verification["manifest_sha256"] != expected_manifest_sha256:
            raise CanaryError("downloaded canary manifest checksum mismatch")
        manifest = json.loads((path / "manifest.json").read_text(encoding="utf-8"))
        if (
            not isinstance(manifest, dict)
            or manifest.get("staging_s3_prefix") != s3_prefix
        ):
            raise CanaryError("downloaded canary manifest S3 prefix mismatch")

    if batch_dir.exists():
        verify_downloaded(batch_dir)
        return batch_dir
    temporary_root = pathlib.Path(
        tempfile.mkdtemp(prefix=f".{batch_id}-", dir=root)
    )
    temporary = temporary_root / batch_id
    temporary.mkdir()
    try:
        manifest_path = temporary / "manifest.json"
        command_runner(
            [
                "aws",
                "s3",
                "cp",
                "--region",
                PROD_REGION,
                "--only-show-errors",
                f"{s3_prefix}/manifest.json",
                str(manifest_path),
            ]
        )
        try:
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise CanaryError("downloaded canary manifest is invalid") from exc
        if not isinstance(manifest, dict):
            raise CanaryError("downloaded canary manifest must be a JSON object")
        if manifest.get("batch_id") != batch_id:
            raise CanaryError("downloaded canary manifest batch id mismatch")
        if manifest.get("staging_s3_prefix") != s3_prefix:
            raise CanaryError("downloaded canary manifest S3 prefix mismatch")
        artifacts = manifest.get("artifacts")
        if not isinstance(artifacts, list) or not artifacts:
            raise CanaryError("downloaded canary manifest has no artifacts")
        for entry in artifacts:
            if not isinstance(entry, dict):
                raise CanaryError("downloaded canary artifact entry is invalid")
            relative = entry.get("path")
            if not isinstance(relative, str) or pathlib.PurePosixPath(relative).name != relative:
                raise CanaryError("downloaded canary artifact path is invalid")
            command_runner(
                [
                    "aws",
                    "s3",
                    "cp",
                    "--region",
                    PROD_REGION,
                    "--only-show-errors",
                    f"{s3_prefix}/{relative}",
                    str(temporary / relative),
                ]
            )
        verify_downloaded(temporary)
        temporary.replace(batch_dir)
        temporary_root.rmdir()
    except Exception:
        if temporary.exists():
            for child in temporary.glob("*"):
                child.unlink(missing_ok=True)
            temporary.rmdir()
        temporary_root.rmdir()
        raise
    return batch_dir


def restore_committed_batch(
    *,
    s3_prefix: str,
    batch_id: str,
    evidence_root: str | os.PathLike[str],
    target_dsn: str,
    seed: int,
    timeout_seconds: int,
    expected_manifest_sha256: str,
) -> dict[str, Any]:
    rehearsal._postgres_dsn_info(target_dsn, target=True)
    batch_dir = _download_committed_batch(
        s3_prefix,
        batch_id,
        evidence_root,
        expected_manifest_sha256=expected_manifest_sha256,
    )
    verification = rehearsal.verify_batch(batch_dir)
    restored = rehearsal.restore_postgres_random(
        batch_dir,
        target_dsn,
        seed=seed,
        timeout_seconds=timeout_seconds,
    )
    return {
        "mode": "prod_archive_export_canary_restore",
        "batch_id": batch_id,
        "batch_dir": str(batch_dir),
        "verify": verification,
        "restore": restored,
        "source_mutated": False,
        "deletion_authorized": False,
    }


def run_canary(args: argparse.Namespace) -> dict[str, Any]:
    if args.confirm != PROD_CONFIRMATION:
        raise CanaryError("production export refused: confirmation token is invalid")
    request = _validated_request(
        table=args.table,
        as_of=args.as_of,
        timeout_seconds=args.timeout_seconds,
        max_rows=args.max_rows,
        max_logical_bytes=args.max_logical_bytes,
    )
    rehearsal._postgres_dsn_info(args.restore_target_dsn, target=True)
    instance_id = _prod_instance()
    hold_receipt = cleanup_hold.verify_receipt_for_instance(
        args.cleanup_hold_receipt, instance_id
    )
    hold_verification = cleanup_hold.verify(args.cleanup_hold_receipt)
    if hold_verification.get("instance_id") != instance_id:
        raise CanaryError("cleanup hold verification reached a different instance")
    bucket = _stack_output(BACKUP_STACK, BACKUP_BUCKET_OUTPUT)
    retention_days = _stack_parameter(BACKUP_STACK, "RetentionDays")
    if retention_days != str(BACKUP_RETENTION_DAYS):
        raise CanaryError(
            "production canary staging requires the approved 7-day retention"
        )
    staging_base = _validated_staging_base(f"s3://{bucket}/{S3_KEY_BASE}")
    remote_bundle = stage_remote_bundle(staging_base)
    remote_script = _remote_host_command(
        table=request["table"],
        as_of=request["as_of"],
        instance_id=instance_id,
        staging_s3_base_uri=staging_base,
        bundle_s3_uri=remote_bundle["uri"],
        bundle_sha256=remote_bundle["sha256"],
        hold_started_at=hold_receipt["hold_started_at"],
        timeout_seconds=request["timeout_seconds"],
        max_rows=request["max_rows"],
        max_logical_bytes=request["max_logical_bytes"],
    )
    upload = _run_ssm(
        instance_id,
        remote_script,
        timeout_seconds=args.ssm_timeout_seconds,
    )
    upload = _validated_remote_receipt(
        upload,
        staging_base=staging_base,
        expected_hold_started_at=hold_receipt["hold_started_at"],
    )
    restored = restore_committed_batch(
        s3_prefix=upload["s3_prefix"],
        batch_id=upload["batch_id"],
        evidence_root=args.evidence_root,
        target_dsn=args.restore_target_dsn,
        seed=args.seed,
        timeout_seconds=args.timeout_seconds,
        expected_manifest_sha256=upload["manifest_sha256"],
    )
    return {
        **restored,
        "schema_version": rehearsal.SCHEMA_VERSION,
        "mode": "prod_archive_export_canary_complete",
        "environment": "prod",
        "instance_id": instance_id,
        "remote_bundle": remote_bundle,
        "upload": upload,
        "cleanup_hold": {
            "hold_started_at": hold_receipt["hold_started_at"],
            "controller_verified_at": hold_verification["server_clock"],
            "host_verified_at": upload["cleanup_hold"]["verified_at"],
            "no_cleanup_after_hold": hold_verification["no_cleanup_after_hold"],
        },
        "production_export_executed": True,
        "source_mutated": False,
        "deletion_authorized": False,
    }


def _add_limits(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--table", choices=rehearsal.PROD_CANARY_TABLES, required=True)
    parser.add_argument("--as-of", required=True, help="UTC waterline near database clock")
    parser.add_argument(
        "--timeout-seconds", type=int, default=DEFAULT_TIMEOUT_SECONDS
    )
    parser.add_argument("--max-rows", type=int, default=DEFAULT_MAX_ROWS)
    parser.add_argument(
        "--max-logical-bytes", type=int, default=DEFAULT_MAX_LOGICAL_BYTES
    )


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    plan = commands.add_parser("plan", help="validate and print an offline no-execute plan")
    _add_limits(plan)

    run = commands.add_parser("run", help="execute the approved export-only canary")
    _add_limits(run)
    run.add_argument("--evidence-root", required=True)
    run.add_argument("--restore-target-dsn", required=True)
    run.add_argument("--seed", type=int, required=True)
    run.add_argument("--cleanup-hold-receipt", required=True)
    run.add_argument("--ssm-timeout-seconds", type=int, default=300)
    run.add_argument("--confirm", required=True)

    host = commands.add_parser("host-export", help=argparse.SUPPRESS)
    _add_limits(host)
    host.add_argument("--instance-id", required=True)
    host.add_argument("--staging-s3-base-uri", required=True)
    host.add_argument("--hold-started-at", required=True)
    host.add_argument("--confirm", required=True)

    return parser


def main(argv: Iterable[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "plan":
            payload = build_plan(
                table=args.table,
                as_of=args.as_of,
                timeout_seconds=args.timeout_seconds,
                max_rows=args.max_rows,
                max_logical_bytes=args.max_logical_bytes,
            )
        elif args.command == "run":
            if not 30 <= args.ssm_timeout_seconds <= 900:
                raise CanaryError("ssm_timeout_seconds must be between 30 and 900")
            payload = run_canary(args)
        elif args.command == "host-export":
            payload = host_export(
                table=args.table,
                as_of=args.as_of,
                instance_id=args.instance_id,
                staging_s3_base_uri=args.staging_s3_base_uri,
                hold_started_at=args.hold_started_at,
                confirmation=args.confirm,
                timeout_seconds=args.timeout_seconds,
                max_rows=args.max_rows,
                max_logical_bytes=args.max_logical_bytes,
            )
        else:  # pragma: no cover - argparse owns the command space.
            parser.error(f"unknown command {args.command}")
        print(rehearsal._canonical_json(payload))
    except (
        CanaryError,
        OSError,
        rehearsal.RehearsalError,
        cleanup_hold.HoldControlError,
        cleanup_hold_remote.HoldError,
    ) as exc:
        print(f"production archive canary refused: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
