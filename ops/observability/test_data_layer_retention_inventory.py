#!/usr/bin/env python3
"""Safety contracts for the read-only retention inventory probe."""

from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import unittest


_DIR = pathlib.Path(__file__).resolve().parent
_PROBE = _DIR / "probe-data-layer-retention-inventory.sh"


class DataLayerRetentionInventorySafetyTest(unittest.TestCase):
    def test_shell_syntax(self) -> None:
        result = subprocess.run(
            ["bash", "-n", str(_PROBE)], capture_output=True, text=True, check=False
        )
        self.assertEqual(result.returncode, 0, msg=result.stderr)

    def test_probe_is_read_only_and_whitelist_bounded(self) -> None:
        body = _PROBE.read_text(encoding="utf-8")
        self.assertIn("default_transaction_read_only=on", body)
        self.assertIn("lock_timeout=100ms", body)
        self.assertIn("statement_timeout=20s", body)
        for table in ("usage_logs", "ops_system_logs", "ops_error_logs", "qa_records"):
            self.assertIn(f"'{table}'", body)
        for forbidden in (
            "DELETE FROM",
            "DROP TABLE",
            "DROP PARTITION",
            "TRUNCATE",
            "VACUUM",
            "ALTER TABLE",
            "INSERT INTO",
            "UPDATE ",
        ):
            self.assertNotIn(forbidden, body.upper())
        self.assertIn("RETENTIONSTATS", body)
        self.assertIn("RETENTIONUSAGE_EXACT", body)
        self.assertIn("RETENTIONPLAN", body)
        self.assertIn("RETPARTITION", body)
        self.assertIn("RETBLOB", body)

    def test_invalid_retention_input_fails_closed_without_docker(self) -> None:
        result = subprocess.run(
            ["bash", str(_PROBE)],
            env={"PATH": "/usr/bin:/bin", "USAGE_RETENTION_DAYS": "0"},
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0)
        self.assertIn('"inventory_probe_ok":false', result.stdout)
        self.assertIn("positive integers", result.stdout)

    def test_blob_scope_fails_closed_without_docker(self) -> None:
        result = subprocess.run(
            ["bash", str(_PROBE)],
            env={
                "PATH": "/usr/bin:/bin",
                "TOKENKEY_QA_BLOB_DIR": "/",
            },
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, msg=result.stderr)
        self.assertIn('"blob_inventory_ok":false', result.stdout)
        self.assertIn("outside the bounded data directory", result.stdout)

    def test_blob_command_failure_is_reported(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = pathlib.Path(temp_dir)
            bin_dir = root / "bin"
            data_dir = root / "data"
            blob_dir = data_dir / "app" / "qa_blobs"
            bin_dir.mkdir()
            blob_dir.mkdir(parents=True)

            stubs = {
                "docker": "#!/bin/sh\nexit 1\n",
                "date": "#!/bin/sh\nprintf '%s\\n' '2026-07-19T00:00:00Z'\n",
                "find": "#!/bin/sh\nexit 1\n",
            }
            for name, body in stubs.items():
                path = bin_dir / name
                path.write_text(body, encoding="utf-8")
                path.chmod(0o755)

            env = os.environ.copy()
            env["PATH"] = f"{bin_dir}:/usr/bin:/bin"
            env["TOKENKEY_DATA_DIR"] = str(data_dir)
            env["TOKENKEY_QA_BLOB_DIR"] = str(blob_dir)
            result = subprocess.run(
                ["bash", str(_PROBE)],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )

        self.assertEqual(result.returncode, 0, msg=result.stderr)
        self.assertIn('"blob_inventory_ok":false', result.stdout)
        self.assertIn("filesystem inventory command failed", result.stdout)


if __name__ == "__main__":
    unittest.main()
