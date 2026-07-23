#!/usr/bin/env python3
"""Production legacy cold batch export (export-only, no deletion).

Exports strictly cold ops rows from the legacy partition window in deterministic
(created_at, id) pages. Each batch is sealed, uploaded to encrypted staging, and
tracked in a local ledger for continuation. Requires an active cleanup hold.
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import shlex
import sys
import tempfile
from collections.abc import Iterable
from typing import Any

import data_layer_archive_cleanup_hold as cleanup_hold
import data_layer_archive_cleanup_hold_remote as cleanup_hold_remote
import data_layer_archive_prod_canary as canary
import data_layer_archive_rehearsal as rehearsal


PROD_EXPORT_CONFIRMATION = "tokenkey-prod-archive-export-batch-v1"
S3_KEY_BASE = "prod/pgdump/archive-export"
LEDGER_SCHEMA_VERSION = 1
LEDGER_MODE = "prod_archive_export_ledger"
DEFAULT_MAX_ROWS = 50_000
HARD_MAX_ROWS = 200_000
DEFAULT_MAX_LOGICAL_BYTES = 256 * 1024 * 1024
HARD_MAX_LOGICAL_BYTES = 512 * 1024 * 1024
DEFAULT_TIMEOUT_SECONDS = 120
HARD_TIMEOUT_SECONDS = 300


class ExportError(canary.CanaryError):
    """Fail-closed production batch export error."""


def _canonical_json(value: Any) -> str:
    return rehearsal._canonical_json(value)


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


def _validated_staging_base(uri: str) -> str:
    bucket, key = canary._s3_location(uri)
    if key != S3_KEY_BASE:
        raise ExportError(f"S3 staging key must be exactly {S3_KEY_BASE}")
    return f"s3://{bucket}/{key}"


def _validated_legacy_upper(value: str) -> str:
    normalized = canary._canonical_timestamp(value)
    if normalized != rehearsal.PROD_LEGACY_UPPER_EXCLUSIVE:
        raise ExportError(
            "legacy_upper_exclusive must match the approved prod legacy attach bound"
        )
    return normalized


def _validated_request(
    *,
    table: str,
    timeout_seconds: int,
    max_rows: int,
    max_logical_bytes: int,
) -> dict[str, Any]:
    if table not in rehearsal.PROD_CANARY_TABLES:
        raise ExportError(f"table must be one of {rehearsal.PROD_CANARY_TABLES}")
    if (
        isinstance(timeout_seconds, bool)
        or not isinstance(timeout_seconds, int)
        or not 1 <= timeout_seconds <= HARD_TIMEOUT_SECONDS
    ):
        raise ExportError(
            f"timeout_seconds must be between 1 and {HARD_TIMEOUT_SECONDS}"
        )
    if (
        isinstance(max_rows, bool)
        or not isinstance(max_rows, int)
        or not 1 <= max_rows <= HARD_MAX_ROWS
    ):
        raise ExportError(f"max_rows must be between 1 and {HARD_MAX_ROWS}")
    if (
        isinstance(max_logical_bytes, bool)
        or not isinstance(max_logical_bytes, int)
        or not 1 <= max_logical_bytes <= HARD_MAX_LOGICAL_BYTES
    ):
        raise ExportError(
            "max_logical_bytes must be between 1 and "
            f"{HARD_MAX_LOGICAL_BYTES}"
        )
    return {
        "table": table,
        "timeout_seconds": timeout_seconds,
        "max_rows": max_rows,
        "max_logical_bytes": max_logical_bytes,
    }


def build_plan(
    *,
    table: str,
    legacy_upper_exclusive: str,
    timeout_seconds: int,
    max_rows: int,
    max_logical_bytes: int,
) -> dict[str, Any]:
    request = _validated_request(
        table=table,
        timeout_seconds=timeout_seconds,
        max_rows=max_rows,
        max_logical_bytes=max_logical_bytes,
    )
    return {
        "schema_version": rehearsal.SCHEMA_VERSION,
        "mode": "prod_archive_export_batch_plan",
        "environment": "prod",
        "region": canary.PROD_REGION,
        "stack": canary.PROD_STACK,
        "backup_stack": canary.BACKUP_STACK,
        "export_scope": rehearsal.PROD_EXPORT_SCOPE_LEGACY_COLD,
        "legacy_upper_exclusive": _validated_legacy_upper(legacy_upper_exclusive),
        "staging_key_base": S3_KEY_BASE,
        "staging_retention_days": canary.BACKUP_RETENTION_DAYS,
        "source_mutated": False,
        "deletion_authorized": False,
        "execution_authorized": False,
        "cleanup_hold_required": True,
        "required_confirmation": PROD_EXPORT_CONFIRMATION,
        "continuation": {
            "selection_order": ["created_at", "id"],
            "cursor_starts_at": None,
            "ledger_required": True,
        },
        **request,
    }


def init_ledger(
    path: str | os.PathLike[str],
    *,
    table: str,
    legacy_upper_exclusive: str,
) -> dict[str, Any]:
    if table not in rehearsal.PROD_CANARY_TABLES:
        raise ExportError(f"table must be one of {rehearsal.PROD_CANARY_TABLES}")
    ledger_path = pathlib.Path(path).expanduser().resolve()
    if ledger_path.exists():
        raise ExportError("export ledger already exists; refuse to overwrite")
    payload = {
        "schema_version": LEDGER_SCHEMA_VERSION,
        "mode": LEDGER_MODE,
        "environment": "prod",
        "table": table,
        "export_scope": rehearsal.PROD_EXPORT_SCOPE_LEGACY_COLD,
        "legacy_upper_exclusive": _validated_legacy_upper(legacy_upper_exclusive),
        "cursor_after": None,
        "completed_batches": [],
        "more_cold_rows_remaining": True,
        "source_mutated": False,
        "deletion_authorized": False,
    }
    _atomic_json(ledger_path, payload)
    return payload


def load_ledger(path: str | os.PathLike[str]) -> dict[str, Any]:
    try:
        payload = json.loads(
            pathlib.Path(path).expanduser().resolve().read_text(encoding="utf-8")
        )
    except (OSError, json.JSONDecodeError) as exc:
        raise ExportError("export ledger cannot be read") from exc
    if (
        not isinstance(payload, dict)
        or payload.get("schema_version") != LEDGER_SCHEMA_VERSION
        or payload.get("mode") != LEDGER_MODE
        or payload.get("environment") != "prod"
        or payload.get("export_scope") != rehearsal.PROD_EXPORT_SCOPE_LEGACY_COLD
        or payload.get("table") not in rehearsal.PROD_CANARY_TABLES
        or payload.get("deletion_authorized") is not False
        or not isinstance(payload.get("completed_batches"), list)
        or not isinstance(payload.get("more_cold_rows_remaining"), bool)
    ):
        raise ExportError("export ledger failed validation")
    _validated_legacy_upper(str(payload["legacy_upper_exclusive"]))
    cursor = payload.get("cursor_after")
    if cursor is not None and (
        not isinstance(cursor, dict) or set(cursor) != {"created_at", "id"}
    ):
        raise ExportError("export ledger cursor is invalid")
    return payload


def seal_prod_export_batch(
    archive_root: str | os.PathLike[str],
    *,
    table: str,
    instance_id: str,
    staging_s3_base_uri: str,
    cursor_before: dict[str, Any] | None,
    legacy_upper_exclusive: str,
    query_runner: canary.QueryRunner = canary._run_stage0_psql,
    timeout_seconds: int = DEFAULT_TIMEOUT_SECONDS,
    max_rows: int = DEFAULT_MAX_ROWS,
    max_logical_bytes: int = DEFAULT_MAX_LOGICAL_BYTES,
) -> dict[str, Any]:
    request = _validated_request(
        table=table,
        timeout_seconds=timeout_seconds,
        max_rows=max_rows,
        max_logical_bytes=max_logical_bytes,
    )
    legacy_upper = _validated_legacy_upper(legacy_upper_exclusive)
    if (
        not isinstance(instance_id, str)
        or not instance_id.startswith("i-")
        or len(instance_id) != 19
    ):
        raise ExportError("production instance id is invalid")
    staging_base = _validated_staging_base(staging_s3_base_uri)
    candidates, metrics = canary._collect_prod_candidates(
        query_runner,
        request=request,
        cursor_after=cursor_before,
        upper_exclusive=legacy_upper,
    )
    artifacts_preview = [rehearsal._artifact_entry("ops", candidates["ops"])[0]]
    source_identity = {
        "container": rehearsal.PROD_CANARY_CONTAINER,
        "database": rehearsal.PROD_CANARY_DATABASE,
        "instance_id": instance_id,
        "stack": canary.PROD_STACK,
        "table": table,
    }
    source_identity_sha256 = rehearsal._sha256(
        _canonical_json(source_identity).encode("utf-8")
    )
    sealed_at = metrics["server_clock"]
    batch_id = rehearsal._batch_id(
        environment="prod",
        sealed_at=sealed_at,
        source_path_sha256=source_identity_sha256,
        source_file_identity=source_identity,
        retention_days=dict(rehearsal.DEFAULT_RETENTION_DAYS),
        artifacts=artifacts_preview,
        prefix="prod-export",
    )
    manifest = {
        "schema_version": rehearsal.SCHEMA_VERSION,
        "mode": rehearsal.PROD_EXPORT_MODE,
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
        "export": {
            "table": table,
            "export_scope": rehearsal.PROD_EXPORT_SCOPE_LEGACY_COLD,
            "legacy_upper_exclusive": legacy_upper,
            "cutoff_exclusive": metrics["cutoff_exclusive"],
            "server_clock": metrics["server_clock"],
            "lock_timeout_ms": canary.LOCK_TIMEOUT_MS,
            "statement_timeout_seconds": timeout_seconds,
            "max_rows": max_rows,
            "max_logical_bytes": max_logical_bytes,
            "query_elapsed_ms": metrics["query_elapsed_ms"],
            "selection_order": ["created_at", "id"],
            "cursor_before": cursor_before,
            "cursor_after": metrics["cursor_after"],
            "first_key": metrics["sample_first_key"],
            "last_key": metrics["sample_last_key"],
            "more_cold_rows_remaining": metrics["more_cold_rows_after_sample"],
        },
        "source_mutated": False,
        "deletion_authorized": False,
    }
    return rehearsal._write_sealed_batch(
        candidates=candidates,
        archive_root=archive_root,
        manifest=manifest,
    )


def _remote_bundle_sources() -> dict[str, bytes]:
    module_path = pathlib.Path(__file__).resolve()
    return {
        **canary._remote_bundle_sources(),
        "data_layer_archive_prod_export.py": module_path.read_bytes(),
    }


def stage_remote_bundle(
    staging_s3_base_uri: str,
    *,
    command_runner: canary.CommandRunner = canary._command_output,
) -> dict[str, Any]:
    staging_base = _validated_staging_base(staging_s3_base_uri)
    import gzip
    import io
    import tarfile

    tar_buffer = io.BytesIO()
    with tarfile.open(fileobj=tar_buffer, mode="w") as archive:
        for name, source in sorted(_remote_bundle_sources().items()):
            info = tarfile.TarInfo(name)
            info.size = len(source)
            info.mode = 0o600
            info.mtime = 0
            archive.addfile(info, io.BytesIO(source))
    payload = gzip.compress(tar_buffer.getvalue(), compresslevel=9, mtime=0)
    checksum = rehearsal._sha256(payload)
    uri = f"{staging_base}/control/{checksum}.tar.gz"
    with tempfile.TemporaryDirectory(prefix="tokenkey-prod-export-control-") as temp:
        path = pathlib.Path(temp) / "control.tar.gz"
        path.write_bytes(payload)
        command_runner(
            [
                "aws",
                "s3",
                "cp",
                "--region",
                canary.PROD_REGION,
                "--only-show-errors",
                "--sse",
                "AES256",
                "--metadata",
                f"sha256={checksum}",
                str(path),
                uri,
            ]
        )
        verified = canary._head_s3_object(
            uri,
            expected_bytes=len(payload),
            expected_sha256=checksum,
            command_runner=command_runner,
        )
    return {
        "mode": "prod_archive_export_batch_control_bundle",
        **verified,
        "deletion_authorized": False,
    }


def _remote_host_command(
    *,
    table: str,
    instance_id: str,
    staging_s3_base_uri: str,
    bundle_s3_uri: str,
    bundle_sha256: str,
    hold_started_at: str,
    cursor_before_json: str,
    legacy_upper_exclusive: str,
    timeout_seconds: int,
    max_rows: int,
    max_logical_bytes: int,
) -> str:
    bundle_bucket, bundle_key = canary._s3_location(bundle_s3_uri)
    staging_bucket, staging_key = canary._s3_location(staging_s3_base_uri)
    if bundle_bucket != staging_bucket or not bundle_key.startswith(
        f"{staging_key}/control/"
    ):
        raise ExportError("remote control bundle is outside the approved staging prefix")
    args = [
        "python3",
        "$WORK/data_layer_archive_prod_export.py",
        "host-export",
        "--table",
        table,
        "--instance-id",
        instance_id,
        "--staging-s3-base-uri",
        staging_s3_base_uri,
        "--hold-started-at",
        hold_started_at,
        "--legacy-upper-exclusive",
        legacy_upper_exclusive,
        "--cursor-before-json",
        cursor_before_json,
        "--timeout-seconds",
        str(timeout_seconds),
        "--max-rows",
        str(max_rows),
        "--max-logical-bytes",
        str(max_logical_bytes),
        "--confirm",
        PROD_EXPORT_CONFIRMATION,
    ]
    rendered_args = " ".join(
        item if item.startswith("$WORK/") else shlex.quote(item) for item in args
    )
    names = sorted(_remote_bundle_sources())
    cleanup_paths = " ".join(f'"$WORK/{name}"' for name in names)
    lines = [
        "set -euo pipefail",
        'WORK="$(mktemp -d /tmp/tokenkey-prod-archive-export.XXXXXX)"',
        'ARCHIVE="$WORK/control.tar.gz"',
        "cleanup() {",
        f'  rm -f "$ARCHIVE" {cleanup_paths}',
        '  rmdir "$WORK" 2>/dev/null || true',  # preflight-allow: swallow
        "}",
        "trap cleanup EXIT",
        f"aws s3 cp --region {canary.PROD_REGION} --only-show-errors "
        f"{shlex.quote(bundle_s3_uri)} \"$ARCHIVE\"",
        'ACTUAL_SHA256="$(sha256sum "$ARCHIVE" | awk \'{print $1}\')"',
        f'test "$ACTUAL_SHA256" = {shlex.quote(bundle_sha256)}',
        'tar -xzf "$ARCHIVE" -C "$WORK"',
        'rm -f "$ARCHIVE"',
        f'PYTHONPATH="$WORK" {rendered_args}',
    ]
    return "\n".join(lines)


def host_export(
    *,
    table: str,
    instance_id: str,
    staging_s3_base_uri: str,
    hold_started_at: str,
    legacy_upper_exclusive: str,
    cursor_before_json: str,
    confirmation: str,
    timeout_seconds: int,
    max_rows: int,
    max_logical_bytes: int,
) -> dict[str, Any]:
    if confirmation != PROD_EXPORT_CONFIRMATION:
        raise ExportError("production export refused: confirmation token is invalid")
    cursor_before: dict[str, Any] | None
    if cursor_before_json in ("", "null"):
        cursor_before = None
    else:
        try:
            parsed = json.loads(cursor_before_json)
        except json.JSONDecodeError as exc:
            raise ExportError("export cursor_before_json is invalid") from exc
        if parsed is not None and (
            not isinstance(parsed, dict) or set(parsed) != {"created_at", "id"}
        ):
            raise ExportError("export cursor_before_json is invalid")
        cursor_before = parsed
    hold_verification = cleanup_hold_remote.verify_hold(hold_started_at)
    with tempfile.TemporaryDirectory(prefix="tokenkey-prod-archive-export-") as temp:
        sealed = seal_prod_export_batch(
            pathlib.Path(temp) / "batches",
            table=table,
            instance_id=instance_id,
            staging_s3_base_uri=staging_s3_base_uri,
            cursor_before=cursor_before,
            legacy_upper_exclusive=legacy_upper_exclusive,
            timeout_seconds=timeout_seconds,
            max_rows=max_rows,
            max_logical_bytes=max_logical_bytes,
        )
        uploaded = canary.upload_committed_batch(sealed["batch_dir"])
        manifest = json.loads(
            (pathlib.Path(sealed["batch_dir"]) / "manifest.json").read_text(
                encoding="utf-8"
            )
        )
        return {
            **uploaded,
            "export": manifest["export"],
            "metrics": sealed.get("metrics"),
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


def _validated_export_receipt(
    receipt: dict[str, Any], *, staging_base: str, expected_hold_started_at: str
) -> dict[str, Any]:
    batch_id = receipt.get("batch_id")
    if not isinstance(batch_id, str) or not batch_id.startswith("prod-export-"):
        raise ExportError("production export receipt has an invalid batch id")
    expected_prefix = f"{staging_base}/{batch_id}"
    manifest_sha256 = receipt.get("manifest_sha256")
    objects = receipt.get("objects")
    hold = receipt.get("cleanup_hold")
    export_block = receipt.get("export")
    if (
        receipt.get("mode") != "prod_archive_export_canary_upload"
        or receipt.get("s3_prefix") != expected_prefix
        or receipt.get("manifest_uploaded_last") is not True
        or receipt.get("source_mutated") is not False
        or receipt.get("deletion_authorized") is not False
        or not isinstance(manifest_sha256, str)
        or not isinstance(objects, list)
        or not objects
        or not isinstance(hold, dict)
        or hold.get("hold_started_at") != expected_hold_started_at
        or not isinstance(export_block, dict)
        or export_block.get("export_scope") != rehearsal.PROD_EXPORT_SCOPE_LEGACY_COLD
    ):
        raise ExportError("production export receipt failed commit validation")
    return receipt


def run_export_batch(args: argparse.Namespace) -> dict[str, Any]:
    if args.confirm != PROD_EXPORT_CONFIRMATION:
        raise ExportError("production export refused: confirmation token is invalid")
    ledger = load_ledger(args.ledger)
    if not ledger["more_cold_rows_remaining"]:
        raise ExportError("export ledger reports no remaining legacy cold rows")
    request = _validated_request(
        table=ledger["table"],
        timeout_seconds=args.timeout_seconds,
        max_rows=args.max_rows,
        max_logical_bytes=args.max_logical_bytes,
    )
    if request["table"] != ledger["table"]:
        raise ExportError("export table does not match ledger")
    legacy_upper = str(ledger["legacy_upper_exclusive"])
    instance_id = canary._prod_instance()
    hold_receipt = cleanup_hold.verify_receipt_for_instance(
        args.cleanup_hold_receipt, instance_id
    )
    hold_verification = cleanup_hold.verify(args.cleanup_hold_receipt)
    bucket = canary._stack_output(canary.BACKUP_STACK, canary.BACKUP_BUCKET_OUTPUT)
    retention_days = canary._stack_parameter(canary.BACKUP_STACK, "RetentionDays")
    if retention_days != str(canary.BACKUP_RETENTION_DAYS):
        raise ExportError(
            "production export staging requires the approved 7-day retention"
        )
    staging_base = _validated_staging_base(f"s3://{bucket}/{S3_KEY_BASE}")
    remote_bundle = stage_remote_bundle(staging_base)
    cursor_before = ledger.get("cursor_after")
    cursor_json = "null" if cursor_before is None else _canonical_json(cursor_before)
    remote_script = _remote_host_command(
        table=ledger["table"],
        instance_id=instance_id,
        staging_s3_base_uri=staging_base,
        bundle_s3_uri=remote_bundle["uri"],
        bundle_sha256=remote_bundle["sha256"],
        hold_started_at=hold_receipt["hold_started_at"],
        cursor_before_json=cursor_json,
        legacy_upper_exclusive=legacy_upper,
        timeout_seconds=request["timeout_seconds"],
        max_rows=request["max_rows"],
        max_logical_bytes=request["max_logical_bytes"],
    )
    upload = canary._run_ssm(
        instance_id,
        remote_script,
        timeout_seconds=args.ssm_timeout_seconds,
    )
    upload = _validated_export_receipt(
        upload,
        staging_base=staging_base,
        expected_hold_started_at=hold_receipt["hold_started_at"],
    )
    evidence_root = pathlib.Path(args.evidence_root).expanduser().resolve()
    evidence_root.mkdir(parents=True, exist_ok=True)
    batch_dir = canary._download_committed_batch(
        upload["s3_prefix"],
        upload["batch_id"],
        evidence_root,
        expected_manifest_sha256=upload["manifest_sha256"],
    )
    verification = rehearsal.verify_batch(batch_dir)
    restore_result: dict[str, Any] | None = None
    if args.verify_restore:
        rehearsal._postgres_dsn_info(args.restore_target_dsn, target=True)
        restore_result = rehearsal.restore_postgres_random(
            batch_dir,
            args.restore_target_dsn,
            seed=args.seed,
            timeout_seconds=request["timeout_seconds"],
        )
    export_meta = json.loads((batch_dir / "manifest.json").read_text())["export"]
    ledger["cursor_after"] = export_meta["cursor_after"]
    ledger["more_cold_rows_remaining"] = export_meta["more_cold_rows_remaining"]
    ledger["completed_batches"].append(
        {
            "batch_id": upload["batch_id"],
            "manifest_sha256": upload["manifest_sha256"],
            "s3_prefix": upload["s3_prefix"],
            "row_count": verification["row_count"],
            "cursor_after": export_meta["cursor_after"],
            "more_cold_rows_remaining": export_meta["more_cold_rows_remaining"],
        }
    )
    _atomic_json(pathlib.Path(args.ledger).expanduser().resolve(), ledger)
    return {
        "mode": "prod_archive_export_batch_complete",
        "environment": "prod",
        "instance_id": instance_id,
        "ledger": {
            "path": str(args.ledger),
            "cursor_after": ledger["cursor_after"],
            "more_cold_rows_remaining": ledger["more_cold_rows_remaining"],
            "completed_batch_count": len(ledger["completed_batches"]),
        },
        "upload": upload,
        "verify": verification,
        "restore": restore_result,
        "cleanup_hold": {
            "hold_started_at": hold_receipt["hold_started_at"],
            "controller_verified_at": hold_verification["server_clock"],
        },
        "batch_dir": str(batch_dir),
        "source_mutated": False,
        "deletion_authorized": False,
        "production_export_executed": True,
    }


def _add_limits(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--table", choices=rehearsal.PROD_CANARY_TABLES, required=True)
    parser.add_argument(
        "--legacy-upper-exclusive",
        default=rehearsal.PROD_LEGACY_UPPER_EXCLUSIVE,
        help="exclusive upper created_at bound for legacy scope",
    )
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

    plan = commands.add_parser("plan", help="offline export plan")
    _add_limits(plan)

    init_ledger_parser = commands.add_parser(
        "init-ledger", help="create a continuation ledger"
    )
    init_ledger_parser.add_argument("--ledger", required=True)
    init_ledger_parser.add_argument("--table", choices=rehearsal.PROD_CANARY_TABLES, required=True)
    init_ledger_parser.add_argument(
        "--legacy-upper-exclusive", default=rehearsal.PROD_LEGACY_UPPER_EXCLUSIVE
    )

    run = commands.add_parser("run-batch", help="export one legacy cold batch")
    run.add_argument("--ledger", required=True)
    run.add_argument("--evidence-root", required=True)
    run.add_argument("--cleanup-hold-receipt", required=True)
    run.add_argument("--ssm-timeout-seconds", type=int, default=900)
    run.add_argument("--confirm", required=True)
    run.add_argument("--timeout-seconds", type=int, default=DEFAULT_TIMEOUT_SECONDS)
    run.add_argument("--max-rows", type=int, default=DEFAULT_MAX_ROWS)
    run.add_argument(
        "--max-logical-bytes", type=int, default=DEFAULT_MAX_LOGICAL_BYTES
    )
    run.add_argument("--verify-restore", action="store_true")
    run.add_argument("--restore-target-dsn", default="")
    run.add_argument("--seed", type=int, default=0)

    host = commands.add_parser("host-export", help=argparse.SUPPRESS)
    host.add_argument("--table", choices=rehearsal.PROD_CANARY_TABLES, required=True)
    host.add_argument("--instance-id", required=True)
    host.add_argument("--staging-s3-base-uri", required=True)
    host.add_argument("--hold-started-at", required=True)
    host.add_argument("--legacy-upper-exclusive", required=True)
    host.add_argument("--cursor-before-json", required=True)
    host.add_argument("--timeout-seconds", type=int, default=DEFAULT_TIMEOUT_SECONDS)
    host.add_argument("--max-rows", type=int, default=DEFAULT_MAX_ROWS)
    host.add_argument(
        "--max-logical-bytes", type=int, default=DEFAULT_MAX_LOGICAL_BYTES
    )
    host.add_argument("--confirm", required=True)

    return parser


def main(argv: Iterable[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.command == "plan":
            payload = build_plan(
                table=args.table,
                legacy_upper_exclusive=args.legacy_upper_exclusive,
                timeout_seconds=args.timeout_seconds,
                max_rows=args.max_rows,
                max_logical_bytes=args.max_logical_bytes,
            )
        elif args.command == "init-ledger":
            payload = init_ledger(
                args.ledger,
                table=args.table,
                legacy_upper_exclusive=args.legacy_upper_exclusive,
            )
        elif args.command == "run-batch":
            if args.verify_restore:
                if not args.restore_target_dsn or args.seed <= 0:
                    raise ExportError(
                        "verify-restore requires restore-target-dsn and seed > 0"
                    )
            elif args.restore_target_dsn or args.seed > 0:
                raise ExportError(
                    "restore options require --verify-restore"
                )
            if not 30 <= args.ssm_timeout_seconds <= 900:
                raise ExportError("ssm_timeout_seconds must be between 30 and 900")
            payload = run_export_batch(args)
        elif args.command == "host-export":
            payload = host_export(
                table=args.table,
                instance_id=args.instance_id,
                staging_s3_base_uri=args.staging_s3_base_uri,
                hold_started_at=args.hold_started_at,
                legacy_upper_exclusive=args.legacy_upper_exclusive,
                cursor_before_json=args.cursor_before_json,
                confirmation=args.confirm,
                timeout_seconds=args.timeout_seconds,
                max_rows=args.max_rows,
                max_logical_bytes=args.max_logical_bytes,
            )
        else:  # pragma: no cover
            raise ExportError(f"unsupported command: {args.command}")
        print(_canonical_json(payload))
    except (
        ExportError,
        canary.CanaryError,
        rehearsal.RehearsalError,
        cleanup_hold.HoldControlError,
        cleanup_hold_remote.HoldError,
    ) as exc:
        print(f"production archive export refused: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
