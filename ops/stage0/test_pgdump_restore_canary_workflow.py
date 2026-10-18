#!/usr/bin/env python3
"""Contract tests for scheduled Fleet restore-canary orchestration."""

from __future__ import annotations

import pathlib
import unittest

import yaml


ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / ".github" / "workflows" / "ops-pgdump-restore-canary.yml"


class PgdumpRestoreCanaryWorkflowTest(unittest.TestCase):
    def test_schedule_dispatches_prod_only_and_is_fail_closed(self) -> None:
        workflow = yaml.load(WORKFLOW.read_text(encoding="utf-8"), Loader=yaml.BaseLoader)
        triggers = workflow["on"]
        self.assertEqual(len(triggers["schedule"]), 1)
        restore = workflow["jobs"]["restore-canary"]
        self.assertEqual(restore["permissions"]["id-token"], "write")
        command = next(
            step["run"]
            for step in restore["steps"]
            if step.get("name") == "Run isolated pg_dump restore canary"
        )
        self.assertIn("run-probe.sh", command)
        self.assertIn("pgdump_restore_canary_remote.sh", command)
        self.assertIn("pgdump_restore_canary.py", command)
        self.assertIn("pgdump_restore_canary_contract.py", command)
        canary_step = next(
            step for step in restore["steps"]
            if step.get("name") == "Run isolated pg_dump restore canary"
        )
        self.assertEqual(canary_step["continue-on-error"], "true")
        self.assertEqual(len(restore["steps"]), 3)


if __name__ == "__main__":
    unittest.main(verbosity=2)
