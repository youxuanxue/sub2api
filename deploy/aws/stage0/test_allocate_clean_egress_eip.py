import json
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
ALLOCATE = REPO_ROOT / "deploy" / "aws" / "stage0" / "allocate-clean-egress-eip.py"
REGISTRY = REPO_ROOT / "deploy" / "aws" / "stage0" / "edge-polluted-ips.json"


def load_module():
    import importlib.util

    spec = importlib.util.spec_from_file_location("allocate_clean_egress_eip", ALLOCATE)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


class LoadExcludedIPsTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.mod = load_module()

    def test_live_registry_lists_polluted_ips_only(self):
        data = json.loads(REGISTRY.read_text(encoding="utf-8"))
        polluted_ips = {e["ip"] for e in data["polluted"]}
        self.assertIn("3.9.160.161", polluted_ips)
        self.assertIn("35.177.124.150", polluted_ips)
        self.assertNotIn("retired_excluded", data)

    def test_load_excluded_ips_for_region(self):
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as fh:
            json.dump(
                {"polluted": [{"ip": "1.2.3.4", "region": "eu-west-2"}]},
                fh,
            )
            path = Path(fh.name)
        try:
            excluded = self.mod.load_excluded_ips("eu-west-2", registry_path=path)
            self.assertEqual(excluded, {"1.2.3.4"})
            self.assertEqual(self.mod.load_excluded_ips("us-west-2", registry_path=path), set())
        finally:
            path.unlink(missing_ok=True)


if __name__ == "__main__":
    unittest.main()
