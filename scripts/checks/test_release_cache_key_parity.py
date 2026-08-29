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
        self.assertIn("backend/**/*.go", push["paths"])
        self.assertIn(".new-api-ref", push["paths"])
        job = workflow["jobs"]["warm-release-cache"]
        self.assertEqual(job.get("if"), "github.ref == 'refs/heads/main'")

    def test_warm_workflow_writes_five_families_with_explicit_save(self) -> None:
        workflow = load_workflow(WARM_WORKFLOW)
        self.assertEqual(workflow["permissions"]["actions"], "write")
        self.assertNotIn("GOFLAGS", workflow.get("env", {}))
        steps = workflow["jobs"]["warm-release-cache"]["steps"]
        save_steps = [step for step in steps if step.get("uses") == "actions/cache/save@v6"]
        restore_steps = [step for step in steps if step.get("uses") == "actions/cache/restore@v6"]
        self.assertEqual(len(save_steps), 5)
        self.assertEqual(len(restore_steps), 5)
        self.assertTrue(all("cache-hit != 'true'" in step.get("if", "") for step in save_steps))
        combined = [step for step in steps if step.get("uses") == "actions/cache@v6"]
        self.assertEqual(combined, [])
        keys = "\n".join(str(step.get("with", {}).get("key", "")) for step in restore_steps + save_steps)
        self.assertIn("gomod-v1-", keys)
        self.assertIn("gobuild-test-v1-", keys)
        self.assertIn("gobuild-integration-v1-", keys)
        self.assertIn("gobuild-analysis-v1-", keys)
        self.assertIn("go-release-v1-", keys)
        self.assertNotIn("github.run_id", keys)

        test_restore = next(step for step in restore_steps if "gobuild-test-v1-" in str(step.get("with", {}).get("key", "")))
        test_save = next(step for step in save_steps if "gobuild-test-v1-" in str(step.get("with", {}).get("key", "")))
        self.assertEqual(test_restore["with"]["path"], "~/.cache/go-build")
        self.assertEqual(test_save["with"]["path"], "~/.cache/go-build")
        analysis_restore = next(step for step in restore_steps if "gobuild-analysis-v1-" in str(step.get("with", {}).get("key", "")))
        self.assertIn("~/.cache/go-build", analysis_restore["with"]["path"])
        self.assertIn("~/.cache/golangci-lint", analysis_restore["with"]["path"])
        release_restore = next(step for step in restore_steps if "go-release-v1-" in str(step.get("with", {}).get("key", "")))
        release_save = next(step for step in save_steps if "go-release-v1-" in str(step.get("with", {}).get("key", "")))
        self.assertEqual(release_restore["with"]["path"], "~/.cache/go-build")
        self.assertEqual(release_save["with"]["path"], "~/.cache/go-build")

        warm = next(step for step in steps if step.get("name") == "Warm integration build cache")
        self.assertEqual(warm["if"], "steps.integration_cache.outputs.cache-hit != 'true'")
        self.assertEqual(
            warm["env"]["GOCACHE"],
            "${{ github.workspace }}/.cache/go-build-integration",
        )
        self.assertEqual(
            warm["env"]["GOFLAGS"], "-trimpath -gcflags=all=-dwarf=false"
        )
        self.assertIn("go test -c -tags=integration", warm["run"])
        self.assertNotIn("-vet=off", warm["run"])

        test_warm = next(
            step for step in steps if step.get("name") == "Warm test build cache"
        )
        self.assertEqual(
            test_warm["env"]["GOFLAGS"], "-trimpath -gcflags=all=-dwarf=false"
        )

        release_warm = next(
            step for step in steps if step.get("name") == "Warm cross-arch Go build cache"
        )
        self.assertEqual(release_warm["env"]["GOFLAGS"], "-trimpath")
        self.assertIn("-tags=embed", release_warm["run"])
        self.assertIn("./cmd/server", release_warm["run"])
        self.assertIn("./cmd/qa-archive", release_warm["run"])

        prune = next(step for step in steps if step.get("name") == "Check managed Go cache budget")
        self.assertIn("go_cache_prune.py --check", prune["run"])

    def test_warm_analysis_runs_golangci_lint_to_fill_analysis_family(self) -> None:
        # Boundary: analysis family includes ~/.cache/golangci-lint. Warm must
        # populate it, or an exact-hit after a go-test-only warm poisons the
        # lint job into restore-only against an empty golangci cache.
        lint_step = next(
            step
            for step in load_workflow(BACKEND_CI)["jobs"]["golangci-lint"]["steps"]
            if step.get("name") == "golangci-lint"
        )
        steps = load_workflow(WARM_WORKFLOW)["jobs"]["warm-release-cache"]["steps"]
        analysis_compile = next(
            step for step in steps if step.get("name") == "Warm analysis build cache"
        )
        self.assertEqual(
            analysis_compile["if"],
            "steps.analysis_cache.outputs.cache-hit != 'true'",
        )
        self.assertEqual(
            analysis_compile["env"]["GOFLAGS"],
            "-trimpath -gcflags=all=-dwarf=false",
        )
        self.assertIn("go test -run=^$", analysis_compile["run"])
        lint_warm = next(
            step
            for step in steps
            if str(step.get("uses", "")).startswith("golangci/golangci-lint-action@")
        )
        self.assertEqual(
            lint_warm["if"],
            "steps.analysis_cache.outputs.cache-hit != 'true'",
        )
        self.assertEqual(
            lint_warm["env"]["GOFLAGS"],
            "-trimpath -gcflags=all=-dwarf=false",
        )
        self.assertEqual(lint_warm["with"]["version"], lint_step["with"]["version"])
        self.assertEqual(lint_warm["with"]["args"], lint_step["with"]["args"])
        self.assertEqual(
            lint_warm["with"]["working-directory"],
            lint_step["with"]["working-directory"],
        )
        self.assertEqual(
            lint_warm["with"].get("skip-cache"),
            lint_step["with"].get("skip-cache"),
        )


if __name__ == "__main__":
    unittest.main()
