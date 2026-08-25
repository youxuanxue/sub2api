#!/usr/bin/env python3
"""Behavior contract for expensive-job routing in backend-ci.yml."""

from __future__ import annotations

from pathlib import Path
import subprocess
import unittest

import yaml


WORKFLOW = Path(__file__).resolve().parents[1] / ".github" / "workflows" / "backend-ci.yml"
BACKEND_MAKEFILE = Path(__file__).resolve().parents[1] / "backend" / "Makefile"


class BackendCIRoutingTest(unittest.TestCase):
    def setUp(self) -> None:
        self.jobs = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))["jobs"]

    def test_changes_job_exports_all_surface_decisions(self) -> None:
        outputs = self.jobs["changes"]["outputs"]
        self.assertEqual(
            set(outputs),
            {
                "backend",
                "frontend",
                "deploy",
                "ops",
                "contracts",
                "service_unit_cold",
                "all",
            },
        )

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
            "scripts.ci.test_unit_test_runner",
            "scripts.test_backend_ci_routing",
        }
        for module in expected_modules:
            with self.subTest(module=module):
                self.assertIn(module, command)

    def test_go_dependent_integration_contract_runs_after_pinned_setup(self) -> None:
        steps = self.jobs["preflight"]["steps"]
        orchestration = next(
            step for step in steps if step.get("name") == "CI orchestration contract tests"
        )
        self.assertNotIn("scripts.ci.test_integration_packages", orchestration.get("run", ""))

        setup_index = next(
            index for index, step in enumerate(steps) if step.get("uses") == "actions/setup-go@v6"
        )
        contract_index = next(
            index
            for index, step in enumerate(steps)
            if step.get("name") == "Integration package discovery contract tests"
        )
        self.assertGreater(contract_index, setup_index)
        self.assertIn(
            "scripts.ci.test_integration_packages",
            steps[contract_index].get("run", ""),
        )

    def test_integration_target_uses_discovered_owner_packages(self) -> None:
        makefile = BACKEND_MAKEFILE.read_text(encoding="utf-8")
        target = makefile.split("test-integration:\n", 1)[1].split("\n\n", 1)[0]
        self.assertIn("integration-packages.py", target)
        self.assertNotIn("go test -tags=integration ./...", target)

    def test_unit_job_passes_the_service_cold_path_decision(self) -> None:
        unit_step = next(
            step
            for step in self.jobs["test-unit"]["steps"]
            if step.get("name") == "Unit tests"
        )

        self.assertIn(
            "needs.changes.outputs.service_unit_cold",
            unit_step["env"]["UNIT_TEST_SERVICE_SHARD"],
        )

    def test_unit_main_writer_uses_native_path_to_seed_result_cache(self) -> None:
        unit_step = next(
            step
            for step in self.jobs["test-unit"]["steps"]
            if step.get("name") == "Unit tests"
        )

        self.assertIn(
            "github.event_name != 'push'",
            unit_step["env"]["UNIT_TEST_SERVICE_SHARD"],
        )

    def test_unit_target_uses_go_cache_by_default(self) -> None:
        result = subprocess.run(
            ["make", "-n", "-C", str(BACKEND_MAKEFILE.parent), "test-unit"],
            check=False,
            capture_output=True,
            text=True,
        )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("go test -tags=unit ./...", result.stdout)
        self.assertNotIn("unit_test_runner.py", result.stdout)

    def test_unit_target_uses_compile_once_shards_on_cold_path(self) -> None:
        result = subprocess.run(
            [
                "make",
                "-n",
                "-C",
                str(BACKEND_MAKEFILE.parent),
                "UNIT_TEST_SERVICE_SHARD=1",
                "test-unit",
            ],
            check=False,
            capture_output=True,
            text=True,
        )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("unit_test_runner.py", result.stdout)
        self.assertNotIn("go test -tags=unit ./...", result.stdout)


if __name__ == "__main__":
    unittest.main()
