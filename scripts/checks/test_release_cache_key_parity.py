#!/usr/bin/env python3
"""Repository-level behavior tests for the release cache warm workflow."""

from __future__ import annotations

from pathlib import Path
import unittest

import yaml


REPO_ROOT = Path(__file__).resolve().parents[2]
BACKEND_CI = REPO_ROOT / ".github" / "workflows" / "backend-ci.yml"
WARM_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "warm-release-cache-main.yml"


def load_workflow(path: Path) -> dict:
    return yaml.safe_load(path.read_text(encoding="utf-8"))


def workflow_on(workflow: dict) -> dict:
    return workflow.get("on") or workflow.get(True) or {}


class ReleaseCacheWorkflowTest(unittest.TestCase):
    def test_default_ci_does_not_wait_for_release_cache_warming(self) -> None:
        jobs = load_workflow(BACKEND_CI)["jobs"]
        self.assertNotIn("warm-release-cache", jobs)

    def test_warm_workflow_runs_only_for_main_backend_build_inputs(self) -> None:
        workflow = load_workflow(WARM_WORKFLOW)
        push = workflow_on(workflow)["push"]
        self.assertEqual(push["branches"], ["main"])
        self.assertEqual(
            set(push["paths"]),
            {
                "backend/**/*.go",
                "backend/go.mod",
                "backend/go.sum",
                ".new-api-ref",
                ".goreleaser.yaml",
                ".github/workflows/warm-release-cache-main.yml",
            },
        )
        job = workflow["jobs"]["warm-release-cache"]
        self.assertEqual(job.get("if"), "github.ref == 'refs/heads/main'")

    def test_warm_workflow_owns_daily_integration_cache_writes(self) -> None:
        workflow = load_workflow(WARM_WORKFLOW)
        steps = workflow["jobs"]["warm-release-cache"]["steps"]
        epoch = next(step for step in steps if step.get("id") == "integration_epoch")
        self.assertIn('day=$(date -u +%Y-%m-%d)', epoch["run"])

        cache = next(step for step in steps if step.get("id") == "integration_cache")
        self.assertEqual(cache["uses"], "actions/cache@v6")
        self.assertEqual(
            cache["with"]["path"],
            "${{ github.workspace }}/.cache/go-build-integration",
        )
        dependency_hash = (
            "${{ hashFiles('backend/go.mod', 'backend/go.sum', '.new-api-ref') }}"
        )
        key_prefix = "${{ runner.os }}-gobuild-integration-nodwarf-v2-"
        self.assertEqual(
            cache["with"]["key"],
            f"{key_prefix}{dependency_hash}-${{{{ steps.integration_epoch.outputs.day }}}}",
        )
        self.assertEqual(
            cache["with"]["restore-keys"],
            f"{key_prefix}{dependency_hash}-\n{key_prefix}\n",
        )

        warm = next(
            step for step in steps if step.get("name") == "Warm integration build cache"
        )
        self.assertEqual(warm["if"], "steps.integration_cache.outputs.cache-hit != 'true'")
        self.assertEqual(
            warm["env"]["GOCACHE"],
            "${{ github.workspace }}/.cache/go-build-integration",
        )
        self.assertEqual(warm["env"]["GOFLAGS"], "-gcflags=all=-dwarf=false")
        self.assertIn("scripts/ci/integration-packages.py", warm["run"])
        self.assertIn("go test -c -tags=integration", warm["run"])


if __name__ == "__main__":
    unittest.main()
