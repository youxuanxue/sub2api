#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("run_kiro_reauth_flow.py")
SPEC = importlib.util.spec_from_file_location("run_kiro_reauth_flow", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
mod = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(mod)


class RunKiroReauthFlowTest(unittest.TestCase):
    def test_normalize_edge_id_accepts_operator_labels(self) -> None:
        self.assertEqual(mod.normalize_edge_id("us6"), "us6")
        self.assertEqual(mod.normalize_edge_id("edge-us6"), "us6")
        self.assertEqual(mod.normalize_edge_id("edge:us6"), "us6")

    def test_default_admin_password_file_prefers_normalized_name(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp)
            key_dir = home / "Codes" / "keys"
            key_dir.mkdir(parents=True)
            normalized = key_dir / "tokenkey-us6-admin-password.txt"
            legacy = key_dir / "tokenkey-edge-us6-admin-password.txt"
            normalized.write_text("password", encoding="utf-8")
            legacy.write_text("password", encoding="utf-8")

            with mock.patch.object(mod.Path, "home", return_value=home):
                self.assertEqual(mod.default_admin_password_file("us6", ""), str(normalized))

    def test_default_admin_password_file_keeps_explicit_path(self) -> None:
        self.assertEqual(
            mod.default_admin_password_file("us6", "/tmp/custom-password.txt"),
            "/tmp/custom-password.txt",
        )


if __name__ == "__main__":
    unittest.main()
