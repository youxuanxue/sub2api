#!/usr/bin/env python3
"""Workflow behavior tests for ancestry and agent-contract handoffs."""

from __future__ import annotations

from pathlib import Path
import unittest

import yaml


REPO_ROOT = Path(__file__).resolve().parents[1]
BACKEND_CI = REPO_ROOT / ".github" / "workflows" / "backend-ci.yml"
ANCESTRY = REPO_ROOT / ".github" / "workflows" / "main-ancestry-guard.yml"


def load_workflow(path: Path) -> dict:
    return yaml.safe_load(path.read_text(encoding="utf-8"))


def workflow_on(workflow: dict) -> dict:
    return workflow.get("on") or workflow.get(True) or {}


class CIGateHandoffTest(unittest.TestCase):
    def test_required_preflight_routes_expensive_gate_implementations(self) -> None:
        workflow = load_workflow(BACKEND_CI)
        run_preflight = next(
            step
            for step in workflow["jobs"]["preflight"]["steps"]
            if step.get("name") == "Run preflight"
        )
        self.assertEqual(run_preflight["env"]["PREFLIGHT_SKIP_MAIN_ANCESTRY"], "1")
        skip_expression = run_preflight["env"]["PREFLIGHT_SKIP_AGENT_CONTRACT"]
        self.assertIn("needs.changes.outputs.contracts", skip_expression)
        self.assertIn("needs.changes.outputs.all", skip_expression)

    def test_main_push_ancestry_detection_uses_a_lightweight_full_history_job(self) -> None:
        workflow = load_workflow(ANCESTRY)
        self.assertEqual(workflow_on(workflow)["push"]["branches"], ["main"])
        job = workflow["jobs"]["main-ancestry-anchor"]
        checkout = next(step for step in job["steps"] if step.get("uses") == "actions/checkout@v6")
        self.assertEqual(checkout["with"]["fetch-depth"], 0)
        self.assertNotIn("submodules", checkout["with"])
        commands = "\n".join(step.get("run", "") for step in job["steps"])
        self.assertIn("python3 scripts/checks/main-ancestry-anchor.py", commands)


if __name__ == "__main__":
    unittest.main()
