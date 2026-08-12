#!/usr/bin/env python3
from __future__ import annotations

import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = REPO_ROOT / ".github/workflows/client-fidelity-watch.yml"
LEGACY_WORKFLOWS = (
    REPO_ROOT / ".github/workflows/client-release-watch.yml",
    REPO_ROOT / ".github/workflows/prompt-surface-watch.yml",
)


class ClientFidelityWorkflowContractTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.text = WORKFLOW.read_text(encoding="utf-8")

    def test_one_scheduled_manual_umbrella_remains(self) -> None:
        self.assertIn("schedule:", self.text)
        self.assertIn("workflow_dispatch:", self.text)
        self.assertIn("release-scan:", self.text)
        self.assertIn("prod-aggregate:", self.text)
        self.assertIn("kiro-production-configured:", self.text)
        self.assertFalse(any(path.exists() for path in LEGACY_WORKFLOWS))

    def test_cache_pr_factory_cannot_return(self) -> None:
        forbidden = (
            "client-release-watch-cache",
            "cache_pr_url",
            "git push --force-with-lease",
            "gh pr create --draft",
            "WIP: chore: refresh client release watch cache",
            "pull-requests: write",
            "contents: write",
        )
        for marker in forbidden:
            with self.subTest(marker=marker):
                self.assertNotIn(marker, self.text)

    def test_artifacts_issues_and_oidc_remain(self) -> None:
        self.assertIn("contents: read", self.text)
        self.assertIn("issues: write", self.text)
        self.assertIn("id-token: write", self.text)
        self.assertIn("actions/upload-artifact@v4", self.text)
        self.assertIn("open_client_release_watch_issues.py", self.text)
        self.assertIn("open_prompt_surface_watch_issues.py", self.text)
        self.assertIn("open_oauth_mimic_watch_issues.py", self.text)


if __name__ == "__main__":
    unittest.main()
