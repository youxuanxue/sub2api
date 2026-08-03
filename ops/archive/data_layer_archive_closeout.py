#!/usr/bin/env python3
"""Prove long-term archive restore before releasing the production cleanup hold."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import pathlib
import random
import re
import sys
import tempfile
from collections.abc import Iterable
from typing import Any

import data_layer_archive_cleanup_hold as cleanup_hold
import data_layer_archive_prod_canary as canary
import data_layer_archive_prod_export as export
import data_layer_archive_promote_batch as promote
import data_layer_archive_rehearsal as rehearsal


CLOSEOUT_CONFIRMATION = "tokenkey-prod-archive-closeout-v1"
CLOSEOUT_SCHEMA_VERSION = 1
CLOSEOUT_MODE = "prod_archive_reclaim_closeout"
SHA256_RE = re.compile(r"[0-9a-f]{64}")


class CloseoutError(RuntimeError):
    """Fail-closed archive closeout error."""


def _sha256_path(path: pathlib.Path) -> str:
    try:
        return rehearsal._sha256(path.read_bytes())
    except OSError as exc:
        raise CloseoutError(f"cannot read evidence file: {path}") from exc


def _canonical_timestamp(value: Any) -> str:
    try:
        return canary._canonical_timestamp(str(value))
    except (TypeError, ValueError, canary.CanaryError) as exc:
        raise CloseoutError("archive timestamp is invalid") from exc


def _valid_sha256(value: Any) -> bool:
    return isinstance(value, str) and SHA256_RE.fullmatch(value) is not None


def _validated_archive_prefix(value: Any, batch_id: str) -> str:
    if not isinstance(value, str):
        raise CloseoutError("promote receipt archive prefix is invalid")
    try:
        _, key = canary._s3_location(value)
    except canary.CanaryError as exc:
        raise CloseoutError("promote receipt archive prefix is invalid") from exc
    if key != f"{promote.ARCHIVE_KEY_BASE}/{batch_id}":
        raise CloseoutError("promote receipt archive prefix is invalid")
    return value


def _restored_artifact(
    verification: dict[str, Any], restore: dict[str, Any]
) -> dict[str, Any]:
    dataset = restore.get("selected_dataset")
    artifacts = verification.get("artifacts")
    if not isinstance(dataset, str) or not isinstance(artifacts, list):
        raise CloseoutError("long-term archive restore dataset is invalid")
    matches = [
        entry
        for entry in artifacts
        if isinstance(entry, dict) and entry.get("dataset") == dataset
    ]
    if len(matches) != 1:
        raise CloseoutError("long-term archive restore dataset is not unique")
    return matches[0]


def validate_ledger_pair(
    export_ledger_path: str | os.PathLike[str],
    promote_ledger_path: str | os.PathLike[str],
) -> dict[str, Any]:
    export_path = pathlib.Path(export_ledger_path).expanduser().resolve()
    promote_path = pathlib.Path(promote_ledger_path).expanduser().resolve()
    export_ledger = export.load_ledger(export_path)
    promote_ledger = promote.load_promote_ledger(promote_path)
    batches = export_ledger["completed_batches"]
    if not batches or export_ledger["more_cold_rows_remaining"] is not False:
        raise CloseoutError("export ledger has not reached the cold-row boundary")

    expected: dict[str, str] = {}
    for batch in batches:
        if not isinstance(batch, dict):
            raise CloseoutError("export ledger contains an invalid batch")
        batch_id = batch.get("batch_id")
        checksum = batch.get("manifest_sha256")
        if not isinstance(batch_id, str) or not _valid_sha256(checksum):
            raise CloseoutError("export ledger batch identity is invalid")
        if batch_id in expected:
            raise CloseoutError("export ledger contains a duplicate batch")
        expected[batch_id] = checksum

    promoted: dict[str, dict[str, Any]] = {}
    for receipt in promote_ledger["promoted_batches"]:
        if not isinstance(receipt, dict):
            raise CloseoutError("promote ledger contains an invalid receipt")
        batch_id = receipt.get("batch_id")
        if not isinstance(batch_id, str) or batch_id in promoted:
            raise CloseoutError("promote ledger batch identity is invalid")
        if (
            receipt.get("schema_version") != promote.PROMOTE_RECEIPT_SCHEMA
            or receipt.get("mode") != promote.PROMOTE_RECEIPT_MODE
            or receipt.get("environment") != "prod"
            or receipt.get("manifest_promoted_last") is not True
            or receipt.get("manifest_sha256") != expected.get(batch_id)
            or receipt.get("archive_standard_days") != promote.ARCHIVE_STANDARD_DAYS
            or receipt.get("archive_expire_days") != promote.ARCHIVE_EXPIRE_DAYS
            or receipt.get("source_mutated") is not False
            or receipt.get("deletion_authorized") is not False
        ):
            raise CloseoutError("promote receipt does not match the export ledger")
        _validated_archive_prefix(receipt.get("archive_s3_prefix"), batch_id)
        promoted[batch_id] = receipt
    if set(promoted) != set(expected):
        raise CloseoutError("not every exported batch is promoted exactly once")

    final_batch = batches[-1]
    cutoff = _canonical_timestamp(final_batch.get("cutoff_exclusive"))
    upper = _canonical_timestamp(export_ledger["legacy_upper_exclusive"])
    if cutoff < upper:
        raise CloseoutError("final export cutoff has not reached the partition upper bound")
    return {
        "table": export_ledger["table"],
        "legacy_upper_exclusive": upper,
        "final_cutoff_exclusive": cutoff,
        "batch_count": len(batches),
        "export_ledger_sha256": _sha256_path(export_path),
        "promote_ledger_sha256": _sha256_path(promote_path),
        "promoted": promoted,
    }


def _download_archive_batch(
    receipt: dict[str, Any],
    evidence_root: str | os.PathLike[str],
    *,
    command_runner: canary.CommandRunner = canary._command_output,
) -> pathlib.Path:
    batch_id = promote._validated_batch_id(str(receipt.get("batch_id")))
    archive_prefix = _validated_archive_prefix(
        receipt.get("archive_s3_prefix"), batch_id
    )
    root = pathlib.Path(evidence_root).expanduser().resolve()
    root.mkdir(parents=True, exist_ok=True)
    destination = root / batch_id

    def verify_download(path: pathlib.Path) -> None:
        verification = rehearsal.verify_batch(path)
        if verification["manifest_sha256"] != receipt["manifest_sha256"]:
            raise CloseoutError("long-term archive manifest checksum mismatch")

    if destination.exists():
        verify_download(destination)
        return destination
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
                canary.PROD_REGION,
                "--only-show-errors",
                f"{archive_prefix}/manifest.json",
                str(manifest_path),
            ]
        )
        try:
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise CloseoutError("long-term archive manifest is invalid") from exc
        if not isinstance(manifest, dict) or manifest.get("batch_id") != batch_id:
            raise CloseoutError("long-term archive batch identity mismatch")
        artifacts = manifest.get("artifacts")
        if not isinstance(artifacts, list) or not artifacts:
            raise CloseoutError("long-term archive manifest has no artifacts")
        for entry in artifacts:
            relative = entry.get("path") if isinstance(entry, dict) else None
            if not isinstance(relative, str) or pathlib.PurePosixPath(relative).name != relative:
                raise CloseoutError("long-term archive artifact path is invalid")
            command_runner(
                [
                    "aws",
                    "s3",
                    "cp",
                    "--region",
                    canary.PROD_REGION,
                    "--only-show-errors",
                    f"{archive_prefix}/{relative}",
                    str(temporary / relative),
                ]
            )
        verify_download(temporary)
        temporary.replace(destination)
        temporary_root.rmdir()
    except Exception:
        for child in temporary.glob("*"):
            child.unlink(missing_ok=True)
        temporary.rmdir()
        temporary_root.rmdir()
        raise
    return destination


def closeout(
    *,
    export_ledger_path: str | os.PathLike[str],
    promote_ledger_path: str | os.PathLike[str],
    cleanup_hold_receipt_path: str | os.PathLike[str],
    closeout_receipt_path: str | os.PathLike[str],
    evidence_root: str | os.PathLike[str],
    restore_target_dsn: str,
    seed: int,
    confirmation: str,
    command_runner: canary.CommandRunner = canary._command_output,
) -> dict[str, Any]:
    if confirmation != CLOSEOUT_CONFIRMATION:
        raise CloseoutError("archive closeout confirmation token is invalid")
    if isinstance(seed, bool) or not isinstance(seed, int) or seed <= 0:
        raise CloseoutError("archive closeout seed must be a positive integer")
    receipt_path = pathlib.Path(closeout_receipt_path).expanduser().resolve()
    if receipt_path.exists():
        raise CloseoutError("closeout receipt already exists; refuse to overwrite proof")

    ledger_proof = validate_ledger_pair(export_ledger_path, promote_ledger_path)
    instance_id = canary._prod_instance()
    hold_receipt = cleanup_hold.verify_receipt_for_instance(
        cleanup_hold_receipt_path, instance_id
    )
    hold_verification = cleanup_hold.verify(cleanup_hold_receipt_path)
    if hold_verification.get("instance_id") != instance_id:
        raise CloseoutError("cleanup hold verification reached a different instance")

    batch_id = random.Random(seed).choice(sorted(ledger_proof["promoted"]))
    promote_receipt = ledger_proof["promoted"][batch_id]
    batch_dir = _download_archive_batch(
        promote_receipt, evidence_root, command_runner=command_runner
    )
    verification = rehearsal.verify_batch(batch_dir)
    restored = rehearsal.restore_postgres_random(
        batch_dir,
        restore_target_dsn,
        seed=seed,
        timeout_seconds=export.DEFAULT_TIMEOUT_SECONDS,
    )
    restored_artifact = _restored_artifact(verification, restored)
    if (
        restored.get("verified") is not True
        or restored.get("expected_rows") != restored.get("restored_rows")
        or restored.get("expected_rows") != restored_artifact.get("row_count")
        or restored.get("logical_sha256") != restored_artifact.get("logical_sha256")
    ):
        raise CloseoutError("long-term archive restore proof is incomplete")

    now = dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")
    hold_path = pathlib.Path(cleanup_hold_receipt_path).expanduser().resolve()
    payload = {
        "schema_version": CLOSEOUT_SCHEMA_VERSION,
        "mode": CLOSEOUT_MODE,
        "environment": "prod",
        "instance_id": instance_id,
        "table": ledger_proof["table"],
        "legacy_upper_exclusive": ledger_proof["legacy_upper_exclusive"],
        "final_cutoff_exclusive": ledger_proof["final_cutoff_exclusive"],
        "batch_count": ledger_proof["batch_count"],
        "export_ledger_sha256": ledger_proof["export_ledger_sha256"],
        "promote_ledger_sha256": ledger_proof["promote_ledger_sha256"],
        "cleanup_hold_receipt_sha256": _sha256_path(hold_path),
        "hold_started_at": hold_receipt["hold_started_at"],
        "hold_verified_at": hold_verification["server_clock"],
        "restore_verified_at": now,
        "selected_batch_id": batch_id,
        "selected_archive_s3_prefix": promote_receipt["archive_s3_prefix"],
        "selected_manifest_sha256": promote_receipt["manifest_sha256"],
        "restore": restored,
        "cleanup_release_authorized": True,
        "deletion_authorized": False,
    }
    export._atomic_json(receipt_path, payload)
    return payload


def load_closeout_receipt(path: str | os.PathLike[str]) -> dict[str, Any]:
    try:
        payload = json.loads(
            pathlib.Path(path).expanduser().resolve().read_text(encoding="utf-8")
        )
    except (OSError, json.JSONDecodeError) as exc:
        raise CloseoutError("closeout receipt cannot be read") from exc
    restore = payload.get("restore") if isinstance(payload, dict) else None
    if (
        not isinstance(payload, dict)
        or payload.get("schema_version") != CLOSEOUT_SCHEMA_VERSION
        or payload.get("mode") != CLOSEOUT_MODE
        or payload.get("environment") != "prod"
        or cleanup_hold.INSTANCE_RE.fullmatch(str(payload.get("instance_id", "")))
        is None
        or payload.get("table") not in rehearsal.PROD_CANARY_TABLES
        or not isinstance(payload.get("batch_count"), int)
        or isinstance(payload.get("batch_count"), bool)
        or payload.get("batch_count") <= 0
        or not all(
            _valid_sha256(payload.get(field))
            for field in (
                "export_ledger_sha256",
                "promote_ledger_sha256",
                "cleanup_hold_receipt_sha256",
                "selected_manifest_sha256",
            )
        )
        or not isinstance(payload.get("selected_batch_id"), str)
        or not isinstance(payload.get("selected_archive_s3_prefix"), str)
        or payload.get("cleanup_release_authorized") is not True
        or payload.get("deletion_authorized") is not False
        or not isinstance(restore, dict)
        or restore.get("verified") is not True
        or restore.get("batch_id") != payload.get("selected_batch_id")
        or not isinstance(restore.get("selected_dataset"), str)
        or restore.get("expected_rows") != restore.get("restored_rows")
        or not isinstance(restore.get("expected_rows"), int)
        or isinstance(restore.get("expected_rows"), bool)
        or restore.get("expected_rows") < 0
        or not _valid_sha256(restore.get("logical_sha256"))
        or restore.get("deletion_authorized") is not False
    ):
        raise CloseoutError("closeout receipt failed validation")
    for field in ("hold_started_at", "hold_verified_at", "restore_verified_at"):
        _canonical_timestamp(payload.get(field))
    _validated_archive_prefix(
        payload.get("selected_archive_s3_prefix"), payload["selected_batch_id"]
    )
    if not (
        _canonical_timestamp(payload["hold_started_at"])
        <= _canonical_timestamp(payload["hold_verified_at"])
        <= _canonical_timestamp(payload["restore_verified_at"])
    ):
        raise CloseoutError("closeout receipt timestamps are out of order")
    if _canonical_timestamp(payload.get("final_cutoff_exclusive")) < _canonical_timestamp(
        payload.get("legacy_upper_exclusive")
    ):
        raise CloseoutError("closeout receipt does not cover the partition upper bound")
    return payload


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--export-ledger", required=True)
    parser.add_argument("--promote-ledger", required=True)
    parser.add_argument("--cleanup-hold-receipt", required=True)
    parser.add_argument("--closeout-receipt", required=True)
    parser.add_argument("--evidence-root", required=True)
    parser.add_argument("--restore-target-dsn", required=True)
    parser.add_argument("--seed", type=int, required=True)
    parser.add_argument("--confirm", required=True)
    return parser


def main(argv: Iterable[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        payload = closeout(
            export_ledger_path=args.export_ledger,
            promote_ledger_path=args.promote_ledger,
            cleanup_hold_receipt_path=args.cleanup_hold_receipt,
            closeout_receipt_path=args.closeout_receipt,
            evidence_root=args.evidence_root,
            restore_target_dsn=args.restore_target_dsn,
            seed=args.seed,
            confirmation=args.confirm,
        )
        print(rehearsal._canonical_json(payload))
    except (
        CloseoutError,
        cleanup_hold.HoldControlError,
        canary.CanaryError,
        export.ExportError,
        promote.PromoteError,
        rehearsal.RehearsalError,
    ) as exc:
        print(f"archive closeout refused: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
