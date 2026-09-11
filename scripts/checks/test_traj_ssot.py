"""Behavior regressions for the trajectory SSOT guard."""
import importlib.util
import tempfile
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("traj_ssot", Path(__file__).with_name("traj-ssot.py"))
assert spec is not None and spec.loader is not None
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


class TrajectorySSOTTest(unittest.TestCase):
    def test_reintroduced_projection_and_duplicate_owner_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ("AGENTS.md", "CLAUDE.md"):
                (root / name).write_text(checker.CONTRACT)
            package = root / "backend/internal/observability/trajectory"
            package.mkdir(parents=True)
            self.assertEqual(checker.check(root), [])
            (package / "projection_v2.go").write_text("package trajectory")
            self.assertIn("retired projector returned: projection_v2.go", checker.check(root))
            (root / "CLAUDE.md").write_text(checker.CONTRACT + " observability/trajectory/session.go")
            self.assertTrue(any("duplicate trajectory owner list" in f for f in checker.check(root)))

    def test_stale_script_contract_is_rejected_but_capture_helpers_are_allowed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ("AGENTS.md", "CLAUDE.md"):
                (root / name).write_text(checker.CONTRACT)
            (root / "ops").mkdir()
            source = root / "ops" / "export.py"
            source.write_text("schema = 'traj/pipeline/schemas/traj_v2.json'")
            self.assertTrue(any("retired trajectory contract" in f for f in checker.check(root)))
            source.write_text("# trajectory.WriteBlobFile remains a live evidence helper")
            self.assertEqual(checker.check(root), [])


if __name__ == "__main__":
    unittest.main()
