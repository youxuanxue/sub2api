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
        self.assertNotIn("go_cache_prune.py --check", text)


if __name__ == "__main__":
    unittest.main()
