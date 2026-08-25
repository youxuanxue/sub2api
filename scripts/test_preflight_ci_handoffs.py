#!/usr/bin/env python3
"""Workflow behavior tests for changed-surface routing of slow ops contracts."""

from __future__ import annotations

from pathlib import Path
import unittest

import yaml


REPO_ROOT = Path(__file__).resolve().parents[1]
BACKEND_CI = REPO_ROOT / ".github" / "workflows" / "backend-ci.yml"


def load_workflow(path: Path) -> dict:
    return yaml.safe_load(path.read_text(encoding="utf-8"))


class PreflightCIHandoffTest(unittest.TestCase):
    def test_required_preflight_runs_slow_ops_only_for_matching_surfaces(self) -> None:
        workflow = load_workflow(BACKEND_CI)
        run_preflight = next(
            step
            for step in workflow["jobs"]["preflight"]["steps"]
            if step.get("name") == "Run preflight"
        )
        skip_expression = run_preflight["env"]["PREFLIGHT_SKIP_SLOW_OPS_CONTRACTS"]
        self.assertIn("needs.changes.outputs.ops", skip_expression)
        self.assertIn("needs.changes.outputs.all", skip_expression)


if __name__ == "__main__":
    unittest.main()
