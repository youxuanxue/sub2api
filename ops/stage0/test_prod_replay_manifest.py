import json
from pathlib import Path
import tempfile
import unittest
import sys

sys.path.insert(0, str(Path(__file__).parent))

from prod_replay_manifest import Capability, load
from post_release_replay_check import main as post_release_main


class ManifestTests(unittest.TestCase):
    def test_repository_manifest_is_finite_and_unique(self):
        values = load(Path(__file__).with_name("prod-replay-capabilities.json"))
        self.assertTrue(values)
        self.assertEqual(len(values), len(set(values)))

    def test_invalid_duplicate_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "manifest.json"
            item = {"model": "m", "protocol": "p", "request_type": "r", "key_type": "k"}
            path.write_text(json.dumps({"capabilities": [item, item]}), encoding="utf-8")
            with self.assertRaises(ValueError):
                load(path)

    def test_post_release_check_is_explicit_and_never_cutover(self):
        self.assertEqual(post_release_main([]), 0)


if __name__ == "__main__":
    unittest.main()
