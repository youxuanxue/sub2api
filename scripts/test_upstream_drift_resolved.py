#!/usr/bin/env python3
"""Regression guard for upstream-merge tracking issue #1792."""
from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent
CHECK_DRIFT = REPO_ROOT / "scripts/upstream/check-drift.sh"
UPSTREAM_DRIFT_LIB = REPO_ROOT / "scripts/lib/upstream-drift.sh"


def _gate_status(
    *,
    head_ref: str | None = None,
    ref_name: str | None = None,
    cwd: pathlib.Path = REPO_ROOT,
) -> int:
    env = os.environ.copy()
    if head_ref is not None:
        if head_ref:
            env["GITHUB_HEAD_REF"] = head_ref
        else:
            env.pop("GITHUB_HEAD_REF", None)
    if ref_name is not None:
        if ref_name:
            env["GITHUB_REF_NAME"] = ref_name
        else:
            env.pop("GITHUB_REF_NAME", None)
    proc = subprocess.run(
        [
            "bash",
            "-c",
            f'source "{UPSTREAM_DRIFT_LIB}"; is_upstream_drift_gate_required',
        ],
        cwd=cwd,
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    return proc.returncode


class UpstreamSyncRegressionTest(unittest.TestCase):
    def test_tokenkey_not_behind_upstream_main(self) -> None:
        if _gate_status() != 0:
            self.skipTest(
                "upstream drift regression applies only to main and merge/upstream-* branches"
            )
        self.assertTrue(CHECK_DRIFT.is_file(), f"missing {CHECK_DRIFT}")
        proc = subprocess.run(
            ["bash", str(CHECK_DRIFT)],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            timeout=180,
            check=False,
        )
        combined = proc.stdout + proc.stderr
        self.assertEqual(proc.returncode, 0, combined)
        self.assertIn("TK behind: 0 commits", combined)


class UpstreamDriftGateTest(unittest.TestCase):
    def test_sync_pr_branch_runs_gate(self) -> None:
        self.assertEqual(_gate_status(head_ref="merge/upstream-2026-08-24"), 0)

    def test_main_push_skips_gate(self) -> None:
        self.assertEqual(_gate_status(head_ref="", ref_name="main"), 1)

    def test_feature_pr_branch_skips_gate(self) -> None:
        self.assertEqual(
            _gate_status(head_ref="fix/openai-responses-lite-parallel-tools"),
            1,
        )

    def test_feature_pr_skips_freshness_regression(self) -> None:
        env = os.environ.copy()
        env["GITHUB_HEAD_REF"] = "fix/openai-responses-lite-parallel-tools"
        env.pop("GITHUB_REF_NAME", None)
        proc = subprocess.run(
            [
                "python3",
                "-m",
                "unittest",
                "scripts.test_upstream_drift_resolved."
                "UpstreamSyncRegressionTest.test_tokenkey_not_behind_upstream_main",
                "-v",
            ],
            cwd=REPO_ROOT,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        combined = proc.stdout + proc.stderr
        self.assertEqual(proc.returncode, 0, combined)
        self.assertIn("skipped", combined)

    def test_main_push_skips_freshness_regression(self) -> None:
        env = os.environ.copy()
        env.pop("GITHUB_HEAD_REF", None)
        env["GITHUB_REF_NAME"] = "main"
        proc = subprocess.run(
            [
                "python3",
                "-m",
                "unittest",
                "scripts.test_upstream_drift_resolved."
                "UpstreamSyncRegressionTest.test_tokenkey_not_behind_upstream_main",
                "-v",
            ],
            cwd=REPO_ROOT,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        combined = proc.stdout + proc.stderr
        self.assertEqual(proc.returncode, 0, combined)
        self.assertIn("skipped", combined)

    def test_head_ref_takes_precedence_over_ref_name(self) -> None:
        self.assertEqual(
            _gate_status(
                head_ref="fix/openai-responses-lite-parallel-tools",
                ref_name="main",
            ),
            1,
        )

    def test_local_branch_fallback_skips_feature_branch(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_repo = pathlib.Path(temp_dir)
            subprocess.run(["git", "init", "-q"], cwd=temp_repo, check=True)
            subprocess.run(
                ["git", "checkout", "-qb", "fix/local-ci-gate"],
                cwd=temp_repo,
                check=True,
            )
            self.assertEqual(_gate_status(head_ref="", ref_name="", cwd=temp_repo), 1)


if __name__ == "__main__":
    unittest.main()
