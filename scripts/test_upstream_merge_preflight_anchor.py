#!/usr/bin/env python3
"""Regression tests for #1685 PREFLIGHT_FAIL on merge/upstream-2026-08-15.

The retired daily merge agent failed because high-risk migrations 222/223 had
no docs/approved anchor, and docs/* hid that directory so a newly written
anchor could not be staged. The gitignore exception remains load-bearing for
human-driven upstream merge PRs.
"""
from __future__ import annotations

import importlib.util
import pathlib
import subprocess
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent
WORKFLOW = REPO_ROOT / ".github/workflows/upstream-merge-notify.yml"
NOTIFY = REPO_ROOT / "scripts/upstream/notify-merge-needed.py"
APPROVED_DOC = REPO_ROOT / "docs/approved/upstream-merge-2026-08-15-migrations.md"


def _load_notify():
    spec = importlib.util.spec_from_file_location("notify_merge_needed", NOTIFY)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {NOTIFY}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _check_ignore(relpath: str) -> int:
    return subprocess.run(
        ["git", "check-ignore", "-q", relpath],
        cwd=REPO_ROOT,
        capture_output=True,
        check=False,
    ).returncode


class ApprovedDocsGitignoreTest(unittest.TestCase):
    def test_approval_anchor_is_stageable(self) -> None:
        self.assertTrue(APPROVED_DOC.is_file(), f"missing {APPROVED_DOC}")
        self.assertNotEqual(
            _check_ignore(str(APPROVED_DOC.relative_to(REPO_ROOT))),
            0,
            "docs/approved/*.md must not match docs/* gitignore",
        )

    def test_unexcepted_docs_path_still_ignored(self) -> None:
        self.assertEqual(
            _check_ignore("docs/not-an-approved-anchor.md"),
            0,
            "docs/* must still ignore files outside the approved/ exception",
        )


class NotifyWorkflowIsDetectionOnlyTest(unittest.TestCase):
    def test_workflow_does_not_auto_merge(self) -> None:
        self.assertTrue(WORKFLOW.is_file(), f"missing {WORKFLOW}")
        _load_notify().assert_workflow_is_notify_only(WORKFLOW.read_text(encoding="utf-8"))

    def test_no_workflow_runs_unattended_upstream_merge(self) -> None:
        for path in sorted((REPO_ROOT / ".github/workflows").glob("*.yml")):
            text = path.read_text(encoding="utf-8")
            self.assertNotIn(
                "name: Daily Upstream Merge Agent",
                text,
                f"{path.name} reintroduced the retired auto-merge agent",
            )
            self.assertNotIn(
                "tk-upstream-agent[bot]",
                text,
                f"{path.name} still pushes as the retired auto-merge identity",
            )


if __name__ == "__main__":
    unittest.main()
