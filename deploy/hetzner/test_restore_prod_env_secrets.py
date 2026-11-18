"""Behavior tests for restore-prod-env-secrets.sh (closeout C4)."""

from __future__ import annotations

import os
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("restore-prod-env-secrets.sh")


class RestoreProdEnvSecretsTest(unittest.TestCase):
    def _valid(self, path: Path) -> None:
        path.write_text(
            "POSTGRES_PASSWORD=" + ("a" * 48) + "\n"
            "JWT_SECRET=" + ("b" * 64) + "\n"
            "TOTP_ENCRYPTION_KEY=" + ("c" * 64) + "\n",
            encoding="utf-8",
        )

    def test_reuses_existing_host_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "secrets.env"
            self._valid(out)
            proc = subprocess.run(
                ["bash", str(SCRIPT), "--output", str(out)],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertIn("already present on host", proc.stdout)

    def test_from_file_copies_validated_material(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            src = Path(tmp) / "src.env"
            out = Path(tmp) / "out.env"
            self._valid(src)
            proc = subprocess.run(
                ["bash", str(SCRIPT), "--output", str(out), "--from-file", str(src)],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertEqual(src.read_text(), out.read_text())
            self.assertIn("restored from --from-file", proc.stdout)

    def test_refuses_securestring_without_break_glass(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "missing.env"
            env = {**os.environ}
            env.pop("TOKENKEY_ALLOW_SECURESTRING_READ", None)
            proc = subprocess.run(
                [
                    "bash",
                    str(SCRIPT),
                    "--output",
                    str(out),
                    "--parameter",
                    "/tokenkey/prod/stage0/env-secrets-backup",
                ],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 45, proc.stderr)
            self.assertIn("SecureString is not the live SSOT", proc.stderr)
            self.assertFalse(out.exists())


if __name__ == "__main__":
    unittest.main()
