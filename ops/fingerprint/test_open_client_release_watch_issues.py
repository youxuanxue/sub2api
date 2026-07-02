#!/usr/bin/env python3
"""Unit tests for client-release-watch issue helpers."""
from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from open_client_release_watch_issues import filename_safe, issue_body_path


class OpenClientReleaseWatchIssuesTest(unittest.TestCase):
    def test_issue_body_path_is_filesystem_safe(self) -> None:
        platform_id = "claude-code"
        path = issue_body_path(Path(".cache/fingerprint/client-release-watch"), platform_id)
        self.assertEqual(path.name, f"issue-{filename_safe(platform_id)}.md")


if __name__ == "__main__":
    unittest.main()
