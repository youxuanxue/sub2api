#!/usr/bin/env python3
"""Promote committed export batches from staging S3 to the long-term archive bucket."""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import sys
import tempfile
from collections.abc import Iterable
from typing import Any

import data_layer_archive_prod_canary as canary
import data_layer_archive_prod_export as export
import data_layer_archive_rehearsal as rehearsal


ARCHIVE_STACK = "tokenkey-stage0-archive"
ARCHIVE_KEY_BASE = "prod/ops-archive"
STAGING_EXPORT_BASE = export.S3_KEY_BASE
PROMOTE_CONFIRMATION = "tokenkey-prod-archive-promote-batch-v1"
PROMOTE_RECEIPT_SCHEMA = 1
PROMOTE_RECEIPT_MODE = "prod_archive_promote_receipt"
PROMOTE_LEDGER_SCHEMA = 1
PROMOTE_LEDGER_MODE = "prod_archive_promote_ledger"
ARCHIVE_STANDARD_DAYS = 90
ARCHIVE_EXPIRE_DAYS = 400


class PromoteError(canary.CanaryError):
    """Fail-closed archive promote error."""


def _canonical_json(value: Any) -> str:
    return rehearsal._canonical_json(value)


def _atomic_json(path: pathlib.Path, value: dict[str, Any]) -> None:
    export._atomic_json(path, value)


def _validated_batch_id(batch_id: str) -> str:
    if (
        not isinstance(batch_id, str)
        or not batch_id.startswith("prod-export-")
        or ".." in batch_id
        or "/" in batch_id
    ):
        raise PromoteError("batch id must be a prod-export batch identifier")
    return batch_id


def _validated_staging_prefix(prefix: str, *, batch_id: str) -> str:
    bucket, key = canary._s3_location(prefix)
    expected = f"{STAGING_EXPORT_BASE}/{batch_id}"
    if key != expected:
        raise PromoteError("staging prefix does not match the approved export layout")
    return f"s3://{bucket}/{key}"


def _archive_base_uri() -> str:
    bucket = canary._stack_output(ARCHIVE_STACK, "BucketName")
    archive_uri = canary._stack_output(ARCHIVE_STACK, "ArchiveS3Uri")
    _, key = canary._s3_location(archive_uri)
    if key != ARCHIVE_KEY_BASE:
        raise PromoteError("archive stack URI does not match the approved key base")
    return f"s3://{bucket}/{key}"


def build_plan(
    *,
    batch_id: str,
) -> dict[str, Any]:
    batch_id = _validated_batch_id(batch_id)
    staging_bucket = canary._stack_output(canary.BACKUP_STACK, "BucketName")
    staging_prefix = _validated_staging_prefix(
        f"s3://{staging_bucket}/{STAGING_EXPORT_BASE}/{batch_id}",
        batch_id=batch_id,
    )
    archive_base = _archive_base_uri()
    return {
        "schema_version": rehearsal.SCHEMA_VERSION,
        "mode": "prod_archive_promote_plan",
        "environment": "prod",
        "region": canary.PROD_REGION,
        "archive_stack": ARCHIVE_STACK,
        "staging_stack": canary.BACKUP_STACK,
        "batch_id": batch_id,
        "staging_s3_prefix": staging_prefix,
        "archive_s3_prefix": f"{archive_base}/{batch_id}",
        "archive_standard_days": ARCHIVE_STANDARD_DAYS,
        "archive_expire_days": ARCHIVE_EXPIRE_DAYS,
        "required_confirmation": PROMOTE_CONFIRMATION,
        "execution_authorized": False,
        "source_mutated": False,
        "deletion_authorized": False,
    }


