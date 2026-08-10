#!/usr/bin/env python3
"""Derive archive/hold/restore health from the checked-in production evidence."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import pathlib
import sys
from typing import Any


REPO = pathlib.Path(__file__).resolve().parents[2]
ARCHIVE = REPO / "ops" / "archive"
sys.path.insert(0, str(ARCHIVE))

import data_layer_archive_cleanup_hold as cleanup_hold  # noqa: E402
import data_layer_archive_closeout as closeout  # noqa: E402
import data_layer_archive_prod_export as export  # noqa: E402
import data_layer_archive_promote_batch as promote  # noqa: E402
import data_layer_archive_rehearsal as rehearsal  # noqa: E402
import pipeline_status_loader as pipeline_status  # noqa: E402


def _sha256(path: pathlib.Path) -> str:
    try:
        return hashlib.sha256(path.read_bytes()).hexdigest()
    except OSError as exc:
        raise ValueError(f"cannot hash archive evidence: {path}") from exc


def _latest_hold_receipt(
    attachments: pathlib.Path, cleanup_hold_glob: str
) -> tuple[pathlib.Path, dict[str, Any]]:
    candidates: list[tuple[dt.datetime, pathlib.Path, dict[str, Any]]] = []
    for path in attachments.glob(cleanup_hold_glob):
        try:
            receipt = cleanup_hold._load_receipt(path)
            if (
                receipt.get("database_cleanup_enabled") is not False
                or receipt.get("api_cleanup_enabled") is not False
                or receipt.get("no_cleanup_after_hold") is not True
            ):
                raise cleanup_hold.HoldControlError(
                    "cleanup hold receipt does not prove cleanup is stopped"
                )
            timestamp = dt.datetime.fromisoformat(
                receipt["hold_started_at"].replace("Z", "+00:00")
            )
            if timestamp.tzinfo is None:
                raise ValueError("hold timestamp must include timezone")
        except (cleanup_hold.HoldControlError, ValueError, TypeError, OSError):
            continue
        candidates.append((timestamp, path, receipt))
    if not candidates:
        raise cleanup_hold.HoldControlError("no valid cleanup hold receipt")
    _, path, receipt = max(candidates, key=lambda item: item[0])
    return path, receipt


def _tail_batches_fully_promoted(
    export_ledger: dict[str, Any], promote_ledger: dict[str, Any]
) -> bool:
    export_batches = export_ledger.get("completed_batches")
    if not isinstance(export_batches, list) or not export_batches:
        return False
    promoted = {
        entry.get("batch_id")
        for entry in promote_ledger.get("promoted_batches", [])
        if isinstance(entry, dict) and isinstance(entry.get("batch_id"), str)
    }
    expected = {
        batch.get("batch_id")
        for batch in export_batches
        if isinstance(batch, dict) and isinstance(batch.get("batch_id"), str)
    }
    return bool(expected) and expected <= promoted


def build_signal(evidence_dir: pathlib.Path | None = None) -> dict[str, Any]:
    layout = pipeline_status.load_evidence_layout()
    if evidence_dir is None:
        evidence_dir = layout.evidence_dir
    ledgers: list[dict[str, Any]] = []
    tail_ledgers: list[dict[str, Any]] = []
    restores: list[str] = []
    closeout_tables: set[str] = set()
    tail_export_tables: set[str] = set()
    evidence_errors: list[str] = []
    hold: dict[str, Any] = {}
    hold_path: pathlib.Path | None = None
    try:
        hold_path, hold = _latest_hold_receipt(evidence_dir, layout.cleanup_hold_glob)
    except (cleanup_hold.HoldControlError, OSError):
        evidence_errors.append("cleanup_hold")
    for table in layout.tables:
        export_path = evidence_dir / layout.export_ledger_name(table)
        try:
            ledger = export.load_ledger(export_path)
            batches = ledger.get("completed_batches")
            final = batches[-1] if isinstance(batches, list) and batches else {}
            if not isinstance(final, dict):
                raise export.ExportError("export ledger final batch is invalid")
            ledgers.append(
                {
                    "table": table,
                    "legacy_upper_exclusive": ledger.get("legacy_upper_exclusive"),
                    "final_cutoff_exclusive": final.get("cutoff_exclusive") if isinstance(final, dict) else None,
                    "more_cold_rows_remaining": ledger.get("more_cold_rows_remaining"),
                }
            )
        except (export.ExportError, OSError):
            evidence_errors.append(f"{table}:export_ledger")

        closeout_path = evidence_dir / layout.closeout_receipt_name(table)
        if not closeout_path.exists():
            continue
        promote_path = evidence_dir / layout.promote_ledger_name(table)
        try:
            receipt = closeout.load_closeout_receipt(closeout_path)
            promote.load_promote_ledger(promote_path)
            if (
                hold_path is None
                or receipt.get("table") != table
                or receipt.get("instance_id") != hold.get("instance_id")
                or receipt.get("hold_started_at") != hold.get("hold_started_at")
                or receipt.get("export_ledger_sha256") != _sha256(export_path)
                or receipt.get("promote_ledger_sha256") != _sha256(promote_path)
                or receipt.get("cleanup_hold_receipt_sha256") != _sha256(hold_path)
            ):
                evidence_errors.append(f"{table}:closeout_binding")
                continue
        except (
            ValueError,
            closeout.CloseoutError,
            promote.PromoteError,
            OSError,
        ):
            evidence_errors.append(f"{table}:closeout_receipt")
            continue
        closeout_tables.add(table)
        restores.append(receipt["restore_verified_at"])

        tail_export_path = evidence_dir / layout.tail_export_ledger_name(table)
        tail_promote_path = evidence_dir / layout.tail_promote_ledger_name(table)
        try:
            tail_export = export.load_ledger(tail_export_path)
            if tail_export.get("table") != table:
                raise export.ExportError("tail export ledger table mismatch")
            if (
                tail_export.get("export_scope")
                != rehearsal.PROD_EXPORT_SCOPE_POST_LEGACY_COLD
            ):
                raise export.ExportError("tail export ledger scope is invalid")
            tail_batches = tail_export.get("completed_batches")
            final_tail = (
                tail_batches[-1]
                if isinstance(tail_batches, list) and tail_batches
                else {}
            )
            if not isinstance(final_tail, dict):
                raise export.ExportError("tail export ledger final batch is invalid")
            tail_promote = promote.load_promote_ledger(tail_promote_path)
            if not _tail_batches_fully_promoted(tail_export, tail_promote):
                evidence_errors.append(f"{table}:tail_promote_binding")
            elif tail_export.get("more_cold_rows_remaining") is not False:
                evidence_errors.append(f"{table}:tail_export_incomplete")
            else:
                tail_ledgers.append(
                    {
                        "table": table,
                        "export_scope": tail_export.get("export_scope"),
                        "legacy_lower_inclusive": tail_export.get(
                            "legacy_lower_inclusive"
                        ),
                        "final_cutoff_exclusive": final_tail.get("cutoff_exclusive"),
                        "more_cold_rows_remaining": tail_export.get(
                            "more_cold_rows_remaining"
                        ),
                    }
                )
                tail_export_tables.add(table)
        except (export.ExportError, promote.PromoteError, OSError):
            evidence_errors.append(f"{table}:tail_export_ledger")
    closeout_complete = closeout_tables == set(layout.tables)
    tail_export_complete = tail_export_tables == set(layout.tables)
    cleanup_release_complete = False
    cleanup_release_verified_at: str | None = None
    if closeout_complete and tail_export_complete:
        if hold_path is None:
            evidence_errors.append("cleanup_release")
        else:
            try:
                _, release = cleanup_hold._latest_release_receipt(
                    evidence_dir,
                    layout.cleanup_release_receipt_glob,
                    hold,
                    hold_path,
                )
                cleanup_release_complete = True
                cleanup_release_verified_at = release.get("verified_at")
            except cleanup_hold.HoldControlError:
                evidence_errors.append("cleanup_release")
    return {
        "ledgers": ledgers,
        "tail_ledgers": tail_ledgers,
        "hold_started_at": hold.get("hold_started_at"),
        "closeout_complete": closeout_complete,
        "tail_export_complete": tail_export_complete,
        "cleanup_release_complete": cleanup_release_complete,
        "cleanup_release_verified_at": cleanup_release_verified_at,
        "restore_verified_at": restores,
        "evidence_errors": sorted(set(evidence_errors)),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    layout = pipeline_status.load_evidence_layout()
    parser.add_argument(
        "--evidence-dir",
        type=pathlib.Path,
        default=None,
        help=f"override repo evidence directory (default: {layout.evidence_dir})",
    )
    args = parser.parse_args()
    evidence_dir = layout.evidence_dir if args.evidence_dir is None else args.evidence_dir
    print("ARCHIVESTATS " + json.dumps(build_signal(evidence_dir), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
