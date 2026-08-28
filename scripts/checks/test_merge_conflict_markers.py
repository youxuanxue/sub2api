from __future__ import annotations

import importlib.util
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("merge-conflict-markers.py")
SPEC = importlib.util.spec_from_file_location("merge_conflict_markers", SCRIPT)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class MergeConflictMarkersTest(unittest.TestCase):
    def test_accepts_decorative_equals_and_embedded_marker_text(self) -> None:
        root = Path(tempfile.mkdtemp())
        (root / "decorative.go").write_text(
            'package fixture\nconst banner = "================================"\nconst sample = "<<<<<<< HEAD"\n',
            encoding="utf-8",
        )
        self.assertEqual(MODULE.check(root), [])

    def test_rejects_real_line_level_markers(self) -> None:
        root = Path(tempfile.mkdtemp())
        (root / "broken.md").write_text(
            "before\n<<<<<<< Updated upstream\n=======\nafter\n>>>>>>> Stashed changes\n",
            encoding="utf-8",
        )
        errors = MODULE.check(root)
        self.assertEqual(len(errors), 2)
        self.assertTrue(all("broken.md" in error for error in errors))


if __name__ == "__main__":
    unittest.main()
