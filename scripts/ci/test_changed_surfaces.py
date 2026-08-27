#!/usr/bin/env python3
"""Behavior tests for CI changed-surface classification."""

from __future__ import annotations

import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import unittest


MODULE_PATH = Path(__file__).resolve().parent / "changed-surfaces.py"
SPEC = importlib.util.spec_from_file_location("changed_surfaces", MODULE_PATH)
assert SPEC and SPEC.loader
changed_surfaces = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(changed_surfaces)


class ChangedSurfacesTest(unittest.TestCase):
    def test_backend_go_change_runs_backend_jobs_only(self) -> None:
        self.assertEqual(
            changed_surfaces.classify(["backend/internal/service/account_service.go"]),
            {
                "backend": True,
                "frontend": False,
                "deploy": False,
                "ops": False,
                "contracts": False,
                "preflight_go": True,
                "service_unit_cold": True,
                "all": False,
            },
        )

    def test_frontend_change_runs_frontend_jobs_only(self) -> None:
        self.assertEqual(
            changed_surfaces.classify(["frontend/src/views/admin/Accounts.vue"]),
            {
                "backend": False,
                "frontend": True,
                "deploy": False,
                "ops": False,
                "contracts": False,
                "preflight_go": False,
                "service_unit_cold": False,
                "all": False,
            },
        )

    def test_deploy_change_runs_deploy_jobs_only(self) -> None:
        self.assertEqual(
            changed_surfaces.classify(["deploy/tests/docker-runtime-resources-test.sh"]),
            {
                "backend": False,
                "frontend": False,
                "deploy": True,
                "ops": False,
                "contracts": False,
                "preflight_go": False,
                "service_unit_cold": False,
                "all": False,
            },
        )

    def test_route_change_runs_backend_and_contract_jobs(self) -> None:
        self.assertEqual(
            changed_surfaces.classify(["backend/internal/server/routes/user.go"]),
            {
                "backend": True,
                "frontend": False,
                "deploy": False,
                "ops": False,
                "contracts": True,
                "preflight_go": True,
                "service_unit_cold": True,
                "all": False,
            },
        )

    def test_ops_contract_change_runs_backend_and_ops_contracts(self) -> None:
        self.assertEqual(
            changed_surfaces.classify(["backend/internal/observability/qa/bundle/service.go"]),
            {
                "backend": True,
                "frontend": False,
                "deploy": False,
                "ops": True,
                "contracts": False,
                "preflight_go": True,
                "service_unit_cold": True,
                "all": False,
            },
        )

    def test_docs_only_change_keeps_expensive_jobs_off(self) -> None:
        self.assertEqual(
            changed_surfaces.classify(["docs/runbook.md"]),
            {
                "backend": False,
                "frontend": False,
                "deploy": False,
                "ops": False,
                "contracts": False,
                "preflight_go": False,
                "service_unit_cold": False,
                "all": False,
            },
        )

    def test_cache_snapshot_change_keeps_expensive_jobs_off(self) -> None:
        self.assertEqual(
            changed_surfaces.classify([".cache/anthropic/cc-triage.json"]),
            {
                "backend": False,
                "frontend": False,
                "deploy": False,
                "ops": False,
                "contracts": False,
                "preflight_go": False,
                "service_unit_cold": False,
                "all": False,
            },
        )

    def test_backend_sentinel_does_not_escalate_a_backend_change_to_all_surfaces(self) -> None:
        self.assertEqual(
            changed_surfaces.classify(
                [
                    "backend/internal/service/gateway_usage_billing.go",
                    "scripts/sentinels/gateway-tk.json",
                ]
            ),
            {
                "backend": True,
                "frontend": False,
                "deploy": False,
                "ops": False,
                "contracts": False,
                "preflight_go": True,
                "service_unit_cold": True,
                "all": False,
            },
        )

    def test_mixed_change_combines_surfaces(self) -> None:
        self.assertEqual(
            changed_surfaces.classify(["backend/go.mod", "frontend/pnpm-lock.yaml"]),
            {
                "backend": True,
                "frontend": True,
                "deploy": False,
                "ops": False,
                "contracts": False,
                "preflight_go": True,
                "service_unit_cold": True,
                "all": False,
            },
        )

    def test_ci_infrastructure_change_runs_everything(self) -> None:
        result = changed_surfaces.classify([".github/actions/go-rolling-cache/action.yml"])
        self.assertEqual(
            result,
            {
                "backend": False,
                "frontend": False,
                "deploy": False,
                "ops": False,
                "contracts": False,
                "preflight_go": True,
                "service_unit_cold": False,
                "all": True,
            },
        )

    def test_unknown_path_fails_safe_by_running_everything(self) -> None:
        result = changed_surfaces.classify(["unexpected/new-surface.bin"])
        self.assertEqual(
            result,
            {
                "backend": False,
                "frontend": False,
                "deploy": False,
                "ops": False,
                "contracts": False,
                "preflight_go": True,
                "service_unit_cold": False,
                "all": True,
            },
        )

    def test_whitespace_in_a_git_path_is_not_normalized_into_a_known_surface(self) -> None:
        result = changed_surfaces.classify([" backend/internal/service/account_service.go"])
        self.assertEqual(
            result,
            {
                "backend": False,
                "frontend": False,
                "deploy": False,
                "ops": False,
                "contracts": False,
                "preflight_go": True,
                "service_unit_cold": False,
                "all": True,
            },
        )

    def test_backend_non_go_change_keeps_service_cache_path(self) -> None:
        self.assertEqual(
            changed_surfaces.classify(["backend/migrations/001.sql"]),
            {
                "backend": True,
                "frontend": False,
                "deploy": False,
                "ops": False,
                "contracts": False,
                "preflight_go": True,
                "service_unit_cold": False,
                "all": False,
            },
        )

    def test_go_derived_preflight_contract_changes_enable_go_bootstrap(self) -> None:
        for path in (
            "ops/pricing/model-surface-bundle.json",
            "ops/observability/generated/model-family-rules.json",
            "scripts/checks/check-model-surface-bundle.sh",
            "scripts/sentinels/check-model-family-rules.sh",
            "scripts/preflight.sh",
        ):
            with self.subTest(path=path):
                self.assertTrue(changed_surfaces.classify([path])["preflight_go"])

    def test_unit_runner_and_ci_entrypoints_force_service_cold_path(self) -> None:
        for path in (
            ".new-api-ref",
            "backend/go.mod",
            "backend/go.sum",
            "scripts/ci/list_go_tests.go",
            "scripts/ci/unit_test_runner.py",
            "scripts/ci/test_unit_test_runner.py",
            "backend/Makefile",
            ".github/workflows/backend-ci.yml",
        ):
            with self.subTest(path=path):
                self.assertTrue(changed_surfaces.classify([path])["service_unit_cold"])

    def test_all_mode_marks_service_cold_path(self) -> None:
        completed = subprocess.run(
            [sys.executable, str(MODULE_PATH), "--all"],
            input="",
            check=False,
            capture_output=True,
            text=True,
        )

        self.assertEqual(completed.returncode, 0, completed.stderr)
        result = json.loads(completed.stdout)
        self.assertEqual(set(result), set(changed_surfaces.KEYS))
        self.assertTrue(all(result.values()))


if __name__ == "__main__":
    unittest.main()
