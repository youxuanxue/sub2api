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
    with tempfile.NamedTemporaryFile("w", suffix="-ls.json", delete=False) as ls_fh:
        json.dump({"targets": {}}, ls_fh)
        ls_path = ls_fh.name
    try:
        return subprocess.run(
            [
                sys.executable,
                str(_SCRIPT),
                "--lightsail-matrix",
                ls_path,
                "--matrix",
                path,
                *args,
            ],
            capture_output=True,
            text=True,
            check=False,
        )
    finally:
        pathlib.Path(path).unlink(missing_ok=True)
        pathlib.Path(ls_path).unlink(missing_ok=True)


class ListDeployableTest(unittest.TestCase):
    MATRIX = {
        "default_profile": "edge-minimal",
        "max_monthly_budget_usd": 16,
        "targets": {
            "us1": {"deployable": True,  "region": "x", "domain": "x", "stack": "x",
                    "instance_type": "x", "root_volume_gib": 1, "data_volume_gib": 1,
                    "swap_gib": 1, "snapshot_schedule": "x", "monthly_budget_usd": 1,
                    "ssm_prefix": "/x", "profile": "edge-minimal"},
            "uk1": {"deployable": True,  "region": "x", "domain": "x", "stack": "x",
                    "instance_type": "x", "root_volume_gib": 1, "data_volume_gib": 1,
                    "swap_gib": 1, "snapshot_schedule": "x", "monthly_budget_usd": 1,
                    "ssm_prefix": "/x", "profile": "edge-minimal"},
            "fra1": {"deployable": False, "region": "x", "domain": "x", "stack": "x",
                     "instance_type": "x", "root_volume_gib": 1, "data_volume_gib": 1,
                     "swap_gib": 1, "snapshot_schedule": "x", "monthly_budget_usd": 1,
                     "ssm_prefix": "/x", "profile": "edge-minimal"},
        },
    }

    def test_lists_only_deployable_sorted(self) -> None:
        proc = _run_with_matrix(self.MATRIX, "--list-deployable")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        # Output: one id per line, sorted ascending
        self.assertEqual(proc.stdout.splitlines(), ["uk1", "us1"])

    def test_mutually_exclusive_with_edge_id(self) -> None:
        proc = _run_with_matrix(self.MATRIX, "--list-deployable", "--edge-id", "us1")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("mutually exclusive", proc.stderr)

    def test_empty_matrix_exits_zero(self) -> None:
        proc = _run_with_matrix({"targets": {}}, "--list-deployable")
        self.assertEqual(proc.returncode, 0)
        self.assertEqual(proc.stdout, "")


if __name__ == "__main__":
    unittest.main()
