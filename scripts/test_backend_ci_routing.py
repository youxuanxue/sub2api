#!/usr/bin/env python3
"""Behavior contract for expensive-job routing in backend-ci.yml."""

from __future__ import annotations

import json
from pathlib import Path
import subprocess
import unittest

import yaml


WORKFLOW = Path(__file__).resolve().parents[1] / ".github" / "workflows" / "backend-ci.yml"
BACKEND_MAKEFILE = Path(__file__).resolve().parents[1] / "backend" / "Makefile"
ROOT_MAKEFILE = Path(__file__).resolve().parents[1] / "Makefile"
FRONTEND_PACKAGE = Path(__file__).resolve().parents[1] / "frontend" / "package.json"


class BackendCIRoutingTest(unittest.TestCase):
    def setUp(self) -> None:
        self.workflow = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
        self.jobs = self.workflow["jobs"]
        self.frontend_scripts = json.loads(
            FRONTEND_PACKAGE.read_text(encoding="utf-8")
        )["scripts"]

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
                "preflight_go",
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

    def test_frontend_job_parallelizes_checks_on_one_runner(self) -> None:
        frontend_step = next(
            step
            for step in self.jobs["frontend"]["steps"]
            if "test-frontend" in step.get("run", "")
        )

        self.assertEqual(
            frontend_step["name"],
            "Frontend lint, typecheck, and critical vitest",
        )
        self.assertEqual(
            frontend_step["run"],
            "make -j3 --output-sync=target test-frontend",
        )

    def test_frontend_target_preserves_all_required_checks(self) -> None:
        result = subprocess.run(
            ["make", "-npRr", "-f", str(ROOT_MAKEFILE), "test-frontend"],
            check=False,
            capture_output=True,
            text=True,
        )

        self.assertEqual(result.returncode, 0, result.stderr)
        target_line = next(
            line for line in result.stdout.splitlines() if line.startswith("test-frontend:")
        )
        self.assertEqual(
            set(target_line.split(":", 1)[1].split()),
            {
                "test-frontend-lint",
                "test-frontend-typecheck",
                "test-frontend-critical",
            },
        )

    def test_frontend_job_reuses_content_validated_check_caches(self) -> None:
        steps = self.jobs["frontend"]["steps"]
        check_index = next(
            index
            for index, step in enumerate(steps)
            if "test-frontend" in step.get("run", "")
        )
        cache_indexes = [
            index
            for index, step in enumerate(steps)
            if step.get("name") == "Restore frontend check caches"
        ]

        self.assertEqual(len(cache_indexes), 1)
        cache_index = cache_indexes[0]
        self.assertLess(cache_index, check_index)
        cache_step = steps[cache_index]
        self.assertEqual(cache_step["uses"], "actions/cache@v6")
        self.assertEqual(
            set(cache_step["with"]["path"].splitlines()),
            {
                "frontend/.cache/eslint",
                "frontend/.cache/vue-tsc",
            },
        )
        self.assertIn("frontend-checks-node24-v1", cache_step["with"]["key"])
        self.assertIn("github.run_id", cache_step["with"]["key"])

        lint = self.frontend_scripts["lint:check"]
        self.assertIn("--cache-strategy content", lint)
        self.assertIn("--cache-location .cache/eslint/.eslintcache", lint)

        typecheck = self.frontend_scripts["typecheck"]
        self.assertIn("--incremental", typecheck)
        self.assertIn(
            "--tsBuildInfoFile .cache/vue-tsc/tsconfig.tsbuildinfo",
            typecheck,
        )

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

    def test_preflight_go_bootstrap_uses_the_preflight_go_surface(self) -> None:
        steps = self.jobs["preflight"]["steps"]
        go_steps = [
            next(
                step
                for step in steps
                if step.get("uses") == "./.github/actions/cache-and-checkout-new-api"
            ),
            next(step for step in steps if step.get("uses") == "actions/setup-go@v6"),
            next(step for step in steps if step.get("name") == "Unit runner contract tests"),
            next(
                step
                for step in steps
                if step.get("uses") == "./.github/actions/go-rolling-cache"
            ),
        ]

        for step in go_steps:
            with self.subTest(step=step.get("name", step.get("uses"))):
                self.assertEqual(
                    step.get("if"),
                    "needs.changes.outputs.preflight_go == 'true'",
                )

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
            "scripts.checks.test_model_surface_bundle_check",
        }
        for module in expected_modules:
            with self.subTest(module=module):
                self.assertIn(module, command)

    def test_go_dependent_unit_runner_contract_runs_after_pinned_setup(self) -> None:
        steps = self.jobs["preflight"]["steps"]
        orchestration = next(
            step for step in steps if step.get("name") == "CI orchestration contract tests"
        )
        self.assertNotIn("scripts.ci.test_unit_test_runner", orchestration.get("run", ""))

        setup_index = next(
            index for index, step in enumerate(steps) if step.get("uses") == "actions/setup-go@v6"
        )
        contract_steps = [
            (index, step)
            for index, step in enumerate(steps)
            if step.get("name") == "Unit runner contract tests"
        ]
        self.assertEqual(len(contract_steps), 1)
        contract_index, contract_step = contract_steps[0]
        self.assertGreater(contract_index, setup_index)
        self.assertIn(
            "scripts.ci.test_unit_test_runner",
            contract_step.get("run", ""),
        )

    def test_preflight_background_output_contract_runs_in_orchestration_gate(self) -> None:
        orchestration = next(
            step
            for step in self.jobs["preflight"]["steps"]
            if step.get("name") == "CI orchestration contract tests"
        )
        self.assertIn(
            "scripts.test_preflight_background_output",
            orchestration.get("run", ""),
        )

    def test_go_dependent_integration_contract_runs_off_the_preflight_path(self) -> None:
        preflight_steps = self.jobs["preflight"]["steps"]
        self.assertFalse(
            [
                step
                for step in preflight_steps
                if "scripts.ci.test_integration_packages" in step.get("run", "")
                or "scripts.ci.test_integration_test_runner" in step.get("run", "")
            ]
        )

        steps = self.jobs["test-integration"]["steps"]
        cache_index = next(
            index
            for index, step in enumerate(steps)
            if step.get("uses") == "./.github/actions/go-rolling-cache"
        )
        contract_index = next(
            index
            for index, step in enumerate(steps)
            if step.get("name") == "Integration runner contract tests"
        )
        integration_index = next(
            index
            for index, step in enumerate(steps)
            if step.get("name") == "Integration tests"
        )
        self.assertGreater(contract_index, cache_index)
        self.assertLess(contract_index, integration_index)
        self.assertIn(
            "scripts.ci.test_integration_packages",
            steps[contract_index].get("run", ""),
        )
        self.assertIn(
            "scripts.ci.test_integration_test_runner",
            steps[contract_index].get("run", ""),
        )

    def test_preflight_restores_go_cache_before_go_backed_contracts(self) -> None:
        steps = self.jobs["preflight"]["steps"]
        cache_index = next(
            index
            for index, step in enumerate(steps)
            if step.get("uses") == "./.github/actions/go-rolling-cache"
        )
        contract_indexes = [
            index
            for index, step in enumerate(steps)
            if step.get("name") == "Unit runner contract tests"
        ]

        self.assertEqual(len(contract_indexes), 1)
        self.assertTrue(
            all(cache_index < contract_index for contract_index in contract_indexes),
            "Go-backed preflight contracts must consume the restored build cache",
        )

    def test_integration_target_uses_compile_once_repository_shards(self) -> None:
        result = subprocess.run(
            ["make", "-n", "-C", str(BACKEND_MAKEFILE.parent), "test-integration"],
            check=False,
            capture_output=True,
            text=True,
        )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("integration_test_runner.py", result.stdout)
        self.assertNotIn("go test -tags=integration", result.stdout)

    def test_integration_job_runs_integration_runner_contract_tests(self) -> None:
        step = next(
            step
            for step in self.jobs["test-integration"]["steps"]
            if step.get("name") == "Integration runner contract tests"
        )

        self.assertIn("scripts.ci.test_integration_test_runner", step["run"])

    def test_unit_job_always_uses_compile_once_service_shards(self) -> None:
        unit_cache = next(
            step
            for step in self.jobs["test-unit"]["steps"]
            if step.get("uses") == "./.github/actions/go-rolling-cache"
        )
        unit_step = next(
            step
            for step in self.jobs["test-unit"]["steps"]
            if step.get("name") == "Unit tests"
        )

        self.assertEqual(unit_cache["id"], "go_cache")
        self.assertEqual(unit_step["env"]["UNIT_TEST_SERVICE_SHARD"], "1")
        self.assertEqual(
            unit_step["env"]["UNIT_TEST_BUILD_CACHE_HIT"],
            "${{ steps.go_cache.outputs.build_cache_hit }}",
        )

    def test_unit_cache_benchmark_is_manual_isolated_and_opt_in(self) -> None:
        triggers = self.workflow.get("on", self.workflow.get(True))
        suite = triggers["workflow_dispatch"]["inputs"]["suite"]
        self.assertIn("unit-cache-benchmark", suite["options"])

        unit_job_condition = self.jobs["test-unit"]["if"]
        self.assertIn("inputs.suite == 'unit-cache-benchmark'", unit_job_condition)
        self.assertIn(
            "inputs.suite != 'unit-cache-benchmark'",
            self.jobs["shell"]["if"],
        )
        self.assertIn(
            "inputs.suite != 'unit-cache-benchmark'",
            self.jobs["ssot-delta-gate"]["if"],
        )
        for job_name in ("preflight", "test-integration", "golangci-lint"):
            with self.subTest(job=job_name):
                self.assertNotIn(
                    "unit-cache-benchmark",
                    self.jobs[job_name]["if"],
                )

        unit_cache = next(
            step
            for step in self.jobs["test-unit"]["steps"]
            if step.get("uses") == "./.github/actions/go-rolling-cache"
        )
        self.assertEqual(unit_cache["with"]["prefix"], "unit-nodwarf-v3")
        self.assertEqual(
            unit_cache["with"]["benchmark_prefix"],
            "unit-nodwarf-v5-other-first-bench",
        )
        self.assertEqual(
            unit_cache["with"]["benchmark_build_cache_write"],
            "${{ github.event_name == 'workflow_dispatch' && "
            "inputs.suite == 'unit-cache-benchmark' && 'true' || 'false' }}",
        )

    def test_unit_job_omits_dwarf_from_test_build_objects(self) -> None:
        self.assertEqual(
            self.jobs["test-unit"]["env"]["GOFLAGS"],
            "-gcflags=all=-dwarf=false",
        )

    def test_heavy_go_jobs_omit_dwarf_from_cached_build_objects(self) -> None:
        declarations = []
        for job_name, job in self.jobs.items():
            if "GOFLAGS" in job.get("env", {}):
                declarations.append((job_name, "job", job["env"]["GOFLAGS"]))
            for step in job.get("steps", []):
                if "GOFLAGS" in step.get("env", {}):
                    declarations.append(
                        (job_name, step.get("name", step.get("uses")), step["env"]["GOFLAGS"])
                    )

        self.assertEqual(
            declarations,
            [
                ("preflight", "job", "-gcflags=all=-dwarf=false"),
                ("test-unit", "job", "-gcflags=all=-dwarf=false"),
                ("test-integration", "job", "-gcflags=all=-dwarf=false"),
                ("golangci-lint", "job", "-gcflags=all=-dwarf=false"),
                ("backend-security", "job", "-gcflags=all=-dwarf=false"),
            ],
        )

    def test_backend_security_reuses_the_shared_nodwarf_rolling_cache(self) -> None:
        steps = self.jobs["backend-security"]["steps"]
        setup_go = next(
            step for step in steps if step.get("uses") == "actions/setup-go@v6"
        )
        self.assertFalse(setup_go["with"]["cache"])

        scan_index = next(
            index
            for index, step in enumerate(steps)
            if step.get("name") == "Run govulncheck"
        )
        cache_indexes = [
            index
            for index, step in enumerate(steps)
            if step.get("uses") == "./.github/actions/go-rolling-cache"
        ]

        self.assertEqual(len(cache_indexes), 1)
        cache_index = cache_indexes[0]
        self.assertLess(cache_index, scan_index)
        cache = steps[cache_index]
        self.assertEqual(cache["with"]["prefix"], "security-nodwarf-v1")
        self.assertNotIn("refresh_on_backend_change", cache.get("with", {}))
        self.assertFalse(
            [
                step
                for step in steps
                if step.get("id") == "security_cache_epoch"
                or step.get("name") == "Cache govulncheck build objects"
            ]
        )

    def test_heavy_go_jobs_use_nodwarf_build_cache_strategy(self) -> None:
        expected_prefixes = {
            "preflight": "preflight-nodwarf-v1",
            "test-unit": "unit-nodwarf-v3",
            "test-integration": "integration-nodwarf-v2",
            "golangci-lint": "lint-nodwarf-v1",
            "backend-security": "security-nodwarf-v1",
        }
        for job_name, expected_prefix in expected_prefixes.items():
            cache_step = next(
                step
                for step in self.jobs[job_name]["steps"]
                if step.get("uses") == "./.github/actions/go-rolling-cache"
            )
            with self.subTest(job=job_name):
                self.assertEqual(cache_step["with"]["prefix"], expected_prefix)

        integration_cache = next(
            step
            for step in self.jobs["test-integration"]["steps"]
            if step.get("uses") == "./.github/actions/go-rolling-cache"
        )
        self.assertEqual(
            integration_cache["with"]["prefix"],
            "integration-nodwarf-v2",
        )
        self.assertEqual(
            integration_cache["with"]["refresh_daily"],
            "true",
        )
        self.assertEqual(
            integration_cache["with"]["save_caches"],
            "false",
        )
        self.assertEqual(
            integration_cache["with"]["build_cache_path"],
            "${{ github.workspace }}/.cache/go-build-integration",
        )
        self.assertEqual(
            self.jobs["test-integration"]["env"]["GOCACHE"],
            "${{ github.workspace }}/.cache/go-build-integration",
        )
        self.assertNotIn(
            "refresh_on_backend_change",
            integration_cache.get("with", {}),
        )

        unit_cache = next(
            step
            for step in self.jobs["test-unit"]["steps"]
            if step.get("uses") == "./.github/actions/go-rolling-cache"
        )
        self.assertEqual(
            unit_cache["with"]["prefix"],
            "unit-nodwarf-v3",
        )
        self.assertEqual(
            unit_cache["with"]["fallback_prefix"],
            "unit-nodwarf-v1",
        )
        self.assertEqual(
            unit_cache["with"]["refresh_on_backend_change"],
            "true",
        )
        self.assertNotIn("refresh_daily", unit_cache["with"])
        self.assertNotIn("build_cache_path", unit_cache["with"])
        self.assertEqual(
            unit_cache["with"]["save_caches"],
            "true",
        )
        self.assertNotIn("GOCACHE", self.jobs["test-unit"]["env"])

        for job_name in ("preflight", "golangci-lint", "backend-security"):
            cache_step = next(
                step
                for step in self.jobs[job_name]["steps"]
                if step.get("uses") == "./.github/actions/go-rolling-cache"
            )
            with self.subTest(job=job_name):
                self.assertNotIn("refresh_on_backend_change", cache_step.get("with", {}))

    def test_unit_avoids_relinking_go_artifact_owners_after_unit_tests(self) -> None:
        steps = self.jobs["test-unit"]["steps"]
        step_names = {step.get("name") for step in steps}
        self.assertTrue(
            {
                "Model surface bundle drift",
                "Model family alert artifact drift",
            }.isdisjoint(step_names),
        )

    def test_go_artifact_drift_has_no_relinking_workflow_owner(self) -> None:
        owners = []
        for job_name, job in self.jobs.items():
            for step in job.get("steps", []):
                command = step.get("run", "")
                for helper in (
                    "scripts/checks/check-model-surface-bundle.sh",
                    "scripts/sentinels/check-model-family-rules.sh",
                ):
                    if helper in command:
                        owners.append((helper, job_name, step.get("name")))

        self.assertEqual(owners, [])

    def test_lint_uses_rolling_analysis_cache_instead_of_action_cache(self) -> None:
        steps = self.jobs["golangci-lint"]["steps"]
        rolling_cache = next(
            step
            for step in steps
            if step.get("uses") == "./.github/actions/go-rolling-cache"
        )
        self.assertEqual(rolling_cache["with"]["prefix"], "lint-nodwarf-v1")
        self.assertEqual(rolling_cache["with"]["golangci_cache"], "true")

        lint_action = next(
            step
            for step in steps
            if step.get("uses") == "golangci/golangci-lint-action@v9"
        )
        self.assertEqual(lint_action["with"]["skip-cache"], "true")

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
