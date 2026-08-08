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
        for table in ("usage_logs", "ops_system_logs", "ops_error_logs"):
            self.assertIn(f"'{table}'", body)
        self.assertNotIn("qa_records", body)
        self.assertNotIn("QA_RETENTION_DAYS", body)
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
        self.assertIn("pg_partition_tree(to_regclass('usage_logs'))", body)
        self.assertNotIn("non-partitioned usage", body)
        self.assertNotIn("non-partitioned relation", body)
        self.assertNotIn("RETBLOB", body)
        self.assertIn('for value in "$USAGE_RETENTION_DAYS" "$OPS_RETENTION_DAYS"', body)

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


if __name__ == "__main__":
    unittest.main()
