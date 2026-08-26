#!/usr/bin/env python3
"""Contract tests for Docker GitHub Actions using the Node 24 runtime major."""

from __future__ import annotations

from pathlib import Path
import re
import unittest


REPO_ROOT = Path(__file__).resolve().parents[2]
GITHUB_DIR = REPO_ROOT / ".github"
DOCKER_ACTION = re.compile(
    r"docker/(?P<action>login-action|setup-buildx-action|setup-qemu-action)"
    r"@v(?P<major>\d+)"
)
REQUIRED_ACTIONS = {
    "login-action",
    "setup-buildx-action",
    "setup-qemu-action",
}
REQUIRED_MAJOR = "4"


class DockerActionRuntimeTest(unittest.TestCase):
    def test_all_direct_docker_actions_use_node24_runtime_major(self) -> None:
        found: set[str] = set()
        outdated: list[str] = []

        workflow_files = sorted(GITHUB_DIR.rglob("*.yml")) + sorted(
            GITHUB_DIR.rglob("*.yaml")
        )
        for path in workflow_files:
            for line_number, line in enumerate(
                path.read_text(encoding="utf-8").splitlines(), start=1
            ):
                for match in DOCKER_ACTION.finditer(line):
                    action_name = match.group("action")
                    action = match.group(0)
                    found.add(action_name)
                    if match.group("major") != REQUIRED_MAJOR:
                        relative = path.relative_to(REPO_ROOT)
                        outdated.append(f"{relative}:{line_number}: {action}")

        self.assertEqual(
            found,
            REQUIRED_ACTIONS,
            "Docker action runtime contract must cover every managed action",
        )
        self.assertEqual(
            outdated,
            [],
            "managed Docker actions must use v4 (Node 24 runtime):\n"
            + "\n".join(outdated),
        )


if __name__ == "__main__":
    unittest.main()
