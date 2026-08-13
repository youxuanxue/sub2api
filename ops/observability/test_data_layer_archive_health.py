#!/usr/bin/env python3
"""Behavior tests for checked-in archive health evidence validation."""

from __future__ import annotations

import datetime as dt
import json
import pathlib
import sys
import tempfile
import unittest


_DIR = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(_DIR))

import data_layer_archive_health as health  # noqa: E402


def _checked_in_evidence_dir() -> pathlib.Path:
    return health.pipeline_status.load_evidence_layout().evidence_dir


class DataLayerArchiveHealthTest(unittest.TestCase):
    def test_checked_in_ledgers_pass_their_owner_validator(self) -> None:
        signal = health.build_signal(now=dt.datetime(2026, 7, 10, tzinfo=dt.timezone.utc))
        self.assertEqual(
            {ledger["table"] for ledger in signal["ledgers"]},
            {"ops_error_logs", "ops_system_logs"},
        )
        self.assertIsInstance(signal["hold_started_at"], str)
        self.assertEqual(signal["archive_mode"], "frozen")
        self.assertTrue(signal["closeout_complete"])
        self.assertTrue(signal["tail_export_complete"])
        self.assertFalse(signal["tail_export_stale"])
        self.assertTrue(signal["archive_coverage_current"])
        self.assertTrue(signal["cleanup_release_complete"])
        self.assertIsInstance(signal["cleanup_release_verified_at"], str)
        self.assertEqual(
            {ledger["table"] for ledger in signal["tail_ledgers"]},
            {"ops_error_logs", "ops_system_logs"},
        )
        self.assertEqual(signal["evidence_errors"], [])

    def test_tail_export_stale_uses_ops_retention_window(self) -> None:
        now = dt.datetime(2026, 8, 5, tzinfo=dt.timezone.utc)
        cutoff = now - dt.timedelta(days=29)
        self.assertFalse(
            health._tail_export_stale(
                [{"final_cutoff_exclusive": cutoff.strftime("%Y-%m-%dT%H:%M:%SZ")}],
                now,
                30,
            )
        )
        older = now - dt.timedelta(days=32)
        self.assertTrue(
            health._tail_export_stale(
                [{"final_cutoff_exclusive": older.strftime("%Y-%m-%dT%H:%M:%SZ")}],
                now,
                30,
            )
        )

    def test_checked_in_frozen_tail_has_no_rolling_freshness_obligation(self) -> None:
        signal = health.build_signal(now=dt.datetime(2027, 8, 10, tzinfo=dt.timezone.utc))
        self.assertEqual(signal["archive_mode"], "frozen")
        self.assertTrue(signal["closeout_complete"])
        self.assertTrue(signal["tail_export_complete"])
        self.assertFalse(signal["tail_export_stale"])
        self.assertTrue(signal["archive_coverage_current"])

    def test_latest_valid_hold_receipt_is_selected(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            for name, started_at in (
                ("data-layer-cleanup-hold-20260721.json", "2026-07-21T00:00:00Z"),
                ("data-layer-cleanup-hold-apply.json", "2026-08-07T02:40:22Z"),
            ):
                (root / name).write_text(
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
            (root / "data-layer-cleanup-hold-invalid.json").write_text(
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

    def test_frozen_tail_rejects_promote_manifest_hash_mismatch(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            for path in _checked_in_evidence_dir().iterdir():
                if path.is_file():
                    (root / path.name).write_bytes(path.read_bytes())
            promote_path = root / "data-layer-ops-error-logs-tail-promote-ledger.json"
            ledger = json.loads(promote_path.read_text(encoding="utf-8"))
            ledger["promoted_batches"][0]["objects"][-1]["sha256"] = "0" * 64
            promote_path.write_text(json.dumps(ledger), encoding="utf-8")

            signal = health.build_signal(root)

        self.assertFalse(signal["tail_export_complete"])
        self.assertIn("ops_error_logs:tail_export_ledger", signal["evidence_errors"])

    def test_minimal_unvalidated_json_cannot_report_closeout_complete(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            (root / "data-layer-cleanup-hold-apply.json").write_text(
                json.dumps({"hold_started_at": "2026-07-21T00:00:00Z"}),
                encoding="utf-8",
            )
            for table, slug in (
                ("ops_error_logs", "ops-error-logs"),
                ("ops_system_logs", "ops-system-logs"),
            ):
                (root / f"data-layer-{slug}-export-ledger.json").write_text(
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
                (root / f"data-layer-{slug}-archive-closeout.json").write_text(
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
            hold_path = root / "data-layer-cleanup-hold-apply.json"
            hold_path.write_text(
                (_checked_in_evidence_dir() / "data-layer-cleanup-hold-apply.json").read_text(
                    encoding="utf-8"
                ),
                encoding="utf-8",
            )
            release = json.loads(
                (_checked_in_evidence_dir() / "data-layer-cleanup-hold-release.json").read_text(
                    encoding="utf-8"
                )
            )
            release["hold_receipt_sha256"] = "deadbeef"
            (root / "data-layer-cleanup-hold-release.json").write_text(
                json.dumps(release),
                encoding="utf-8",
            )
            for name in _checked_in_evidence_dir().glob("data-layer-ops-*"):
                (root / name.name).write_text(name.read_text(encoding="utf-8"), encoding="utf-8")

            signal = health.build_signal(root)

        self.assertIn("cleanup_release", signal["evidence_errors"])
        self.assertFalse(signal["cleanup_release_complete"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
