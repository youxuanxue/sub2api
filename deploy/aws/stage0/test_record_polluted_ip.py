import json
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
RECORD = REPO_ROOT / "deploy" / "aws" / "stage0" / "record-polluted-ip.py"


def load_module():
    import importlib.util

    spec = importlib.util.spec_from_file_location("record_polluted_ip", RECORD)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


class RecordPollutedIPTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.mod = load_module()

    def test_append_is_idempotent(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "edge-polluted-ips.json"
            path.write_text(
                json.dumps({"schema_version": 2, "polluted": []}),
                encoding="utf-8",
            )
            added = self.mod.append_entry(
                ip="18.135.59.111",
                region="eu-west-2",
                notes="Lightsail uk1 superseded",
                edge_id="uk1",
                platform="lightsail",
                registry_path=path,
            )
            self.assertTrue(added)
            again = self.mod.append_entry(
                ip="18.135.59.111",
                region="eu-west-2",
                notes="duplicate attempt",
                registry_path=path,
            )
            self.assertFalse(again)
            data = json.loads(path.read_text(encoding="utf-8"))
            self.assertEqual(len(data["polluted"]), 1)
            self.assertEqual(data["polluted"][0]["edge_id"], "uk1")

    def test_atomic_write_replaces_whole_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "edge-polluted-ips.json"
            path.write_text('{"schema_version":2,"polluted":[]}', encoding="utf-8")
            self.mod.append_entry(
                ip="1.2.3.4",
                region="us-east-1",
                notes="test",
                registry_path=path,
            )
            data = json.loads(path.read_text(encoding="utf-8"))
            self.assertEqual(data["polluted"][0]["ip"], "1.2.3.4")

    def test_is_excluded_lookup(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "edge-polluted-ips.json"
            path.write_text(
                json.dumps(
                    {
                        "polluted": [
                            {"ip": "3.9.160.161", "region": "eu-west-2", "notes": "x"}
                        ]
                    }
                ),
                encoding="utf-8",
            )
            data = self.mod.load_registry(path)
            excluded = self.mod.excluded_ips_for_region(data, "eu-west-2")
            self.assertIn("3.9.160.161", excluded)
            self.assertNotIn("3.9.160.161", self.mod.excluded_ips_for_region(data, "us-east-1"))


if __name__ == "__main__":
    unittest.main()
