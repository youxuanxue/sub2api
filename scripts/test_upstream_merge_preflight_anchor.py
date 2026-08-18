#!/usr/bin/env python3
"""Regression tests for #1685 PREFLIGHT_FAIL on merge/upstream-2026-08-15.

The daily agent failed because:
1. high-risk migrations 222/223 had no docs/approved anchor, and docs/* hid
   that directory so a newly written anchor could not be staged.
2. auto-retry `gh workflow run` omitted --repo and 404'd on Wei-Shaw/sub2api.
"""
from __future__ import annotations

import pathlib
import subprocess
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent
WORKFLOW = REPO_ROOT / ".github/workflows/upstream-merge-agent-daily.yml"
APPROVED_DOC = REPO_ROOT / "docs/approved/upstream-merge-2026-08-15-migrations.md"


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


class AutoRetryRepoPinTest(unittest.TestCase):
    def test_workflow_dispatch_pins_this_repository(self) -> None:
        text = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("gh workflow run upstream-merge-agent-daily.yml", text)
        self.assertIn('--repo "$GITHUB_REPOSITORY"', text)

    def test_workflow_run_is_not_repo_ambiguous(self) -> None:
        text = WORKFLOW.read_text(encoding="utf-8")
        self.assertNotIn(
            "gh workflow run upstream-merge-agent-daily.yml --ref main",
            text,
            "auto-retry must not call gh workflow run without --repo",
        )


if __name__ == "__main__":
    unittest.main()
