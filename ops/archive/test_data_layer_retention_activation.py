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
        self.assertNotIn("forward_archive_window", script)
        self.assertNotIn("qa_archive_shards", script)
        self.assertIn("WITH bounds AS MATERIALIZED", script)
        self.assertIn("usage_logs", script)
        self.assertIn("tokenkey-qa-maintenance.timer", script)
        self.assertIn("tokenkey-qa-stale-cleanup.timer", script)
        self.assertNotIn("DELETE FROM", script)
        self.assertNotIn("UPDATE ", script)
        self.assertNotIn("cleanup_eligible", script)
        self.assertNotIn("commit_key", script)

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

    def test_qa_cleanup_readiness_is_independent_of_archive_and_maintenance(self) -> None:
        payload = {
            "active_image": "ghcr.io/youxuanxue/sub2api:1.8.140",
            "timers": {
                "qa_maintenance": {"enabled": "disabled", "active": "inactive"},
                "qa_stale_cleanup": {"enabled": "disabled", "active": "inactive"},
            },
            "qa": {"active_image": "ghcr.io/youxuanxue/sub2api:1.8.140"},
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
