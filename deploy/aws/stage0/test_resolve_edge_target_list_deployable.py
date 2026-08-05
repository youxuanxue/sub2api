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
_REAL_MATRIX = pathlib.Path(__file__).resolve().parent / "edge-targets.json"


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
        "targets": {
            "us1": {"deployable": True,  "region": "x", "domain": "x", "stack": "x",
                    "instance_type": "t4g.small", "root_volume_gib": 20, "data_volume_gib": 20,
                    "swap_gib": 2, "snapshot_schedule": "daily",
                    "ssm_prefix": "/x", "profile": "edge-minimal"},
            "uk1": {"deployable": True,  "region": "x", "domain": "x", "stack": "x",
                    "instance_type": "t4g.small", "root_volume_gib": 20, "data_volume_gib": 20,
                    "swap_gib": 2, "snapshot_schedule": "daily",
                    "ssm_prefix": "/x", "profile": "edge-minimal"},
            "fra1": {"deployable": False, "region": "x", "domain": "x", "stack": "x",
                     "instance_type": "t4g.small", "root_volume_gib": 20, "data_volume_gib": 20,
                     "swap_gib": 2, "snapshot_schedule": "daily",
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

    def test_migration_candidate_requires_explicit_opt_in(self) -> None:
        matrix = json.loads(json.dumps(self.MATRIX))
        matrix["targets"]["fra1"]["migration_candidate"] = True
        proc = _run_with_matrix(matrix, "--edge-id", "fra1", "--confirm-stack", "x")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("--allow-migration-candidate", proc.stderr)

    def test_explicit_migration_candidate_resolves(self) -> None:
        matrix = json.loads(json.dumps(self.MATRIX))
        matrix["targets"]["fra1"]["migration_candidate"] = True
        proc = _run_with_matrix(
            matrix,
            "--edge-id", "fra1",
            "--confirm-stack", "x",
            "--allow-migration-candidate",
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("migration_candidate=true", proc.stdout)

    def test_ordinary_planned_target_cannot_use_candidate_opt_in(self) -> None:
        proc = _run_with_matrix(
            self.MATRIX,
            "--edge-id", "fra1",
            "--confirm-stack", "x",
            "--allow-migration-candidate",
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("not a migration candidate", proc.stderr)

    def test_real_candidates_have_fixed_capacity_and_no_budget_fields(self) -> None:
        matrix = json.loads(_REAL_MATRIX.read_text(encoding="utf-8"))
        per_edge_budget = "monthly" + "_budget_usd"
        self.assertNotIn("max_" + per_edge_budget, matrix)
        self.assertNotIn("max_fleet_" + per_edge_budget, matrix)
        expected_regions = {
            "us3": "us-east-2",
            "us4": "us-west-2",
            "us5": "us-west-2",
            "us6": "us-east-2",
        }
        for edge_id, target in matrix["targets"].items():
            with self.subTest(edge_id=edge_id):
                self.assertEqual(
                    ("t4g.small", 20, 20, 2, "daily"),
                    (
                        target["instance_type"],
                        target["root_volume_gib"],
                        target["data_volume_gib"],
                        target["swap_gib"],
                        target["snapshot_schedule"],
                    ),
                )
                self.assertNotIn(per_edge_budget, target)
                proc = _run_with_matrix(
                    matrix,
                    "--edge-id", edge_id,
                    "--confirm-stack", f"tokenkey-edge-{edge_id}-stage0",
                    "--allow-migration-candidate",
                )
                self.assertEqual(proc.returncode, 0, proc.stderr)
                outputs = dict(line.split("=", 1) for line in proc.stdout.splitlines())
                self.assertEqual(outputs["deployable"], "false")
                self.assertEqual(outputs["migration_candidate"], "true")
                self.assertEqual(outputs["region"], expected_regions[edge_id])
                self.assertEqual(outputs["instance_type"], "t4g.small")
                self.assertEqual(outputs["root_volume_gib"], "20")
                self.assertEqual(outputs["data_volume_gib"], "20")
                self.assertEqual(outputs["swap_gib"], "2")
                self.assertEqual(outputs["snapshot_schedule"], "daily")
                self.assertNotIn(per_edge_budget, outputs)
                self.assertEqual(outputs["ssm_prefix"], f"/tokenkey/edge/{edge_id}")

    def test_capacity_drift_is_rejected_per_field(self) -> None:
        drift_cases = {
            "instance_type": "t4g.medium",
            "root_volume_gib": 21,
            "data_volume_gib": 21,
            "swap_gib": 4,
            "snapshot_schedule": "hourly",
        }
        for field, bad_value in drift_cases.items():
            with self.subTest(field=field):
                matrix = json.loads(_REAL_MATRIX.read_text(encoding="utf-8"))
                matrix["targets"]["us3"][field] = bad_value
                proc = _run_with_matrix(
                    matrix,
                    "--edge-id", "us3",
                    "--confirm-stack", "tokenkey-edge-us3-stage0",
                    "--allow-migration-candidate",
                )
                self.assertNotEqual(proc.returncode, 0)
                self.assertIn(field, proc.stderr)

    def test_real_migration_candidates_are_not_deployable_without_lightsail(self) -> None:
        matrix = json.loads(_REAL_MATRIX.read_text(encoding="utf-8"))
        proc = _run_with_matrix(matrix, "--list-deployable")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(proc.stdout, "")


if __name__ == "__main__":
    unittest.main()
