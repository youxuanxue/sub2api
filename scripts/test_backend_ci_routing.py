#!/usr/bin/env python3
"""Behavior contract for expensive-job routing in backend-ci.yml."""

from __future__ import annotations

from pathlib import Path
import unittest

import yaml


WORKFLOW = Path(__file__).resolve().parents[1] / ".github" / "workflows" / "backend-ci.yml"


class BackendCIRoutingTest(unittest.TestCase):
    def setUp(self) -> None:
        self.jobs = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))["jobs"]

    def test_changes_job_exports_all_surface_decisions(self) -> None:
        outputs = self.jobs["changes"]["outputs"]
        self.assertEqual(set(outputs), {"backend", "frontend", "deploy", "ops", "contracts", "all"})

    def test_expensive_jobs_depend_on_the_matching_surface(self) -> None:
        expected = {
            "shell": "deploy",
            "test-unit": "backend",
            "test-integration": "backend",
            "golangci-lint": "backend",
            "backend-security": "backend",
            "frontend": "frontend",
            "frontend-security": "frontend",
        }
        for job_name, surface in expected.items():
            with self.subTest(job=job_name):
                job = self.jobs[job_name]
                needs = job.get("needs")
                self.assertIn("changes", [needs] if isinstance(needs, str) else needs)
                condition = job.get("if", "")
                self.assertIn(f"needs.changes.outputs.{surface} == 'true'", condition)
                self.assertIn("needs.changes.outputs.all == 'true'", condition)

    def test_required_preflight_owns_path_conditioned_contract_gates(self) -> None:
        preflight = self.jobs["preflight"]
        self.assertEqual(preflight.get("needs"), "changes")
        self.assertIn("always()", preflight.get("if", ""))
        run_preflight = next(
            step for step in preflight["steps"] if step.get("name") == "Run preflight"
        )
        env = run_preflight["env"]
        self.assertIn("needs.changes.outputs.ops", env["PREFLIGHT_SKIP_SLOW_OPS_CONTRACTS"])
        self.assertIn("needs.changes.outputs.contracts", env["PREFLIGHT_SKIP_AGENT_CONTRACT"])

    def test_preflight_job_runs_ci_orchestration_contracts(self) -> None:
        matching_steps = [
            step
            for step in self.jobs["preflight"]["steps"]
            if step.get("name") == "CI orchestration contract tests"
        ]
        self.assertEqual(len(matching_steps), 1)
        step = matching_steps[0]
        command = step.get("run", "")
        expected_modules = {
            "scripts.test_preflight_ci_lint_skip",
            "scripts.checks.test_release_cache_key_parity",
            "scripts.checks.test_go_rolling_cache_policy",
            "scripts.test_preflight_ci_handoffs",
            "scripts.test_ci_gate_handoffs",
            "scripts.ci.test_changed_surfaces",
            "scripts.test_backend_ci_routing",
        }
        for module in expected_modules:
            with self.subTest(module=module):
                self.assertIn(module, command)


if __name__ == "__main__":
    unittest.main()
