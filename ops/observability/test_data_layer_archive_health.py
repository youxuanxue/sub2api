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
        self.assertTrue(signal["closeout_complete"])
        self.assertTrue(signal["tail_export_complete"])
        self.assertTrue(signal["cleanup_release_complete"])
        self.assertIsInstance(signal["cleanup_release_verified_at"], str)
        self.assertEqual(
            {ledger["table"] for ledger in signal["tail_ledgers"]},
            {"ops_error_logs", "ops_system_logs"},
        )
        self.assertEqual(signal["evidence_errors"], [])

    def test_latest_valid_hold_receipt_is_selected(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            for date, started_at in (
                ("20260721", "2026-07-21T00:00:00Z"),
                ("20260807", "2026-08-07T02:40:22Z"),
            ):
                (root / f"US-039-prod-cleanup-hold-{date}.json").write_text(
                    json.dumps(
                        {
                            "action": "apply",
                            "mode": "prod_archive_cleanup_hold",
                            "environment": "prod",
                            "instance_id": "i-0123456789abcdef0",
                            "hold_active": True,
                            "hold_started_at": started_at,
                            "database_cleanup_enabled": False,
                            "api_cleanup_enabled": False,
                            "cleanup_lock_active": False,
                            "no_cleanup_after_hold": True,
                            "deletion_authorized": False,
                            "reload_proven": True,
                            "settings_mutated": True,
                            "settings_sha256": "a" * 64,
                            "settings_sha256_before": "b" * 64,
                            "settings_sha256_after": "a" * 64,
                            "verified_at": started_at,
                        }
                    ),
                    encoding="utf-8",
                )
            (root / "US-039-prod-cleanup-hold-20260808.json").write_text(
                json.dumps(
                    {
                        "action": "apply",
                        "mode": "prod_archive_cleanup_hold",
                        "environment": "prod",
                        "instance_id": "i-0123456789abcdef0",
                        "hold_active": True,
                        "hold_started_at": "2026-08-08T00:00:00Z",
                        "database_cleanup_enabled": True,
                        "api_cleanup_enabled": True,
                        "no_cleanup_after_hold": True,
                        "deletion_authorized": False,
                        "reload_proven": True,
                    }
                ),
                encoding="utf-8",
            )
            signal = health.build_signal(root)

        self.assertEqual(signal["hold_started_at"], "2026-08-07T02:40:22Z")
        self.assertNotIn("cleanup_hold", signal["evidence_errors"])

    def test_minimal_unvalidated_json_cannot_report_closeout_complete(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            (root / "US-039-prod-cleanup-hold-20260807.json").write_text(
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
        self.assertFalse(signal["tail_export_complete"])
        self.assertFalse(signal["cleanup_release_complete"])
        self.assertIsNone(signal["cleanup_release_verified_at"])
        self.assertEqual(signal["tail_ledgers"], [])
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


class DataLayerArchiveHealthReleaseTest(unittest.TestCase):
    def test_release_receipt_must_bind_to_latest_hold(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            hold_path = root / "US-039-prod-cleanup-hold-20260807.json"
            hold_path.write_text(
                (health.ATTACHMENTS / "US-039-prod-cleanup-hold-20260807.json").read_text(
                    encoding="utf-8"
                ),
                encoding="utf-8",
            )
            release = json.loads(
                (health.ATTACHMENTS / "US-039-prod-cleanup-hold-release-20260808.json").read_text(
                    encoding="utf-8"
                )
            )
            release["hold_receipt_sha256"] = "deadbeef"
            (root / "US-039-prod-cleanup-hold-release-20260808.json").write_text(
                json.dumps(release),
                encoding="utf-8",
            )
            for name in health.ATTACHMENTS.glob("US-040-*"):
                (root / name.name).write_text(name.read_text(encoding="utf-8"), encoding="utf-8")

            signal = health.build_signal(root)

        self.assertIn("cleanup_release", signal["evidence_errors"])
        self.assertFalse(signal["cleanup_release_complete"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
