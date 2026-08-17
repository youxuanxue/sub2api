from __future__ import annotations

import importlib.util
import pathlib
import tempfile
import unittest


SCRIPT = pathlib.Path(__file__).with_name("frontend-dist-freshness.py")
SPEC = importlib.util.spec_from_file_location("frontend_dist_freshness", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class FrontendDistFreshnessTest(unittest.TestCase):
    def test_deleted_tracked_source_is_not_hashed(self) -> None:
        with tempfile.TemporaryDirectory(prefix="frontend-dist-freshness-") as temp_dir:
            root = pathlib.Path(temp_dir)
            existing = root / "frontend/src/api/live.ts"
            deleted = root / "frontend/src/api/deleted.ts"
            existing.parent.mkdir(parents=True)
            existing.write_text("export const live = true\n", encoding="utf-8")

            original_root = MODULE.REPO_ROOT
            try:
                MODULE.REPO_ROOT = root
                self.assertEqual(MODULE.filter_input_paths([deleted, existing]), [existing])
            finally:
                MODULE.REPO_ROOT = original_root


if __name__ == "__main__":
    unittest.main()
