#!/usr/bin/env python3
"""Workflow behavior tests for changed-surface routing of slow ops contracts."""

from __future__ import annotations

from pathlib import Path
import unittest

import yaml


REPO_ROOT = Path(__file__).resolve().parents[1]
BACKEND_CI = REPO_ROOT / ".github" / "workflows" / "backend-ci.yml"
PREFLIGHT = REPO_ROOT / "scripts" / "preflight.sh"


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

    def test_required_preflight_skips_go_contracts_when_go_surface_is_unchanged(self) -> None:
        workflow = load_workflow(BACKEND_CI)
        preflight = workflow["jobs"]["preflight"]
        run_preflight = next(
            step for step in preflight["steps"] if step.get("name") == "Run preflight"
        )
        self.assertEqual(
            run_preflight["env"]["PREFLIGHT_SKIP_GO_CONTRACTS"],
            "${{ needs.changes.outputs.preflight_go != 'true' && '1' || '0' }}",
        )

    def test_required_preflight_defers_go_artifacts_only_when_unit_runs(self) -> None:
        workflow = load_workflow(BACKEND_CI)
        run_preflight = next(
            step
            for step in workflow["jobs"]["preflight"]["steps"]
            if step.get("name") == "Run preflight"
        )
        self.assertEqual(
            run_preflight["env"]["PREFLIGHT_DEFER_GO_ARTIFACT_DRIFT"],
            "${{ (needs.changes.outputs.all == 'true' || "
            "needs.changes.outputs.backend == 'true') && '1' || '0' }}",
        )

        declarations = []
        for job_name, job in workflow["jobs"].items():
            if "PREFLIGHT_DEFER_GO_ARTIFACT_DRIFT" in job.get("env", {}):
                declarations.append((job_name, "job"))
            for step in job.get("steps", []):
                if "PREFLIGHT_DEFER_GO_ARTIFACT_DRIFT" in step.get("env", {}):
                    declarations.append((job_name, step.get("name")))
        self.assertEqual(declarations, [("preflight", "Run preflight")])

    def test_local_preflight_does_not_honor_ci_go_artifact_delegation(self) -> None:
        script = PREFLIGHT.read_text(encoding="utf-8")
        artifact_sections = script.split(
            "# ---- sub2api: generated model-surface bundle drift", 1
        )[1].split("# ---- sub2api: frontend TK sentinel registry", 1)[0]

        self.assertIn('"${GITHUB_ACTIONS:-}" = "true"', artifact_sections)
        self.assertIn("PREFLIGHT_DEFER_GO_ARTIFACT_DRIFT", artifact_sections)
        self.assertEqual(
            artifact_sections.count("_preflight_defer_go_artifact_drift"),
            4,
        )


if __name__ == "__main__":
    unittest.main()
