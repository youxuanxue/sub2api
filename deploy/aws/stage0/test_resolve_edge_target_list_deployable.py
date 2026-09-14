#!/usr/bin/env python3
"""Smoke test for the --list-deployable flag added to resolve-edge-target.py.

stdlib-only.
"""
from __future__ import annotations

import json
import pathlib
import subprocess
import sys
import tempfile
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "resolve-edge-target.py"


def _run_with_matrix(matrix: dict, *args: str) -> subprocess.CompletedProcess:
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as fh:
        json.dump(matrix, fh)
        path = fh.name
    try:
        return subprocess.run(
            [
                sys.executable,
                str(_SCRIPT),
                "--lightsail-matrix",
                path,
                *args,
            ],
            capture_output=True,
            text=True,
            check=False,
        )
    finally:
        pathlib.Path(path).unlink(missing_ok=True)


class ListDeployableTest(unittest.TestCase):
    MATRIX = {"targets": {
        "us1": {"deployable": True, "lightsail_region": "x", "ssm_prefix": "/us1"},
        "uk1": {"deployable": True, "lightsail_region": "x", "ssm_prefix": "/uk1"},
        "fra1": {"deployable": False, "lightsail_region": "x", "ssm_prefix": "/fra1"},
    }}

    def test_lists_only_deployable_sorted(self) -> None:
        proc = _run_with_matrix(self.MATRIX, "--list-deployable")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        # Output: one id per line, sorted ascending
        self.assertEqual(proc.stdout.splitlines(), ["uk1", "us1"])

    def test_mutually_exclusive_with_prod_ops_matrix(self) -> None:
        proc = _run_with_matrix(self.MATRIX, "--list-deployable", "--prod-ops-matrix")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("mutually exclusive", proc.stderr)

    def test_empty_matrix_exits_zero(self) -> None:
        proc = _run_with_matrix({"targets": {}}, "--list-deployable")
        self.assertEqual(proc.returncode, 0)
        self.assertEqual(proc.stdout, "")


if __name__ == "__main__":
    unittest.main()
