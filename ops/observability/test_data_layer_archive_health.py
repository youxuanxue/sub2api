#!/usr/bin/env python3
"""Behavior tests for checked-in archive health evidence validation."""

from __future__ import annotations

import json
import pathlib
import sys
import tempfile
import unittest


_DIR = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(_DIR))

import data_layer_archive_health as health  # noqa: E402


class DataLayerArchiveHealthTest(unittest.TestCase):
    def test_checked_in_ledgers_pass_their_owner_validator(self) -> None:
        signal = health.build_signal()
        self.assertEqual(
            {ledger["table"] for ledger in signal["ledgers"]},
            {"ops_error_logs", "ops_system_logs"},
        )
        self.assertIsInstance(signal["hold_started_at"], str)
        self.assertFalse(signal["closeout_complete"])

    def test_minimal_unvalidated_json_cannot_report_closeout_complete(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            (root / "US-039-prod-cleanup-hold-20260721.json").write_text(
                json.dumps({"hold_started_at": "2026-07-21T00:00:00Z"}),
                encoding="utf-8",
            )
            for table, slug in (
                ("ops_error_logs", "ops-error-logs"),
                ("ops_system_logs", "ops-system-logs"),
            ):
                (root / f"US-040-{slug}-export-ledger.json").write_text(
                    json.dumps(
                        {
                            "table": table,
                            "completed_batches": [
                                {"cutoff_exclusive": "2026-07-01T00:00:00Z"}
                            ],
                            "legacy_upper_exclusive": "2026-07-01T00:00:00Z",
                            "more_cold_rows_remaining": False,
                        }
                    ),
                    encoding="utf-8",
                )
                (root / f"US-040-{slug}-archive-closeout.json").write_text(
                    json.dumps(
                        {
                            "table": table,
                            "restore_verified_at": "2026-08-03T00:00:00Z",
                        }
                    ),
                    encoding="utf-8",
                )

            signal = health.build_signal(root)

        self.assertEqual(signal["ledgers"], [])
        self.assertFalse(signal["closeout_complete"])
        self.assertEqual(signal["restore_verified_at"], [])
        self.assertIsNone(signal["hold_started_at"])
        self.assertEqual(
            set(signal["evidence_errors"]),
            {
                "cleanup_hold",
                "ops_error_logs:export_ledger",
                "ops_error_logs:closeout_receipt",
                "ops_system_logs:export_ledger",
                "ops_system_logs:closeout_receipt",
            },
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
