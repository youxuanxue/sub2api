#!/usr/bin/env python3
"""Behavior checks for check_prod_fleet_matrix.py (no live hosts)."""
from __future__ import annotations

import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent
MOD_PATH = ROOT / "check_prod_fleet_matrix.py"


def load_module():
    spec = importlib.util.spec_from_file_location("check_prod_fleet_matrix", MOD_PATH)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


class ProdFleetMatrixCheckTest(unittest.TestCase):
    def test_live_matrix_passes(self):
        mod = load_module()
        self.assertEqual(mod.main(), 0)

    def _with_matrix(self, mod, data):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "matrix.json"
            path.write_text(json.dumps(data))
            original = mod.MATRIX
            try:
                mod.MATRIX = path
                return mod.main()
            finally:
                mod.MATRIX = original

    def test_missing_entry_fails(self):
        mod = load_module()
        data = json.loads((ROOT / "candidate-usw-20261022.json").read_text())
        del data["entry"]
        self.assertEqual(self._with_matrix(mod, data), 1)

    def test_missing_layout_fails(self):
        mod = load_module()
        data = json.loads((ROOT / "candidate-usw-20261022.json").read_text())
        del data["layout"]
        self.assertEqual(self._with_matrix(mod, data), 1)

    def test_cand_8g_memory_fails(self):
        mod = load_module()
        data = json.loads((ROOT / "candidate-usw-20261022.json").read_text())
        for app in data["apps"]:
            if app["name"] == "app-cand":
                app["memory_limit"] = "8g"
        self.assertEqual(self._with_matrix(mod, data), 1)

    def test_missing_memory_limit_fails(self):
        mod = load_module()
        data = json.loads((ROOT / "candidate-usw-20261022.json").read_text())
        del data["apps"][0]["memory_limit"]
        self.assertEqual(self._with_matrix(mod, data), 1)


if __name__ == "__main__":
    unittest.main()
