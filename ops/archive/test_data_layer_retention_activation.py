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
    def test_remote_plan_is_read_only_and_ops_owned(self) -> None:
        script = activation._remote_plan_script()
        self.assertIn("30 days", script)
        self.assertIn("ops_error_logs", script)
        self.assertIn("ops_system_logs", script)
        self.assertIn("active_image", script)
        self.assertIn("telemetry_archive_shadow", script)
        self.assertIn("ops_partition_maintenance", script)
        self.assertNotIn("forward_archive_window", script)
        self.assertNotIn("qa_archive_shards", script)
        self.assertIn("WITH bounds AS MATERIALIZED", script)
        self.assertIn("usage_logs", script)
        self.assertNotIn("tokenkey-qa-maintenance", script)
        self.assertNotIn("tokenkey-qa-stale-cleanup", script)
        self.assertNotIn("qa_records", script)
        self.assertNotIn("DELETE FROM", script)
        self.assertNotIn("UPDATE ", script)
        self.assertNotIn("cleanup_eligible", script)
        self.assertNotIn("commit_key", script)

    def test_ready_accepts_legacy_window_without_tomorrow_child_partition(self) -> None:
        payload = {
            "active_image": "ghcr.io/youxuanxue/sub2api:1.8.140",
            "ops": {
                "ops_retention_days": 30,
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

    def test_ops_readiness_ignores_qa_lifecycle_payloads(self) -> None:
        payload = {
            "active_image": "ghcr.io/youxuanxue/sub2api:1.8.140",
            "qa": {"malformed": "must be ignored by generic retention"},
            "ops": {
                "ops_retention_days": 30,
                "usage_logs_partitioned": True,
                "usage_legacy_attached": True,
                "usage_future_partition_exists": True,
                "usage_partition_maintenance_clean": True,
                "telemetry_clean": True,
                "telemetry_stats": {"dropped": 0, "failed": 0},
            },
        }
        ready, reasons = activation._ready(payload)
        self.assertIs(ready, True)
        self.assertEqual(reasons, [])


if __name__ == "__main__":
    unittest.main(verbosity=2)
