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


if __name__ == "__main__":
    unittest.main()
