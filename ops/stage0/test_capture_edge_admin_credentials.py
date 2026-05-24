#!/usr/bin/env python3
"""Smoke tests for ops/stage0/capture-edge-admin-credentials.sh — validation only.

Real execution needs SSM access to a freshly-provisioned edge. Here we verify:
  - bash -n syntax check
  - --help exits 0
  - Invalid edge id format is rejected before any AWS call

stdlib-only.
"""
from __future__ import annotations

import pathlib
import subprocess
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "capture-edge-admin-credentials.sh"


class CaptureEdgeAdminCredentialsTest(unittest.TestCase):
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
        self.assertIn("capture-edge-admin-credentials.sh", proc.stdout)

    def test_missing_arg_shows_usage(self) -> None:
        proc = subprocess.run(
            ["bash", str(_SCRIPT)],
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(proc.returncode, 1)

    def test_invalid_edge_id_rejected(self) -> None:
        proc = subprocess.run(
            ["bash", str(_SCRIPT), "INVALID_ID"],
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(proc.returncode, 1)
        self.assertIn("invalid edge id", proc.stderr)


if __name__ == "__main__":
    unittest.main()
