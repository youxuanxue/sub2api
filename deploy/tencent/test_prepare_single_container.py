#!/usr/bin/env python3
"""Dry-run behavior for prepare_single_container.py (no docker, no live hosts)."""
from __future__ import annotations

import importlib.util
import contextlib
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent
MOD_PATH = ROOT / "prepare_single_container.py"


def load_module():
    spec = importlib.util.spec_from_file_location("prepare_single_container", MOD_PATH)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


class PrepareSingleContainerTest(unittest.TestCase):
    def test_dry_run_renders_prod_node_bind_without_writing_root(self):
        mod = load_module()
        with tempfile.TemporaryDirectory() as tmp:
            # Point ROOT at a disposable path so a mistaken --apply cannot touch
            # a real /var/lib/tokenkey during unit tests.
            original = mod.ROOT
            try:
                mod.ROOT = Path(tmp) / "tokenkey"
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    code = mod.main(["--node", "app-cand", "--dry-run"])
                self.assertEqual(code, 0)
                plan = json.loads(output.getvalue())
                self.assertEqual(plan["node"], "app-cand")
                self.assertEqual(plan["traffic_impact"].split()[0], "none")
                self.assertFalse(plan["apply"])
                self.assertEqual(plan["private_ip"], "10.4.0.9")
                self.assertFalse(mod.ROOT.exists())
            finally:
                mod.ROOT = original

    def test_unknown_node_is_refused(self):
        with self.assertRaises(SystemExit):
            load_module().main(["--node", "app-x", "--dry-run"])


if __name__ == "__main__":
    unittest.main()
