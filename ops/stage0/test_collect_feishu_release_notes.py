#!/usr/bin/env python3
"""Shallow-clone fixtures for Feishu rollout note collection.

Reproduces the deploy-stage0 fetch-depth:1 + tag-tip-only fetch shape that
emptied the 1.8.164 card, and asserts the collector recovers the real log.
"""
from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import unittest


SCRIPT = pathlib.Path(__file__).with_name("collect-feishu-release-notes.sh")


def _clean_env() -> dict[str, str]:
    return {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}


def _git(cwd: pathlib.Path, *args: str, check: bool = True) -> str:
    proc = subprocess.run(
        ["git", "-c", "protocol.file.allow=always", *args],
        cwd=cwd,
        env=_clean_env(),
        capture_output=True,
        text=True,
        check=check,
    )
    return proc.stdout


def _commit(repo: pathlib.Path, message: str, filename: str) -> None:
    (repo / filename).write_text(f"{message}\n", encoding="utf-8")
    _git(repo, "add", filename)
    _git(repo, "commit", "-q", "-m", message)


class CollectFeishuReleaseNotesTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.origin = pathlib.Path(self._tmp.name) / "origin"
        self.origin.mkdir()
        _git(self.origin, "init", "-q", "-b", "main")
        _git(self.origin, "config", "user.email", "t@example.com")
        _git(self.origin, "config", "user.name", "T")
        # Enough history that a depth=1 clone cannot walk v1.0.0..v1.0.1.
        _commit(self.origin, "base", "README.md")
        _commit(self.origin, "fix: alpha before first tag", "alpha.txt")
        _git(self.origin, "tag", "-a", "v1.0.0", "-m", "Release 1.0.0\n\n- fix: alpha before first tag\n")
        _commit(self.origin, "fix: failover Codex overloaded 400", "codex.txt")
        _commit(self.origin, "fix: stash encrypted_reasoning for QA", "openai.txt")
        _commit(self.origin, "fix: empty site_logo fallback", "logo.txt")
        _commit(self.origin, "chore: bump VERSION to 1.0.1", "VERSION")
        _git(
            self.origin,
            "tag",
            "-a",
            "v1.0.1",
            "-m",
            "Release 1.0.1\n\n"
            "- fix: failover Codex overloaded 400\n"
            "- fix: stash encrypted_reasoning for QA\n"
            "- fix: empty site_logo fallback\n",
        )

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _clone_depth1(self) -> pathlib.Path:
        dest = pathlib.Path(self._tmp.name) / "shallow"
        _git(
            pathlib.Path(self._tmp.name),
            "clone",
            "--depth=1",
            str(self.origin),
            str(dest),
        )
        return dest

    def _fetch_tag_tips_only(self, repo: pathlib.Path) -> None:
        # GitHub Actions fetch-depth:1 plus a tag-tip fetch leaves the clone
        # shallow. Local file remotes otherwise transfer the full tag history,
        # so pin --depth=1 to keep the commits between the tags missing.
        _git(
            repo,
            "fetch",
            "--depth=1",
            "origin",
            "refs/tags/v1.0.1:refs/tags/v1.0.1",
            "refs/tags/v1.0.0:refs/tags/v1.0.0",
            "--force",
        )

    def _collect(self, cwd: pathlib.Path, *args: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            ["bash", str(SCRIPT), *args],
            cwd=cwd,
            env=_clean_env(),
            capture_output=True,
            text=True,
            check=False,
        )

    def test_full_clone_lists_fixes_and_drops_version_bump(self) -> None:
        proc = self._collect(self.origin, "1.0.0", "1.0.1")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIn("failover Codex overloaded 400", proc.stdout)
        self.assertIn("stash encrypted_reasoning for QA", proc.stdout)
        self.assertIn("empty site_logo fallback", proc.stdout)
        self.assertNotIn("bump VERSION", proc.stdout)
        self.assertIn("source=git-log", proc.stderr)
        self.assertIn("lines=3", proc.stderr)

    def test_shallow_tag_tip_fetch_recovers_log_that_naive_git_log_drops(self) -> None:
        shallow = self._clone_depth1()
        self._fetch_tag_tips_only(shallow)

        naive = subprocess.run(
            ["git", "log", "--first-parent", "--pretty=format:- %s", "v1.0.0..v1.0.1"],
            cwd=shallow,
            env=_clean_env(),
            capture_output=True,
            text=True,
            check=False,
        )
        naive_notes = "\n".join(
            line
            for line in naive.stdout.splitlines()
            if "bump VERSION" not in line and "sync.version.file" not in line
        )
        self.assertNotIn("failover Codex overloaded 400", naive_notes)

        proc = self._collect(shallow, "v1.0.0", "v1.0.1")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIn("failover Codex overloaded 400", proc.stdout)
        self.assertIn("stash encrypted_reasoning for QA", proc.stdout)
        self.assertIn("empty site_logo fallback", proc.stdout)
        self.assertNotIn("bump VERSION", proc.stdout)
        self.assertIn("source=git-log", proc.stderr)

    def test_empty_range_falls_back_to_annotated_tag_body(self) -> None:
        # A second tag on the same commit: git log A..B is empty, tag body is not.
        _git(self.origin, "tag", "-a", "v1.0.2", "-m", "Release 1.0.2\n\n- fix: replayed from tag body\n")
        proc = self._collect(self.origin, "1.0.1", "1.0.2")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIn("replayed from tag body", proc.stdout)
        self.assertIn("source=tag-body", proc.stderr)
        self.assertNotIn("Release 1.0.2", proc.stdout)

    def test_empty_notes_warn_and_print_nothing(self) -> None:
        _git(self.origin, "tag", "v1.0.2")
        proc = self._collect(self.origin, "1.0.1", "1.0.2")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(proc.stdout, "")
        self.assertIn("empty notes", proc.stderr)
        self.assertIn("source=git-log", proc.stderr)

    def test_missing_args_are_a_usage_error(self) -> None:
        proc = self._collect(self.origin)
        self.assertEqual(proc.returncode, 2)
        self.assertIn("required", proc.stderr)

    def test_invalid_tag_is_a_usage_error(self) -> None:
        proc = self._collect(self.origin, "main", "1.0.1")
        self.assertEqual(proc.returncode, 2)
        self.assertIn("previous-tag must be a Stage0 release tag", proc.stderr)


if __name__ == "__main__":
    unittest.main()
