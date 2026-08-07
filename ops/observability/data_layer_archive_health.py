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
ATTACHMENTS = REPO / ".testing" / "user-stories" / "attachments"
TABLES = ("ops_error_logs", "ops_system_logs")
ARCHIVE = REPO / "ops" / "archive"
sys.path.insert(0, str(ARCHIVE))

import data_layer_archive_cleanup_hold as cleanup_hold  # noqa: E402
import data_layer_archive_closeout as closeout  # noqa: E402
import data_layer_archive_prod_export as export  # noqa: E402
import data_layer_archive_promote_batch as promote  # noqa: E402


def _sha256(path: pathlib.Path) -> str:
    try:
        return hashlib.sha256(path.read_bytes()).hexdigest()
    except OSError as exc:
        raise ValueError(f"cannot hash archive evidence: {path}") from exc


def _latest_hold_receipt(attachments: pathlib.Path) -> tuple[pathlib.Path, dict[str, Any]]:
    candidates: list[tuple[dt.datetime, pathlib.Path, dict[str, Any]]] = []
    for path in attachments.glob("US-039-prod-cleanup-hold-*.json"):
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


def build_signal(attachments: pathlib.Path = ATTACHMENTS) -> dict[str, Any]:
    slugs = {
        "ops_error_logs": "ops-error-logs",
        "ops_system_logs": "ops-system-logs",
    }
    ledgers: list[dict[str, Any]] = []
    restores: list[str] = []
    closeout_tables: set[str] = set()
    evidence_errors: list[str] = []
    hold_path = attachments / "US-039-prod-cleanup-hold-missing.json"
    try:
        hold_path, hold = _latest_hold_receipt(attachments)
    except (cleanup_hold.HoldControlError, OSError):
        hold = {}
        evidence_errors.append("cleanup_hold")
    for table in TABLES:
        slug = slugs[table]
        export_path = attachments / f"US-040-{slug}-export-ledger.json"
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

        closeout_path = attachments / f"US-040-{slug}-archive-closeout.json"
        if not closeout_path.exists():
            continue
        promote_path = attachments / f"US-040-{slug}-promote-ledger.json"
        try:
            receipt = closeout.load_closeout_receipt(closeout_path)
            promote.load_promote_ledger(promote_path)
            if (
                receipt.get("table") != table
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
    return {
        "ledgers": ledgers,
        "hold_started_at": hold.get("hold_started_at"),
        "closeout_complete": closeout_tables == set(TABLES),
        "restore_verified_at": restores,
        "evidence_errors": sorted(set(evidence_errors)),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--attachments", type=pathlib.Path, default=ATTACHMENTS)
    args = parser.parse_args()
    print("ARCHIVESTATS " + json.dumps(build_signal(args.attachments), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
