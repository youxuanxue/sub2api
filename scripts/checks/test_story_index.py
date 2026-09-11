"""Story status is owned by the Story, never independently by its index."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("story_quality", ROOT / ".testing/user-stories/verify_quality.py")
QUALITY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(QUALITY)


class StoryIndexTest(unittest.TestCase):
    def check_index(self, text):
        with tempfile.TemporaryDirectory() as raw:
            path = Path(raw) / "index.md"
            path.write_text(text, encoding="utf-8")
            return QUALITY.verify_index(path, [{"id": "US-050", "status": "InTest",
                "path": ROOT / ".testing/user-stories/stories/example.md"}])

    def test_matching_status_with_endpoint_in_title(self):
        self.assertEqual([], self.check_index(
            "| US-050 | `/v1/messages` | InTest | `.testing/user-stories/stories/example.md` |"))

    def test_drift_missing_duplicate_and_wrong_target_fail(self):
        for row in [
            "| US-050 | Catalog | Done | `.testing/user-stories/stories/example.md` |",
            "| US-050 | Catalog | InTest | `wrong.md` |", "",
            "| US-051 | Catalog | InTest | `.testing/user-stories/stories/example.md` |",
        ]:
            with self.subTest(row=row):
                self.assertTrue(self.check_index(row))
        row = "| US-050 | Catalog | InTest | `.testing/user-stories/stories/example.md` |"
        self.assertTrue(self.check_index(row + "\n" + row))


if __name__ == "__main__":
    unittest.main()
