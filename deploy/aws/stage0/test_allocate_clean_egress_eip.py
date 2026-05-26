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

    def test_live_registry_separates_pollution_from_retired_excluded(self):
        data = json.loads(REGISTRY.read_text(encoding="utf-8"))
        polluted_ips = {e["ip"] for e in data["polluted"]}
        retired_ips = {e["ip"] for e in data.get("retired_excluded", [])}
        self.assertNotIn("16.61.87.51", polluted_ips)
        self.assertIn("16.61.87.51", retired_ips)

    def test_load_excluded_ips_unions_both_lists_for_region(self):
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as fh:
            json.dump(
                {
                    "polluted": [{"ip": "1.2.3.4", "region": "eu-west-2"}],
                    "retired_excluded": [{"ip": "5.6.7.8", "region": "eu-west-2"}],
                },
                fh,
            )
            path = Path(fh.name)
        try:
            excluded = self.mod.load_excluded_ips("eu-west-2", registry_path=path)
            self.assertEqual(excluded, {"1.2.3.4", "5.6.7.8"})
            self.assertEqual(self.mod.load_excluded_ips("us-west-2", registry_path=path), set())
        finally:
            path.unlink(missing_ok=True)


if __name__ == "__main__":
    unittest.main()
