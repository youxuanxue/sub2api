"""Verify that --prod-ops-matrix surfaces deployable Lightsail edges alongside
EC2/CFN ones, with platform=lightsail and ssm_prefix set from the lightsail
matrix.

stdlib-only.
"""
from __future__ import annotations

import json
import pathlib
import subprocess
import sys
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
SCRIPT = REPO_ROOT / "deploy/aws/stage0/resolve-edge-target.py"
EC2_MATRIX = REPO_ROOT / "deploy/aws/stage0/edge-targets.json"
LIGHTSAIL_MATRIX = REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json"


def _run(*args: str) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(SCRIPT), "--matrix", str(EC2_MATRIX), *args],
        capture_output=True,
        text=True,
        check=False,
    )


class ProdOpsMatrixLightsailTests(unittest.TestCase):
    def test_all_selector_includes_lightsail_deployable_edges(self):
        ls = json.loads(LIGHTSAIL_MATRIX.read_text(encoding="utf-8"))
        deployable_ls = sorted(
            edge_id for edge_id, target in ls.get("targets", {}).items() if target.get("deployable")
        )
        proc = _run("--prod-ops-matrix", "--target-selector", "all")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        payload = json.loads(proc.stdout)
        include = payload["matrix"]["include"]
        target_ids = {item["target_id"]: item for item in include}
        for edge_id in deployable_ls:
            ls_id = f"edge-{edge_id}-ls"
            self.assertIn(ls_id, target_ids, f"missing lightsail edge in matrix: {ls_id}")
            self.assertEqual(target_ids[ls_id]["platform"], "lightsail")
            self.assertEqual(target_ids[ls_id]["target_kind"], "edge")
            self.assertEqual(target_ids[ls_id]["stack"], "")
            self.assertTrue(target_ids[ls_id]["ssm_prefix"].startswith("/tokenkey/lightsail/"))

    def test_ec2_entries_carry_platform_ec2(self):
        proc = _run("--prod-ops-matrix", "--target-selector", "all")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        payload = json.loads(proc.stdout)
        for item in payload["matrix"]["include"]:
            if item["target_id"].startswith("edge-") and not item["target_id"].endswith("-ls"):
                self.assertEqual(item["platform"], "ec2", item)
            if item["target_id"] == "prod":
                self.assertEqual(item["platform"], "ec2")

    def test_planned_lightsail_excluded_with_reason(self):
        proc = _run("--prod-ops-matrix", "--target-selector", "all")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        payload = json.loads(proc.stdout)
        excluded = payload["excluded"]
        ls_excluded = [item for item in excluded if item["target_id"].endswith("-ls")]
        for item in ls_excluded:
            self.assertIn("lightsail", item["reason"])

    def test_explicit_lightsail_selector(self):
        ls = json.loads(LIGHTSAIL_MATRIX.read_text(encoding="utf-8"))
        deployable_ls = [
            edge_id for edge_id, target in ls.get("targets", {}).items() if target.get("deployable")
        ]
        if not deployable_ls:
            self.skipTest("no deployable lightsail edges in matrix")
        edge_id = deployable_ls[0]
        proc = _run("--prod-ops-matrix", "--target-selector", f"edge:{edge_id}-ls")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        payload = json.loads(proc.stdout)
        include = payload["matrix"]["include"]
        self.assertEqual(len(include), 1)
        self.assertEqual(include[0]["target_id"], f"edge-{edge_id}-ls")
        self.assertEqual(include[0]["platform"], "lightsail")


if __name__ == "__main__":
    unittest.main()
