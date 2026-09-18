#!/usr/bin/env python3
"""Tests for scripts/checks/migration-immutability.py."""
from __future__ import annotations

import importlib.util
import pathlib
import subprocess
import tempfile
import unittest
from unittest import mock

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "migration-immutability.py"
_spec = importlib.util.spec_from_file_location("migration_immutability", _MOD_PATH)
mi = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(mi)


class MigrationImmutabilityTest(unittest.TestCase):
    def _init_repo(self, root: pathlib.Path) -> None:
        subprocess.run(["git", "init"], cwd=root, check=True, capture_output=True)
        subprocess.run(
            ["git", "config", "user.email", "test@example.com"],
            cwd=root,
            check=True,
            capture_output=True,
        )
        subprocess.run(
            ["git", "config", "user.name", "test"],
            cwd=root,
            check=True,
            capture_output=True,
        )

    def _commit_all(self, root: pathlib.Path, message: str) -> None:
        subprocess.run(["git", "add", "-A"], cwd=root, check=True, capture_output=True)
        subprocess.run(["git", "commit", "-m", message], cwd=root, check=True, capture_output=True)

    def test_modified_existing_migration_fails(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            mig_dir = root / "backend/migrations"
            mig_dir.mkdir(parents=True)
            path = mig_dir / "tk_001_seed.sql"
            path.write_text("SELECT 1;\n")
            self._init_repo(root)
            self._commit_all(root, "base")
            subprocess.run(["git", "tag", "v1.0.0"], cwd=root, check=True, capture_output=True)
            path.write_text("SELECT 2;\n")
            self._commit_all(root, "modify migration")
            with mock.patch.object(mi, "ROOT", root):
                violations = mi.scan("HEAD^", "HEAD")
            self.assertEqual(len(violations), 1)
            self.assertEqual(violations[0].kind, "modified")

    def test_restore_to_shipped_checksum_passes(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            mig_dir = root / "backend/migrations"
            mig_dir.mkdir(parents=True)
            path = mig_dir / "tk_001_seed.sql"
            original = "SELECT 1;\n"
            path.write_text(original)
            self._init_repo(root)
            self._commit_all(root, "base")
            subprocess.run(["git", "tag", "v1.0.0"], cwd=root, check=True, capture_output=True)
            path.write_text("SELECT 2;\n")
            self._commit_all(root, "bad edit")
            path.write_text(original)
            self._commit_all(root, "restore")
            with mock.patch.object(mi, "ROOT", root):
                violations = mi.scan("HEAD~2", "HEAD")
            self.assertEqual(violations, [])

    def test_added_migration_passes(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            mig_dir = root / "backend/migrations"
            mig_dir.mkdir(parents=True)
            (mig_dir / "tk_001_seed.sql").write_text("SELECT 1;\n")
            self._init_repo(root)
            self._commit_all(root, "base")
            (mig_dir / "tk_002_more.sql").write_text("SELECT 2;\n")
            self._commit_all(root, "add migration")
            with mock.patch.object(mi, "ROOT", root):
                violations = mi.scan("HEAD^", "HEAD")
            self.assertEqual(violations, [])

    def test_deleted_migration_fails(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            mig_dir = root / "backend/migrations"
            mig_dir.mkdir(parents=True)
            path = mig_dir / "tk_001_seed.sql"
            path.write_text("SELECT 1;\n")
            self._init_repo(root)
            self._commit_all(root, "base")
            path.unlink()
            self._commit_all(root, "delete migration")
            with mock.patch.object(mi, "ROOT", root):
                violations = mi.scan("HEAD^", "HEAD")
            self.assertEqual(len(violations), 1)
            self.assertEqual(violations[0].kind, "deleted")

    def test_pending_merge_checks_staged_migrations(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            self._init_repo(root)
            (root / "base.txt").write_text("base\n")
            self._commit_all(root, "base")
            with mock.patch.object(mi, "ROOT", root):
                self.assertEqual(mi.git("checkout", "-b", "incoming").returncode, 0)
                migration = root / "backend/migrations/tk_001_seed.sql"
                migration.parent.mkdir(parents=True)
                migration.write_text("SELECT 1;\n")
                self._commit_all(root, "incoming migration")
                self.assertEqual(mi.git("checkout", "-b", "feature", "HEAD^").returncode, 0)
                (root / "feature.txt").write_text("feature\n")
                self._commit_all(root, "feature")
                self.assertEqual(mi.git("merge", "--no-commit", "incoming").returncode, 0)
                self.assertEqual(mi.scan("incoming", "HEAD"), [])
                # An explicit historical ref must still check that exact tree.
                old_head = mi.git("rev-parse", "HEAD").stdout.strip()
                self.assertEqual([v.kind for v in mi.scan("incoming", old_head)], ["deleted"])
                migration.write_text("SELECT 2;\n")
                self.assertEqual(mi.git("add", "backend/migrations").returncode, 0)
                self.assertEqual([v.kind for v in mi.scan("incoming", "HEAD")], ["modified"])
                migration.unlink()
                self.assertEqual(mi.git("add", "backend/migrations").returncode, 0)
                self.assertEqual([v.kind for v in mi.scan("incoming", "HEAD")], ["deleted"])


if __name__ == "__main__":
    unittest.main()
