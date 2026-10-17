"""Verify --prod-ops-matrix surfaces prod (+ any live edges).

stdlib-only. 2026-10-17: live edges empty (prod-only).
"""
from __future__ import annotations

import json
import pathlib
import subprocess
import sys
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
SCRIPT = REPO_ROOT / "deploy/aws/stage0/resolve-edge-target.py"
sys.path.insert(0, str(REPO_ROOT / "ops" / "stage0"))
from edge_routing_matrix import live_deployable_edge_ids  # noqa: E402


def _run(*args: str) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(SCRIPT), *args],
        capture_output=True,
        text=True,
        check=False,
        cwd=str(REPO_ROOT),
    )


class ProdOpsMatrixLiveFleetTests(unittest.TestCase):
    def test_all_selector_is_prod_only_when_edges_retired(self):
        live = live_deployable_edge_ids(REPO_ROOT)
        self.assertEqual(live, [])
        proc = _run("--prod-ops-matrix", "--target-selector", "all")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        payload = json.loads(proc.stdout)
        include = payload["matrix"]["include"]
        target_ids = {item["target_id"]: item for item in include}
        self.assertEqual(set(target_ids), {"prod"})
        self.assertEqual(target_ids["prod"]["platform"], "aws")

    def test_standby_edges_excluded_with_reason(self):
        proc = _run("--prod-ops-matrix", "--target-selector", "all")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        excluded = {item["target_id"]: item["reason"] for item in json.loads(proc.stdout)["excluded"]}
        for edge_id in ("uk1", "uk2", "us4", "us5"):
            self.assertIn(f"edge-{edge_id}-ls", excluded)
            self.assertIn("deployable=false", excluded[f"edge-{edge_id}-ls"])

    def test_prod_uses_tokyo_fresh_coordinates(self):
        proc = _run("--prod-ops-matrix", "--target-selector", "all")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        include = json.loads(proc.stdout)["matrix"]["include"]
        self.assertEqual({t["platform"] for t in include}, {"aws"})
        prod = next(t for t in include if t["target_id"] == "prod")
        fresh = json.loads((REPO_ROOT / "deploy/aws/fresh-migration-20261015.json").read_text())["prod"]
        self.assertEqual(prod["region"], fresh["region"])
        self.assertEqual(prod["domain"], fresh["domain"])
        self.assertEqual(prod["platform"], "aws")
        self.assertEqual(prod["stack"], "")
        self.assertEqual(fresh["instance_name"], "tokenkey-prod-tokyo-20261017")

    def test_explicit_retired_edge_selector_rejected(self):
        proc = _run("--prod-ops-matrix", "--target-selector", "edge:uk1-ls")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("not in the live deployable fleet", proc.stderr)


if __name__ == "__main__":
    unittest.main()
