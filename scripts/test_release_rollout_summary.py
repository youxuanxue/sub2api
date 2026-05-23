#!/usr/bin/env python3
"""Unit tests for scripts/release-rollout-summary.sh — verify each mode renders
the expected markdown sections against a fake repo. stdlib-only.
"""
from __future__ import annotations

import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "release-rollout-summary.sh"


def _scrubbed_env() -> dict:
    env = dict(os.environ)
    for key in ("GIT_DIR", "GIT_INDEX_FILE", "GIT_WORK_TREE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR"):
        env.pop(key, None)
    return env


def _git(cwd: pathlib.Path, *args: str, check: bool = True) -> str:
    proc = subprocess.run(
        ["git", *args], cwd=cwd, env=_scrubbed_env(),
        capture_output=True, text=True, check=check,
    )
    return proc.stdout


class ReleaseRolloutSummaryTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.repo = pathlib.Path(self._tmp.name) / "repo"
        self.repo.mkdir()
        _git(self.repo, "init", "-q", "-b", "main")
        _git(self.repo, "config", "user.email", "t@example.com")
        _git(self.repo, "config", "user.name", "T")
        (self.repo / "scripts").mkdir()
        shutil.copy(_SCRIPT, self.repo / "scripts/release-rollout-summary.sh")
        (self.repo / "scripts/release-rollout-summary.sh").chmod(0o755)
        (self.repo / "README.md").write_text("base\n")
        _git(self.repo, "add", "-A")
        _git(self.repo, "commit", "-q", "-m", "base")
        # tag v1.0.0
        _git(self.repo, "tag", "v1.0.0")
        # advance: a real fix commit + a skip-ci VERSION bump that must be filtered
        (self.repo / "backend").mkdir()
        (self.repo / "backend/foo.go").write_text("package main\n")
        _git(self.repo, "add", "-A")
        _git(self.repo, "commit", "-q", "-m", "feat: add foo")
        (self.repo / "backend/cmd/server").mkdir(parents=True)
        (self.repo / "backend/cmd/server/VERSION").write_text("1.0.1\n")
        _git(self.repo, "add", "-A")
        _git(self.repo, "commit", "-q", "-m", "chore: bump VERSION to 1.0.1 [skip ci]")
        _git(self.repo, "tag", "v1.0.1")

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _run(self, *args: str) -> str:
        proc = subprocess.run(
            ["bash", "scripts/release-rollout-summary.sh", *args],
            cwd=self.repo, env=_scrubbed_env(),
            capture_output=True, text=True, check=True,
        )
        return proc.stdout

    def test_release_mode_filters_version_bump_and_skip_ci(self) -> None:
        out = self._run("--mode", "release")
        self.assertIn("v1.0.0", out)
        self.assertIn("v1.0.1", out)
        self.assertIn("feat: add foo", out)
        # The VERSION bump commit should be filtered out
        self.assertNotIn("bump VERSION", out)
        self.assertNotIn("[skip ci]", out)

    def test_release_mode_has_required_sections(self) -> None:
        out = self._run("--mode", "release")
        for section in (
            "## Summary (mode=release)",
            "### Commits",
            "### Top changed files (backend/, frontend/src/)",
            "### Sentinel changes",
            "### Upstream file deletions (backend/)",
        ):
            self.assertIn(section, out, f"missing section: {section}")

    def test_local_mode_uses_HEAD(self) -> None:
        out = self._run("--mode", "local")
        # HEAD_REF is "HEAD" in local mode; should appear in Range line
        self.assertIn("`HEAD`", out)

    def test_deterministic(self) -> None:
        a = self._run("--mode", "release")
        b = self._run("--mode", "release")
        self.assertEqual(a, b)

    def test_invalid_mode_rejected(self) -> None:
        proc = subprocess.run(
            ["bash", "scripts/release-rollout-summary.sh", "--mode", "bogus"],
            cwd=self.repo, env=_scrubbed_env(),
            capture_output=True, text=True, check=False,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("--mode must be", proc.stderr)


if __name__ == "__main__":
    unittest.main()
