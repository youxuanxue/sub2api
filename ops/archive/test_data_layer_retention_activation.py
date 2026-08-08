#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import pathlib
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location(
    "data_layer_retention_activation", HERE / "data_layer_retention_activation.py"
)
assert SPEC is not None and SPEC.loader is not None
activation = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(activation)


class RetentionActivationTest(unittest.TestCase):
    def test_remote_plan_is_read_only_and_age_owned(self) -> None:
        script = activation._remote_plan_script()
        self.assertIn("tokenkey-qa-stale-cleanup.sh --plan", script)
        self.assertIn("30 days", script)
        self.assertIn("ops_error_logs", script)
        self.assertIn("ops_system_logs", script)
        self.assertIn("active_image", script)
        self.assertIn("telemetry_archive_shadow", script)
        self.assertIn("ops_partition_maintenance", script)
        self.assertIn("forward_archive_window", script)
        self.assertIn("restore_verified_at", script)
        self.assertIn("WITH bounds AS MATERIALIZED", script)
        self.assertIn("usage_logs", script)
        self.assertIn("tokenkey-qa-maintenance.timer", script)
        self.assertIn("tokenkey-qa-stale-cleanup.timer", script)
        self.assertNotIn("DELETE FROM", script)
        self.assertNotIn("UPDATE ", script)
        self.assertNotIn("cleanup_eligible", script)
        self.assertNotIn("commit_key", script)

    def test_ready_accepts_legacy_window_without_tomorrow_child_partition(self) -> None:
        payload = {
            "active_image": "ghcr.io/youxuanxue/sub2api:1.8.140",
            "timers": {
                "qa_maintenance": {"enabled": "enabled", "active": "active"},
                "qa_stale_cleanup": {"enabled": "disabled", "active": "inactive"},
            },
            "qa": {
                "active_image": "ghcr.io/youxuanxue/sub2api:1.8.140",
                "mode": "prod_qa_age_retention_plan",
            },
            "ops": {
                "ops_retention_days": 30,
                "historical_windows": {
                    "2026-08-04T04:00:00Z": {
                        "state": "failed",
                        "verification_error_code": "missing_evidence",
                    },
                    "2026-08-07T01:00:00Z": {
                        "state": "failed",
                        "verification_error_code": "commit_mismatch",
                    },
                },
                "forward_archive_window": {"state": "committed"},
                "usage_logs_partitioned": True,
                "usage_legacy_attached": True,
                "usage_future_partition_exists": False,
                "usage_partition_maintenance_clean": True,
                "telemetry_clean": True,
                "telemetry_stats": {"dropped": 0, "failed": 0},
            },
        }
        ready, reasons = activation._ready(payload)
        self.assertTrue(ready, reasons)

    def test_plan_rejects_invalid_hold_before_remote_resolution(self) -> None:
        called = False

        def unexpected_verify(_path):
            nonlocal called
            called = True
            return {}

        with mock.patch.object(activation.hold, "verify", side_effect=unexpected_verify):
            with self.assertRaisesRegex(activation.ActivationError, "hold receipt"):
                activation.plan("/does/not/exist")
        self.assertFalse(called)


if __name__ == "__main__":
    unittest.main(verbosity=2)
