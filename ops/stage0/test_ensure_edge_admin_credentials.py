#!/usr/bin/env python3
"""Smoke tests for ops/stage0/ensure-edge-admin-credentials.sh — validation only."""
from __future__ import annotations

import pathlib
import subprocess
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "ensure-edge-admin-credentials.sh"


class EnsureEdgeAdminCredentialsTest(unittest.TestCase):
    def test_syntax_clean(self) -> None:
        proc = subprocess.run(
            ["bash", "-n", str(_SCRIPT)],
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)

    def test_help_exits_zero(self) -> None:
        proc = subprocess.run(
            ["bash", str(_SCRIPT), "--help"],
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(proc.returncode, 0)
        self.assertIn("ensure-edge-admin-credentials.sh", proc.stdout)
        self.assertIn("Never prints the password", proc.stdout)
        # prod is a supported target (capture misses -> falls back to reset).
        self.assertIn("prod", proc.stdout)

    def test_missing_arg_shows_usage(self) -> None:
        proc = subprocess.run(
            ["bash", str(_SCRIPT)],
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(proc.returncode, 1)


if __name__ == "__main__":
    unittest.main()
