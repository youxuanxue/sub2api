#!/usr/bin/env python3
"""Behavior tests for the annotated release changelog generator."""
from __future__ import annotations

import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest


_SCRIPT = pathlib.Path(__file__).resolve().parent / "release-tag.sh"


def _clean_env() -> dict[str, str]:
    return {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}


def _git(cwd: pathlib.Path, *args: str) -> str:
    proc = subprocess.run(
        ["git", *args],
        cwd=cwd,
        env=_clean_env(),
        capture_output=True,
        text=True,
        check=True,
    )
    return proc.stdout.strip()


class ReleaseTagTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.repo = pathlib.Path(self._tmp.name) / "repo"
        self.repo.mkdir()
        _git(self.repo, "init", "-q", "-b", "main")
        _git(self.repo, "config", "user.email", "test@example.com")
        _git(self.repo, "config", "user.name", "Test")
        scripts = self.repo / "scripts"
        scripts.mkdir()
        shutil.copy(_SCRIPT, scripts / "release-tag.sh")
        (scripts / "release-tag.sh").chmod(0o755)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _commit(self, filename: str, content: str, subject: str) -> None:
        (self.repo / filename).write_text(content)
        _git(self.repo, "add", filename)
        _git(self.repo, "commit", "-q", "-m", subject)

    def test_changelog_ignores_closer_tag_from_merged_upstream_history(self) -> None:
        self._commit("root.txt", "root\n", "chore: initialize repository")
        root = _git(self.repo, "rev-parse", "HEAD")
        self._commit(
            "historical.txt",
            "already released\n",
            "feat: historical TokenKey feature",
        )
        _git(self.repo, "tag", "-a", "v1.8.101", "-m", "Release v1.8.101")

        _git(self.repo, "switch", "-q", "-c", "upstream", root)
        self._commit("upstream.txt", "upstream\n", "feat: upstream change")
        _git(self.repo, "tag", "-a", "v0.1.156", "-m", "Upstream v0.1.156")

        _git(self.repo, "switch", "-q", "main")
        self._commit(
            "premerge.txt",
            "current before upstream merge\n",
            "fix: current pre-merge TokenKey change",
        )
        _git(
            self.repo,
            "merge",
            "-q",
            "--no-ff",
            "upstream",
            "-m",
            "chore(upstream): merge upstream release",
        )
        self._commit("current.txt", "current\n", "feat: current TokenKey feature")

        self.assertEqual(
            _git(self.repo, "describe", "--tags", "--abbrev=0", "HEAD"),
            "v0.1.156",
            "fixture must reproduce the side-parent tag selection bug",
        )
        proc = subprocess.run(
            ["bash", "scripts/release-tag.sh", "v1.8.102", "--dry-run"],
            cwd=self.repo,
            env=_clean_env(),
            capture_output=True,
            text=True,
            check=False,
        )

        self.assertEqual(
            proc.returncode,
            0,
            f"stdout:{proc.stdout}\nstderr:{proc.stderr}",
        )
        self.assertIn("feat: current TokenKey feature", proc.stdout)
        self.assertIn("fix: current pre-merge TokenKey change", proc.stdout)
        self.assertIn("chore(upstream): merge upstream release", proc.stdout)
        self.assertNotIn("feat: historical TokenKey feature", proc.stdout)
        self.assertNotIn("feat: upstream change", proc.stdout)


if __name__ == "__main__":
    unittest.main()
