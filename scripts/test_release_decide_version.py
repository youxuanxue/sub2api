#!/usr/bin/env python3
"""Unit tests for scripts/release-decide-version.sh — verify the 3-state
decision matrix in an isolated temp repo (fake origin + tags) so the test
does not depend on real network or the real repo state.

stdlib-only.
"""
from __future__ import annotations

import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "release-decide-version.sh"


def _clean_env() -> dict[str, str]:
    return {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}


def _git(cwd: pathlib.Path, *args: str, check: bool = True) -> str:
    proc = subprocess.run(
        ["git", *args], cwd=cwd, env=_clean_env(),
        capture_output=True, text=True, check=check,
    )
    return proc.stdout


class ReleaseDecideVersionTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        tmp = pathlib.Path(self._tmp.name)
        self.origin = tmp / "origin.git"
        self.repo = tmp / "repo"
        # Bare origin
        subprocess.run(
            ["git", "init", "--bare", "-q", "-b", "main", str(self.origin)],
            env=_clean_env(), check=True,
        )
        # Working repo
        self.repo.mkdir()
        _git(self.repo, "init", "-q", "-b", "main")
        _git(self.repo, "config", "user.email", "test@example.com")
        _git(self.repo, "config", "user.name", "Test")
        _git(self.repo, "remote", "add", "origin", str(self.origin))
        # Put VERSION + script in place
        (self.repo / "backend/cmd/server").mkdir(parents=True)
        (self.repo / "backend/cmd/server/VERSION").write_text("1.0.0\n")
        (self.repo / "scripts").mkdir(parents=True)
        shutil.copy(_SCRIPT, self.repo / "scripts/release-decide-version.sh")
        (self.repo / "scripts/release-decide-version.sh").chmod(0o755)
        _git(self.repo, "add", "-A")
        _git(self.repo, "commit", "-q", "-m", "base")
        _git(self.repo, "push", "-q", "origin", "main")

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _run(self) -> dict:
        proc = subprocess.run(
            ["bash", "scripts/release-decide-version.sh"],
            cwd=self.repo, env=_clean_env(),
            capture_output=True, text=True, check=False,
        )
        if proc.returncode != 0:
            raise AssertionError(
                f"script exit {proc.returncode}\nstdout:{proc.stdout}\nstderr:{proc.stderr}"
            )
        out: dict[str, str] = {}
        for line in proc.stdout.splitlines():
            if "=" in line:
                k, v = line.split("=", 1)
                out[k] = v
        return out

    def test_tag_only_when_tag_missing(self) -> None:
        out = self._run()
        self.assertEqual(out["action"], "tag-only")
        self.assertEqual(out["current_version"], "1.0.0")
        self.assertEqual(out["current_tag"], "v1.0.0")
        self.assertEqual(out["tag_on_origin"], "false")

    def test_skip_when_tag_at_head(self) -> None:
        # Tag the current commit on origin
        _git(self.repo, "tag", "v1.0.0")
        _git(self.repo, "push", "-q", "origin", "v1.0.0")
        out = self._run()
        self.assertEqual(out["action"], "skip-bump-skip-tag")
        self.assertEqual(out["tag_on_origin"], "true")
        self.assertEqual(out["main_synced_with_tag"], "true")

    def test_bump_when_tag_behind_main(self) -> None:
        # Tag current commit, then advance main, then expect bump-and-tag
        _git(self.repo, "tag", "v1.0.0")
        _git(self.repo, "push", "-q", "origin", "v1.0.0")
        (self.repo / "another.txt").write_text("hi\n")
        _git(self.repo, "add", "-A")
        _git(self.repo, "commit", "-q", "-m", "advance main")
        _git(self.repo, "push", "-q", "origin", "main")
        out = self._run()
        self.assertEqual(out["action"], "bump-and-tag")
        self.assertEqual(out["tag_on_origin"], "true")
        self.assertEqual(out["main_synced_with_tag"], "false")

    def test_emit_suggested_bump(self) -> None:
        _git(self.repo, "tag", "v1.0.0")
        _git(self.repo, "push", "-q", "origin", "v1.0.0")
        (self.repo / "another.txt").write_text("hi\n")
        _git(self.repo, "add", "-A")
        _git(self.repo, "commit", "-q", "-m", "advance main")
        _git(self.repo, "push", "-q", "origin", "main")
        proc = subprocess.run(
            ["bash", "scripts/release-decide-version.sh", "--emit-suggested-bump"],
            cwd=self.repo, env=_clean_env(),
            capture_output=True, text=True, check=True,
        )
        self.assertIn("suggested_next_version=1.0.1", proc.stdout)


if __name__ == "__main__":
    unittest.main()
