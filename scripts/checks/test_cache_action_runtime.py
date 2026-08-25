#!/usr/bin/env python3
"""Contract tests for the GitHub Actions cache runtime major."""

from __future__ import annotations

from pathlib import Path
import re
import unittest


REPO_ROOT = Path(__file__).resolve().parents[2]
GITHUB_DIR = REPO_ROOT / ".github"
CACHE_ACTION = re.compile(
    r"actions/cache(?P<variant>/(?:restore|save))?@v(?P<major>\d+)"
)
REQUIRED_MAJOR = "6"


class CacheActionRuntimeTest(unittest.TestCase):
    def test_all_direct_cache_actions_use_node24_runtime_major(self) -> None:
        matches: list[tuple[Path, int, str]] = []
        outdated: list[str] = []

        workflow_files = sorted(GITHUB_DIR.rglob("*.yml")) + sorted(
            GITHUB_DIR.rglob("*.yaml")
        )
        for path in workflow_files:
            for line_number, line in enumerate(
                path.read_text(encoding="utf-8").splitlines(), start=1
            ):
                for match in CACHE_ACTION.finditer(line):
                    action = match.group(0)
                    matches.append((path, line_number, action))
                    if match.group("major") != REQUIRED_MAJOR:
                        relative = path.relative_to(REPO_ROOT)
                        outdated.append(f"{relative}:{line_number}: {action}")

        self.assertTrue(matches, "expected at least one direct actions/cache reference")
        self.assertEqual(
            outdated,
            [],
            "direct cache actions must use v6 (Node 24 runtime):\n"
            + "\n".join(outdated),
        )


if __name__ == "__main__":
    unittest.main()
