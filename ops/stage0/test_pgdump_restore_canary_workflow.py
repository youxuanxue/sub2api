#!/usr/bin/env python3
"""Contract tests for scheduled Fleet restore-canary orchestration."""

from __future__ import annotations

import pathlib
import unittest

import yaml


ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / ".github" / "workflows" / "ops-pgdump-restore-canary.yml"


class PgdumpRestoreCanaryWorkflowTest(unittest.TestCase):
    def test_schedule_dispatch_and_dynamic_fleet_matrix_are_fail_closed(self) -> None:
        workflow = yaml.load(WORKFLOW.read_text(encoding="utf-8"), Loader=yaml.BaseLoader)
        triggers = workflow["on"]
        self.assertEqual(len(triggers["schedule"]), 1)
        self.assertIn("target_selector", triggers["workflow_dispatch"]["inputs"])

        discover = workflow["jobs"]["discover-targets"]
        self.assertIn("matrix", discover["outputs"])
        matrix_step = next(
            step for step in discover["steps"] if step.get("id") == "matrix"
        )
        self.assertIn("--prod-ops-matrix", matrix_step["run"])
        self.assertIn("--target-selector", matrix_step["run"])

        restore = workflow["jobs"]["restore-canary"]
        self.assertEqual(restore["strategy"]["fail-fast"], "false")
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

        names = [step.get("name", "") for step in restore["steps"]]
        restore_state = names.index("Restore per-target alert state")
        decide = names.index("Decide restore-canary alert")
        deliver = names.index("Deliver restore-canary alert")
        save_state = names.index("Save per-target alert state")
        propagate = names.index("Propagate restore-canary failure")
        self.assertLess(restore_state, names.index("Run isolated pg_dump restore canary"))
        self.assertLess(decide, deliver)
        self.assertLess(deliver, save_state)
        self.assertLess(save_state, propagate)

        restore_state_step = restore["steps"][restore_state]
        self.assertIn("github.run_attempt", restore_state_step["with"]["key"])

        decision_step = restore["steps"][decide]
        self.assertEqual(decision_step["if"], "always()")
        self.assertIn("pgdump_restore_canary_alert.py", decision_step["run"])
        delivery_step = restore["steps"][deliver]
        self.assertEqual(delivery_step["if"], "always()")
        self.assertIn("edge_health_delivery.py", delivery_step["run"])
        self.assertIn("TK_FEISHU_WEBHOOK_URL", str(delivery_step["env"]))
        self.assertNotIn("TK_FEISHU_WEBHOOK_URL", command)

        save_step = restore["steps"][save_state]
        self.assertIn("actions/cache/save", save_step["uses"])
        self.assertIn("TARGET_ID", str(save_step))
        self.assertIn("github.run_attempt", save_step["with"]["key"])
        self.assertIn("steps.canary.outcome", str(restore["steps"][propagate]))


if __name__ == "__main__":
    unittest.main(verbosity=2)