def _head_object_bytes(uri: str, *, command_runner: canary.CommandRunner) -> int:
    bucket, key = canary._s3_location(uri)
    head = json.loads(
        command_runner(
            [
                "aws",
                "s3api",
                "head-object",
                "--region",
                canary.PROD_REGION,
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
        raise PromoteError("S3 head-object returned invalid JSON")
    content_length = head.get("ContentLength")
    if not isinstance(content_length, int) or isinstance(content_length, bool):
        raise PromoteError("S3 head-object content length is invalid")
    return content_length


def _download_manifest(
    staging_prefix: str,
    *,
    command_runner: canary.CommandRunner,
) -> tuple[dict[str, Any], bytes, str]:
    with tempfile.TemporaryDirectory(prefix="tokenkey-archive-promote-") as temp:
        manifest_path = pathlib.Path(temp) / "manifest.json"
        command_runner(
            [
                "aws",
                "s3",
                "cp",
                "--region",
                canary.PROD_REGION,
                "--only-show-errors",
                f"{staging_prefix}/manifest.json",
                str(manifest_path),
            ]
        )
        manifest_bytes = manifest_path.read_bytes()
        try:
            manifest = json.loads(manifest_bytes.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise PromoteError("staging manifest is invalid JSON") from exc
    if not isinstance(manifest, dict):
        raise PromoteError("staging manifest must be a JSON object")
    manifest_sha256 = rehearsal._sha256(manifest_bytes)
    return manifest, manifest_bytes, manifest_sha256


def _copy_object(
    source_uri: str,
    dest_uri: str,
    *,
    sha256: str,
    command_runner: canary.CommandRunner,
) -> dict[str, Any]:
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
            f"sha256={sha256}",
            source_uri,
            dest_uri,
        ]
    )
    return canary._head_s3_object(
        dest_uri,
        expected_bytes=_head_object_bytes(source_uri, command_runner=command_runner),
        expected_sha256=sha256,
        command_runner=command_runner,
    )


def promote_batch(
    *,
    batch_id: str,
    confirmation: str,
    command_runner: canary.CommandRunner = canary._command_output,
) -> dict[str, Any]:
    if confirmation != PROMOTE_CONFIRMATION:
        raise PromoteError("archive promote refused: confirmation token is invalid")
    batch_id = _validated_batch_id(batch_id)
    plan = build_plan(batch_id=batch_id)
    staging_prefix = plan["staging_s3_prefix"]
    archive_prefix = plan["archive_s3_prefix"]
    manifest, _manifest_bytes, manifest_sha256 = _download_manifest(
        staging_prefix, command_runner=command_runner
    )
    if manifest.get("batch_id") != batch_id:
        raise PromoteError("staging manifest batch id mismatch")
    if manifest.get("mode") != rehearsal.PROD_EXPORT_MODE:
        raise PromoteError("staging manifest is not an export batch")
    if manifest.get("staging_s3_prefix") != staging_prefix:
        raise PromoteError("staging manifest prefix mismatch")
    if manifest.get("source_mutated") is not False or manifest.get("deletion_authorized") is not False:
        raise PromoteError("staging manifest failed safety validation")
    artifacts = manifest.get("artifacts")
    if not isinstance(artifacts, list) or not artifacts:
        raise PromoteError("staging manifest has no artifacts")
    copied: list[dict[str, Any]] = []
    for entry in artifacts:
        if not isinstance(entry, dict):
            raise PromoteError("staging artifact entry is invalid")
        name = entry.get("path")
        artifact_sha256 = entry.get("artifact_sha256")
        if (
            not isinstance(name, str)
            or pathlib.PurePosixPath(name).name != name
            or not isinstance(artifact_sha256, str)
        ):
            raise PromoteError("staging artifact metadata is invalid")
        copied.append(
            _copy_object(
                f"{staging_prefix}/{name}",
                f"{archive_prefix}/{name}",
                sha256=artifact_sha256,
                command_runner=command_runner,
            )
        )
    copied.append(
        _copy_object(
            f"{staging_prefix}/manifest.json",
            f"{archive_prefix}/manifest.json",
            sha256=manifest_sha256,
            command_runner=command_runner,
        )
    )
    if not copied[-1]["uri"].endswith("/manifest.json"):
        raise PromoteError("archive manifest was not promoted last")
    return {
        "schema_version": PROMOTE_RECEIPT_SCHEMA,
        "mode": PROMOTE_RECEIPT_MODE,
        "environment": "prod",
        "batch_id": batch_id,
        "staging_s3_prefix": staging_prefix,
        "archive_s3_prefix": archive_prefix,
        "manifest_sha256": manifest_sha256,
        "objects": copied,
        "manifest_promoted_last": True,
        "archive_standard_days": ARCHIVE_STANDARD_DAYS,
        "archive_expire_days": ARCHIVE_EXPIRE_DAYS,
        "source_mutated": False,
        "deletion_authorized": False,
    }


def init_promote_ledger(path: str | os.PathLike[str]) -> dict[str, Any]:
    ledger_path = pathlib.Path(path).expanduser().resolve()
    if ledger_path.exists():
        raise PromoteError("promote ledger already exists; refuse to overwrite")
    payload = {
        "schema_version": PROMOTE_LEDGER_SCHEMA,
        "mode": PROMOTE_LEDGER_MODE,
        "environment": "prod",
        "promoted_batches": [],
        "source_mutated": False,
        "deletion_authorized": False,
    }
    _atomic_json(ledger_path, payload)
    return payload


def load_promote_ledger(path: str | os.PathLike[str]) -> dict[str, Any]:
    try:
        payload = json.loads(
            pathlib.Path(path).expanduser().resolve().read_text(encoding="utf-8")
        )
    except (OSError, json.JSONDecodeError) as exc:
        raise PromoteError("promote ledger cannot be read") from exc
    if (
        not isinstance(payload, dict)
        or payload.get("schema_version") != PROMOTE_LEDGER_SCHEMA
        or payload.get("mode") != PROMOTE_LEDGER_MODE
        or payload.get("environment") != "prod"
        or not isinstance(payload.get("promoted_batches"), list)
    ):
        raise PromoteError("promote ledger failed validation")
    return payload


def promote_export_ledger(
    *,
    export_ledger_path: str | os.PathLike[str],
    promote_ledger_path: str | os.PathLike[str],
    confirmation: str,
    command_runner: canary.CommandRunner = canary._command_output,
) -> dict[str, Any]:
    export_ledger = export.load_ledger(export_ledger_path)
    promote_path = pathlib.Path(promote_ledger_path).expanduser().resolve()
    if promote_path.exists():
        promote_ledger = load_promote_ledger(promote_path)
    else:
        promote_ledger = init_promote_ledger(promote_path)
    already = {
        entry.get("batch_id")
        for entry in promote_ledger["promoted_batches"]
        if isinstance(entry, dict)
    }
    receipts: list[dict[str, Any]] = []
    for batch in export_ledger["completed_batches"]:
        if not isinstance(batch, dict):
            raise PromoteError("export ledger batch entry is invalid")
        batch_id = batch.get("batch_id")
        if not isinstance(batch_id, str):
            raise PromoteError("export ledger batch id is invalid")
        if batch_id in already:
            continue
        receipt = promote_batch(
            batch_id=batch_id,
            confirmation=confirmation,
            command_runner=command_runner,
        )
        expected_manifest_sha256 = batch.get("manifest_sha256")
        if receipt["manifest_sha256"] != expected_manifest_sha256:
            raise PromoteError("promoted manifest checksum does not match export ledger")
        promote_ledger["promoted_batches"].append(receipt)
        receipts.append(receipt)
        already.add(batch_id)
    _atomic_json(promote_path, promote_ledger)
    export_batches = len(export_ledger["completed_batches"])
    promoted_batches = len(promote_ledger["promoted_batches"])
    return {
        "mode": "prod_archive_promote_ledger_complete",
        "export_ledger": str(export_ledger_path),
        "promote_ledger": str(promote_path),
        "export_batch_count": export_batches,
        "promoted_batch_count": promoted_batches,
        "newly_promoted": len(receipts),
        "drop_ready": (
            export_ledger.get("more_cold_rows_remaining") is False
            and promoted_batches >= export_batches
            and export_batches > 0
        ),
        "receipts": receipts,
        "source_mutated": False,
        "deletion_authorized": False,
    }


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)

    plan = commands.add_parser("plan", help="offline promote plan")
    plan.add_argument("--batch-id", required=True)

    promote = commands.add_parser("promote", help="promote one export batch")
    promote.add_argument("--batch-id", required=True)
    promote.add_argument("--confirm", required=True)

    ledger = commands.add_parser(
        "promote-ledger", help="promote all batches listed in an export ledger"
    )
    ledger.add_argument("--export-ledger", required=True)
    ledger.add_argument("--promote-ledger", required=True)
    ledger.add_argument("--confirm", required=True)

    init_ledger = commands.add_parser("init-promote-ledger", help="create promote ledger")
    init_ledger.add_argument("--promote-ledger", required=True)

    return parser


def main(argv: Iterable[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.command == "plan":
            payload = build_plan(batch_id=args.batch_id)
        elif args.command == "promote":
            payload = promote_batch(batch_id=args.batch_id, confirmation=args.confirm)
        elif args.command == "init-promote-ledger":
            payload = init_promote_ledger(args.promote_ledger)
        elif args.command == "promote-ledger":
            payload = promote_export_ledger(
                export_ledger_path=args.export_ledger,
                promote_ledger_path=args.promote_ledger,
                confirmation=args.confirm,
            )
        else:  # pragma: no cover
            raise PromoteError(f"unsupported command: {args.command}")
        print(_canonical_json(payload))
    except (PromoteError, export.ExportError, canary.CanaryError, rehearsal.RehearsalError) as exc:
        print(f"archive promote refused: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
