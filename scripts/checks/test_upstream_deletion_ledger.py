#!/usr/bin/env python3
"""Tests for scripts/checks/upstream-deletion-ledger.py."""
from __future__ import annotations

import importlib.util
import pathlib
import subprocess
import tempfile
import unittest

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "upstream-deletion-ledger.py"
_spec = importlib.util.spec_from_file_location("upstream_deletion_ledger", _MOD_PATH)
udl = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(udl)

UPSTREAM_FILE = "backend/internal/handler/thing.go"


class UpstreamDeletionLedgerTest(unittest.TestCase):
    def _git(self, root: pathlib.Path, *args: str) -> None:
        subprocess.run(["git", "-C", str(root), *args], check=True, capture_output=True)

    def _init_upstream_base(self, root: pathlib.Path) -> None:
        """A one-commit upstream-main branch holding the file."""
        self._git(root, "init", "--initial-branch=upstream-main")
        self._git(root, "config", "user.email", "test@example.com")
        self._git(root, "config", "user.name", "test")
        target = root / UPSTREAM_FILE
        target.parent.mkdir(parents=True)
        target.write_text("package handler\n")
        self._git(root, "add", "-A")
        self._git(root, "commit", "-m", "upstream base")

    def _repo_with_deletion(self, root: pathlib.Path) -> None:
        """upstream-main branch holds the file; HEAD (tk) has deleted it."""
        self._init_upstream_base(root)
        self._git(root, "checkout", "-b", "tk")
        (root / UPSTREAM_FILE).unlink()
        self._git(root, "add", "-A")
        self._git(root, "commit", "-m", "tk deletes it")

    def test_deletion_in_head_is_reported(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            self._repo_with_deletion(root)
            self.assertEqual(
                udl.deleted_upstream_paths(root, "upstream-main"), [UPSTREAM_FILE]
            )

    def test_staged_deletion_is_reported_before_it_is_committed(self) -> None:
        """The whole point: catch the deletion in the commit that makes it.

        Diffing HEAD reported deletions one commit late — the removing commit
        passed and the next one failed, which is how an unledgered deletion
        shipped in 5729fda5f.
        """
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            self._init_upstream_base(root)
            self._git(root, "checkout", "-b", "tk")
            self._git(root, "rm", "-q", UPSTREAM_FILE)
            self.assertEqual(
                udl.deleted_upstream_paths(root, "upstream-main"), [UPSTREAM_FILE]
            )

    def test_staged_restore_clears_the_deletion(self) -> None:
        """§5.x's own remedy — restoring the file — must be committable."""
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            self._repo_with_deletion(root)
            (root / UPSTREAM_FILE).write_text("package handler\n")
            self._git(root, "add", UPSTREAM_FILE)
            self.assertEqual(udl.deleted_upstream_paths(root, "upstream-main"), [])

    def test_unstaged_restore_still_reports(self) -> None:
        """A file restored in the worktree but not staged is not in the commit."""
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            self._repo_with_deletion(root)
            (root / UPSTREAM_FILE).write_text("package handler\n")
            self.assertEqual(
                udl.deleted_upstream_paths(root, "upstream-main"), [UPSTREAM_FILE]
            )


if __name__ == "__main__":
    unittest.main()
