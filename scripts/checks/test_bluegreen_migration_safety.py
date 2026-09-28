"""Focused tests for the blue/green migration safety gate."""

from __future__ import annotations

import importlib.util
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "bluegreen_migration_safety", ROOT / "scripts/checks/bluegreen-migration-safety.py"
)
assert SPEC and SPEC.loader
CHECK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECK)


class BluegreenMigrationSafetyTests(unittest.TestCase):
    def _git(self, root: Path, *args: str) -> None:
        subprocess.run(
            ["git", "-C", str(root), *args], check=True, capture_output=True
        )

    def _init_repo(self, root: Path) -> None:
        self._git(root, "init", "--initial-branch=main")
        self._git(root, "config", "user.email", "test@example.com")
        self._git(root, "config", "user.name", "test")
        migrations = root / "backend" / "migrations"
        migrations.mkdir(parents=True)
        (migrations / "001_base.sql").write_text("SELECT 1;\n")
        self._git(root, "add", "-A")
        self._git(root, "commit", "-m", "base")

    def test_allowlist_shrink_is_rejected(self) -> None:
        old = "ALTER TABLE users ADD CONSTRAINT users_platform_check CHECK (platform IN ('legacy', 'new'));"
        new = "ALTER TABLE users ADD CONSTRAINT users_platform_check CHECK (platform IN ('legacy'));"
        failures = CHECK.find_constraint_allowlist_shrinks(
            [("001_old.sql", old), ("002_new.sql", new)]
        )
        self.assertEqual(len(failures), 1)
        self.assertIn("removes values ['new']", failures[0])

    def test_changed_migration_is_checked_against_historical_union(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            migrations = root / "backend" / "migrations"
            migrations.mkdir(parents=True)
            historical = migrations / "tk_083_history.sql"
            current = migrations / "239_current.sql"
            historical.write_text(
                "ALTER TABLE user_platform_quotas ADD CONSTRAINT user_platform_quotas_platform_check "
                "CHECK (platform IN ('newapi', 'kiro'));\n"
            )
            current.write_text(
                "ALTER TABLE user_platform_quotas ADD CONSTRAINT user_platform_quotas_platform_check "
                "CHECK (platform IN ('newapi', 'kiro', 'opencode_go'));\n"
            )
            self.assertEqual(CHECK.constraint_allowlist_history(root, [current]), [])

            current.write_text(
                "ALTER TABLE user_platform_quotas ADD CONSTRAINT user_platform_quotas_platform_check "
                "CHECK (platform IN ('opencode_go'));\n"
            )
            failures = CHECK.constraint_allowlist_history(root, [current])
            self.assertEqual(len(failures), 1)
            self.assertIn("omits historical values ['kiro', 'newapi']", failures[0])

    def test_unchanged_historical_migration_is_not_reported(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            migrations = root / "backend" / "migrations"
            migrations.mkdir(parents=True)
            old = migrations / "001_old.sql"
            current = migrations / "002_current.sql"
            old.write_text(
                "ALTER TABLE users ADD CONSTRAINT users_platform_check CHECK (platform IN ('legacy', 'new'));\n"
            )
            current.write_text(
                "ALTER TABLE users ADD CONSTRAINT users_platform_check CHECK (platform IN ('legacy'));\n"
            )
            self.assertEqual(CHECK.constraint_allowlist_history(root, [old]), [])
            failures = CHECK.constraint_allowlist_history(root, [current])
            self.assertEqual(len(failures), 1)
            self.assertIn("omits historical values ['new']", failures[0])

    def test_staged_new_migration_is_visible_before_commit(self) -> None:
        """The whole point of the index diff: catch a brand-new staged migration.

        Diffing base..HEAD reported 0 changed files for a staged-only SQL file,
        so pre-commit passed even with the ack comment deleted.
        """
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._init_repo(root)
            self._git(root, "checkout", "-b", "feature")
            new = root / "backend" / "migrations" / "002_drop.sql"
            new.write_text("ALTER TABLE t DROP COLUMN c;\n")
            self._git(root, "add", str(new.relative_to(root)))
            names = [p.name for p in CHECK.changed_migrations("main", "HEAD", root=root)]
            self.assertEqual(names, ["002_drop.sql"])

    def test_commit_range_still_used_for_non_head(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._init_repo(root)
            self._git(root, "checkout", "-b", "feature")
            new = root / "backend" / "migrations" / "002_drop.sql"
            new.write_text("ALTER TABLE t DROP COLUMN c;\n")
            self._git(root, "add", "-A")
            self._git(root, "commit", "-m", "add migration")
            head = subprocess.run(
                ["git", "-C", str(root), "rev-parse", "HEAD"],
                check=True,
                capture_output=True,
                text=True,
            ).stdout.strip()
            # Staged-only noise after the commit must not appear when head is a SHA.
            noise = root / "backend" / "migrations" / "003_noise.sql"
            noise.write_text("SELECT 1;\n")
            self._git(root, "add", str(noise.relative_to(root)))
            names = [p.name for p in CHECK.changed_migrations("main", head, root=root)]
            self.assertEqual(names, ["002_drop.sql"])


if __name__ == "__main__":
    unittest.main()
