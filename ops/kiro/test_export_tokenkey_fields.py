#!/usr/bin/env python3
from __future__ import annotations

import subprocess
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("export-tokenkey-fields.sh")


class ExportTokenKeyFieldsTest(unittest.TestCase):
    def test_script_is_operator_facing_and_self_contained(self) -> None:
        text = SCRIPT.read_text(encoding="utf-8")
        self.assertIn("kiro-auth-token.json", text)
        self.assertIn("clientId", text)
        self.assertIn("clientSecret", text)
        self.assertIn("chmod(out_path, 0o600)", text)
        self.assertNotIn(".cursor/skills", text)

    def test_script_syntax(self) -> None:
        proc = subprocess.run(
            ["bash", "-n", str(SCRIPT)],
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_output_template_contains_operator_fields(self) -> None:
        text = SCRIPT.read_text(encoding="utf-8")
        for label in (
            "Access Token:",
            "Refresh Token:",
            "Region:",
            "认证方式:",
            "Client ID:",
            "Client Secret:",
            "接受 Kiro 服务条款: 勾选",
        ):
            self.assertIn(label, text)


if __name__ == "__main__":
    unittest.main()
