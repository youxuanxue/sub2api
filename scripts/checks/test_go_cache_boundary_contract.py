#!/usr/bin/env python3
"""Mechanical gates for the Go cache boundary: trimpath, one writer key, no setup-go cache."""

from __future__ import annotations

from pathlib import Path
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]
WORKFLOWS = ROOT / ".github" / "workflows"
TRIMPATH = "-trimpath"


def load(path: Path) -> dict:
    return yaml.safe_load(path.read_text(encoding="utf-8"))


class GoCacheBoundaryContractTest(unittest.TestCase):
    def test_workflow_goflags_keep_trimpath(self) -> None:
        missing: list[str] = []
        for path in (WORKFLOWS / "backend-ci.yml", WORKFLOWS / "warm-release-cache-main.yml"):
            workflow = load(path)
            for job_name, job in workflow.get("jobs", {}).items():
                flags = job.get("env", {}).get("GOFLAGS")
                if flags is not None and TRIMPATH not in flags:
                    missing.append(f"{path.name}:{job_name}")
                for step in job.get("steps", []):
                    step_flags = step.get("env", {}).get("GOFLAGS")
                    if step_flags is not None and TRIMPATH not in step_flags:
                        missing.append(f"{path.name}:{job_name}:{step.get('name')}")
        self.assertEqual(missing, [])

    def test_setup_go_never_enables_implicit_cache(self) -> None:
        offenders: list[str] = []
        for path in WORKFLOWS.glob("*.yml"):
            workflow = load(path)
            for job_name, job in (workflow.get("jobs") or {}).items():
                for index, step in enumerate(job.get("steps") or []):
                    if step.get("uses", "").startswith("actions/setup-go@"):
                        cache = step.get("with", {}).get("cache")
                        if cache is True or cache == "true" or cache is None:
                            offenders.append(f"{path.name}:{job_name}:{index}")
        self.assertEqual(offenders, [])

    def test_persistent_go_keys_forbid_run_id_and_dates(self) -> None:
        forbidden = ("github.run_id", "%Y-%m-%d", "%G-W%V")
        offenders: list[str] = []
        for path in (
            WORKFLOWS / "backend-ci.yml",
            WORKFLOWS / "warm-release-cache-main.yml",
            WORKFLOWS / "release.yml",
            ROOT / ".github" / "actions" / "go-rolling-cache" / "action.yml",
        ):
            text = path.read_text(encoding="utf-8")
            for token in forbidden:
                if token in text and "go-release" in text or token in text and "gobuild" in text:
                    if token in text:
                        offenders.append(f"{path.name}:{token}")
        self.assertEqual(offenders, [])


    def test_warm_release_cache_heals_budget_overflow(self) -> None:
        text = (WORKFLOWS / "warm-release-cache-main.yml").read_text(encoding="utf-8")
        self.assertIn("go_cache_prune.py --heal", text)
        self.assertIn("go_cache_prune.py --save-budget analysis", text)
        self.assertIn("go_cache_prune.py --save-budget release", text)
        self.assertNotIn("go_cache_prune.py --check", text)

    def test_all_warm_saves_are_budgeted_and_heal_precedes_restore(self) -> None:
        steps = load(WORKFLOWS / "warm-release-cache-main.yml")["jobs"]["warm-release-cache"]["steps"]
        heal = next(i for i, s in enumerate(steps) if "go_cache_prune.py --heal" in s.get("run", ""))
        restores = [i for i, s in enumerate(steps) if s.get("uses") == "actions/cache/restore@v6"]
        self.assertLess(heal, min(restores))
        for family in ("gomod", "test", "integration", "analysis", "release"):
            gate_id = f"{family}_save_budget"
            gate = next(s for s in steps if s.get("id") == gate_id)
            self.assertIn(f"--save-budget {family} --path", gate["run"])
            self.assertNotIn("if python3", gate["run"])
            saving = [s for s in steps if s.get("uses") == "actions/cache/save@v6" and f"steps.{gate_id}.outputs.fits == 'true'" in s.get("if", "")]
            self.assertEqual(len(saving), 1, family)

    def test_required_workflows_do_not_compete_with_warm_cache_writer(self) -> None:
        for path in WORKFLOWS.glob("*.yml"):
            for job in load(path).get("jobs", {}).values():
                for step in job.get("steps", []):
                    if step.get("uses") == "./.github/actions/go-rolling-cache":
                        self.assertIn(step.get("with", {}).get("save_caches", "false"), (False, "false"), str(path))


if __name__ == "__main__":
    unittest.main()
