"""Go sentinel anchors must reject comments masquerading as live hooks."""
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location(
    "gateway_sentinels", Path(__file__).parent / "sentinels/check-gateway-tk.py"
)
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


class CodeAnchorTests(unittest.TestCase):
    def test_code_anchor_requires_live_call(self):
        needle = "settle(input.HoldID)"
        entry = {"path": "hook.go", "must_contain": [needle], "code_only": True}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for source, accepted in [
                ("func run() { settle(input.HoldID) }", True),
                ("// settle(input.HoldID)", False),
                ("/* settle(input.HoldID) */", False),
                ('var example = "settle(input.HoldID)"', False),
                ("var example = `settle(input.HoldID)`", False),
            ]:
                with self.subTest(source=source):
                    (root / "hook.go").write_text(source)
                    with patch.object(checker, "REPO_ROOT", root), patch.object(checker, "_CONTENT_CACHE", {}):
                        ok, failures = checker.check_sentinel(entry)
                    self.assertEqual(ok, accepted, failures)
                    if not accepted:
                        self.assertIn("missing literal", failures[0])


if __name__ == "__main__":
    unittest.main()
