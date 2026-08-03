#!/usr/bin/env python3
"""Derive archive/hold/restore health from the checked-in production evidence."""

from __future__ import annotations

import argparse
import json
import pathlib
from typing import Any


REPO = pathlib.Path(__file__).resolve().parents[2]
ATTACHMENTS = REPO / ".testing" / "user-stories" / "attachments"
TABLES = ("ops_error_logs", "ops_system_logs")


def _load(path: pathlib.Path) -> dict[str, Any] | None:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    return value if isinstance(value, dict) else None


def build_signal(attachments: pathlib.Path = ATTACHMENTS) -> dict[str, Any]:
    slugs = {
        "ops_error_logs": "ops-error-logs",
        "ops_system_logs": "ops-system-logs",
    }
    ledgers: list[dict[str, Any]] = []
    restores: list[str] = []
    closeout_tables: set[str] = set()
    for table in TABLES:
        slug = slugs[table]
        ledger = _load(attachments / f"US-040-{slug}-export-ledger.json")
        if ledger is not None:
            batches = ledger.get("completed_batches")
            final = batches[-1] if isinstance(batches, list) and batches else {}
            ledgers.append(
                {
                    "table": table,
                    "legacy_upper_exclusive": ledger.get("legacy_upper_exclusive"),
                    "final_cutoff_exclusive": final.get("cutoff_exclusive") if isinstance(final, dict) else None,
                    "more_cold_rows_remaining": ledger.get("more_cold_rows_remaining"),
                }
            )
        closeout = _load(attachments / f"US-040-{slug}-archive-closeout.json")
        if closeout is not None and closeout.get("table") == table:
            closeout_tables.add(table)
            if isinstance(closeout.get("restore_verified_at"), str):
                restores.append(closeout["restore_verified_at"])
    hold = _load(attachments / "US-039-prod-cleanup-hold-20260721.json") or {}
    return {
        "ledgers": ledgers,
        "hold_started_at": hold.get("hold_started_at"),
        "closeout_complete": closeout_tables == set(TABLES),
        "restore_verified_at": restores,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--attachments", type=pathlib.Path, default=ATTACHMENTS)
    args = parser.parse_args()
    print("ARCHIVESTATS " + json.dumps(build_signal(args.attachments), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
