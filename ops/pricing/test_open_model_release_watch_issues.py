#!/usr/bin/env python3
"""Unit tests for model-release-watch issue helpers."""
from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
from open_model_release_watch_issues import (  # noqa: E402
    build_body,
    filename_safe,
    issue_body_path,
    sync_issues,
)


class OpenModelReleaseWatchIssuesTest(unittest.TestCase):
    def test_issue_body_path_is_filesystem_safe(self) -> None:
        path = issue_body_path(Path(".cache/model-release-watch"), "anthropic", "claude-sonnet-5-5")
        self.assertEqual(path.name, "issue-anthropic-claude-sonnet-5-5.md")
        self.assertEqual(filename_safe("a:b/c"), "a-b-c")

    def test_build_body_mentions_multi_channel_followup(self) -> None:
        body = build_body(
            {
                "status": "unpriced",
                "vendor": "anthropic",
                "model_id": "claude-sonnet-5-5",
                "priced": False,
                "primary_coverage": False,
                "surfaces": ["platform:bedrock"],
                "source_url": "https://example",
                "notes": "",
            },
            {"run_url": "https://github.com/example/actions/runs/1"},
        )
        self.assertIn("platform:bedrock", body)
        self.assertIn("tokenkey-modelops-planner", body)
        self.assertIn("Wildcard supplier mappings do not count", body)

    def test_sync_issues_skips_served_and_creates_actionable(self) -> None:
        report = {
            "run_url": "https://example/run",
            "findings": [
                {
                    "status": "served",
                    "vendor": "openai",
                    "model_id": "gpt-6-astra",
                    "priced": True,
                    "primary_coverage": True,
                    "surfaces": ["platform:openai"],
                    "source_url": "https://example",
                    "notes": "",
                },
                {
                    "status": "unpriced",
                    "vendor": "anthropic",
                    "model_id": "claude-sonnet-5-5",
                    "priced": False,
                    "primary_coverage": False,
                    "surfaces": ["platform:bedrock"],
                    "source_url": "https://example",
                    "notes": "",
                    "actionable": True,
                    "in_scope": True,
                    "is_new": True,
                    "issue_suppressed": False,
                },
                {
                    "status": "missing",
                    "vendor": "google",
                    "model_id": "gemini-3.1-pro-preview",
                    "priced": True,
                    "primary_coverage": False,
                    "surfaces": [],
                    "source_url": "https://example",
                    "notes": "suppressed: previously seen",
                    "actionable": False,
                    "in_scope": True,
                    "is_new": False,
                    "issue_suppressed": True,
                },
            ],
        }

        def fake_sh(args, check=True):  # noqa: ANN001
            class Result:
                def __init__(self, stdout: str = "") -> None:
                    self.stdout = stdout

            if args[:3] == ["gh", "issue", "list"] and "--label" in args:
                # existing lookup by sig → empty
                return Result("")
            if args[:3] == ["gh", "issue", "create"]:
                return Result("https://github.com/youxuanxue/sub2api/issues/9999")
            if args[:3] == ["gh", "issue", "list"] and "model-release-watch" in args:
                return Result("[]")
            return Result("")

        with tempfile.TemporaryDirectory() as tmp:
            with patch("open_model_release_watch_issues.ensure_base_labels"), \
                patch("open_model_release_watch_issues.ensure_label"), \
                patch("open_model_release_watch_issues.sh", side_effect=fake_sh), \
                patch("open_model_release_watch_issues.subprocess.run"):
                links = sync_issues(report, cache_dir=Path(tmp), umbrella=True)

        self.assertEqual(len(links), 1)
        self.assertEqual(links[0]["model_id"], "claude-sonnet-5-5")
        self.assertEqual(links[0]["status"], "created")
        self.assertEqual(links[0]["number"], 9999)


if __name__ == "__main__":
    unittest.main()
