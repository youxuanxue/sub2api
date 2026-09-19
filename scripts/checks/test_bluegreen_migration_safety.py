"""Focused tests for the blue/green migration safety gate."""

from __future__ import annotations

import importlib.util
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


if __name__ == "__main__":
    unittest.main()
