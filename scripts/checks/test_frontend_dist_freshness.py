from __future__ import annotations

import importlib.util
import pathlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = pathlib.Path(__file__).with_name("frontend-dist-freshness.py")
SPEC = importlib.util.spec_from_file_location("frontend_dist_freshness", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class FrontendDistFreshnessTest(unittest.TestCase):
    def test_new_source_is_hashed_before_staging_and_ignored_outputs_are_excluded(self) -> None:
        with tempfile.TemporaryDirectory(prefix="frontend-dist-freshness-") as temp_dir:
            root = pathlib.Path(temp_dir)

            def git(*args: str) -> None:
                subprocess.run(["git", *args], cwd=root, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

            git("init", "-q")
            (root / ".gitignore").write_text((MODULE.REPO_ROOT / ".gitignore").read_text(encoding="utf-8"), encoding="utf-8")
            source = root / "frontend/src/live.ts"
            source.parent.mkdir(parents=True)
            source.write_text("export const live = true\n", encoding="utf-8")
            git("add", ".gitignore", "frontend")
            with patch.object(MODULE, "REPO_ROOT", root):
                original = MODULE.compute_digest()
                component = source.with_name("new\ncomponent.vue")
                component.write_text("<template>New component</template>\n", encoding="utf-8")
                ignored = root / "frontend/.cache/output.ts"
                ignored.parent.mkdir()
                ignored.write_text("generated\n", encoding="utf-8")
                source.with_name("live.spec.ts").write_text("test only\n", encoding="utf-8")
                for name in ("playwright.config.ts", "playwright.candidate.config.ts"):
                    (root / "frontend" / name).write_text("test config only\n", encoding="utf-8")
                before_staging = MODULE.compute_digest()
                self.assertNotEqual(original[0], before_staging[0])
                self.assertEqual(before_staging[1], 2)
                git("add", "frontend")
                self.assertEqual(MODULE.compute_digest(), before_staging)

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
