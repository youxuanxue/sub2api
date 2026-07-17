#!/usr/bin/env python3
"""Unit tests for client-release-watch issue helpers."""
from __future__ import annotations

import sys
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))

from open_client_release_watch_issues import filename_safe, issue_body_path, sync_issues


class OpenClientReleaseWatchIssuesTest(unittest.TestCase):
    def test_issue_body_path_is_filesystem_safe(self) -> None:
        platform_id = "claude-code"
        path = issue_body_path(Path(".cache/fingerprint/client-release-watch"), platform_id)
        self.assertEqual(path.name, f"issue-{filename_safe(platform_id)}.md")

    def test_sync_issues_returns_no_links_for_aligned_report(self) -> None:
        report = {
            "platforms": [
                {
                    "id": "codex-cli",
                    "name": "Codex CLI",
                    "pinned": "1",
                    "upstream_latest": "1",
                    "drift": False,
                }
            ]
        }
        with patch("open_client_release_watch_issues.ensure_base_labels"), \
            patch("open_client_release_watch_issues.ensure_label"), \
            patch("open_client_release_watch_issues.sh") as mock_sh:
            mock_sh.return_value.stdout = "[]"
            self.assertEqual(sync_issues(report, cache_dir=Path(".cache/fingerprint/client-release-watch"), umbrella=True), [])

    def test_sync_issues_returns_existing_drift_issue_link(self) -> None:
        report = {
            "run_url": "https://github.com/youxuanxue/sub2api/actions/runs/1",
            "platforms": [
                {
                    "id": "claude-code",
                    "name": "Claude Code",
                    "pinned": "2.1.197",
                    "upstream_latest": "2.1.198",
                    "status": "drift",
                    "pin_path": "internal/example.go",
                    "skill": "tokenkey-cc-fingerprint-alignment",
                    "upstream_sources": {
                        "npm": {
                            "version": "2.1.198",
                            "url": "https://www.npmjs.com/package/@anthropic-ai/claude-code",
                        }
                    },
                    "drift": True,
                }
            ],
        }

        def fake_sh(args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
            if args[:3] == ["gh", "issue", "list"]:
                return subprocess.CompletedProcess(args, 0, stdout="1136", stderr="")
            if args[:3] == ["gh", "issue", "view"]:
                return subprocess.CompletedProcess(
                    args,
                    0,
                    stdout="https://github.com/youxuanxue/sub2api/issues/1136\n",
                    stderr="",
                )
            return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

        with tempfile.TemporaryDirectory() as tmp, \
            patch("open_client_release_watch_issues.ensure_base_labels"), \
            patch("open_client_release_watch_issues.ensure_label"), \
            patch("open_client_release_watch_issues.sh", side_effect=fake_sh):
            links = sync_issues(report, cache_dir=Path(tmp), umbrella=True)

        self.assertEqual(len(links), 1)
        self.assertEqual(links[0]["signal_type"], "release-drift")
        self.assertEqual(links[0]["platform_id"], "claude-code")
        self.assertEqual(links[0]["number"], 1136)
        self.assertEqual(links[0]["status"], "updated")
        self.assertEqual(links[0]["url"], "https://github.com/youxuanxue/sub2api/issues/1136")


if __name__ == "__main__":
    unittest.main()
