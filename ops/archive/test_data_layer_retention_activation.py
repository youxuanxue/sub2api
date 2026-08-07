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
