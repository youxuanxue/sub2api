#!/usr/bin/env python3
"""Behavior gates for the Stage0 RDS cutover approval and rollback boundary."""

from __future__ import annotations

import os
import pathlib
import subprocess
import unittest


REPO = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO / "ops/stage0/cutover_data_layer_via_ssm.sh"


def run_cutover(action: str, *, environment: str = "prod") -> subprocess.CompletedProcess:
    env = os.environ.copy()
    env.update(
        {
            "TK_ENVIRONMENT": environment,
            "TK_DATA_PG_HOST": "candidate.example.rds.amazonaws.com",
        }
    )
    return subprocess.run(
        ["bash", str(SCRIPT), action, "i-test-only"],
        cwd=REPO,
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )


class CutoverDataLayerSafetyTest(unittest.TestCase):
    def test_prod_apply_is_blocked_while_design_is_pending(self) -> None:
        proc = run_cutover("apply")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("production blocked", proc.stderr)
        self.assertIn("status=pending", proc.stderr)
        self.assertNotIn("reading RDS master password", proc.stdout)

    def test_stale_local_rollback_action_is_rejected(self) -> None:
        proc = run_cutover("rollback")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("rollback to stale local PG is intentionally unsupported", proc.stderr)

    def test_nonprod_rehearsal_reaches_runtime_preflight(self) -> None:
        env = os.environ.copy()
        env["TK_ENVIRONMENT"] = "rehearsal"
        env.pop("TK_DATA_PG_HOST", None)
        proc = subprocess.run(
            ["bash", str(SCRIPT), "apply", "i-test-only"],
            cwd=REPO,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("TK_DATA_PG_HOST", proc.stderr)
        self.assertNotIn("production blocked", proc.stderr)


if __name__ == "__main__":
    unittest.main()
