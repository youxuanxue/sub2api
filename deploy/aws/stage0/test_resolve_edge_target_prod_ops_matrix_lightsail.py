"""Verify --prod-ops-matrix surfaces the live Hetzner-first fleet + prod.

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
sys.path.insert(0, str(REPO_ROOT / "ops" / "stage0"))
from edge_routing_matrix import live_deployable_edge_ids, load_live_targets, resolve_route_tab  # noqa: E402


def _run(*args: str) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(SCRIPT), *args],
        capture_output=True,
        text=True,
        check=False,
        cwd=str(REPO_ROOT),
    )


class ProdOpsMatrixLiveFleetTests(unittest.TestCase):
    def test_all_selector_includes_live_hz_edges(self):
        live = live_deployable_edge_ids(REPO_ROOT)
        self.assertEqual(live, ["uk1", "uk2", "us4", "us5"])
        proc = _run("--prod-ops-matrix", "--target-selector", "all")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        payload = json.loads(proc.stdout)
        include = payload["matrix"]["include"]
        target_ids = {item["target_id"]: item for item in include}
        self.assertIn("prod", target_ids)
        for edge_id in live:
            platform = resolve_route_tab(REPO_ROOT, edge_id)[0]
            row = load_live_targets(REPO_ROOT)[edge_id]
            hz_id = f"edge-{edge_id}-" + ("hz" if platform == "hetzner" else "ls")
            self.assertIn(hz_id, target_ids, f"missing hetzner edge in matrix: {hz_id}")
            self.assertEqual(target_ids[hz_id]["platform"], platform)
            self.assertEqual(target_ids[hz_id]["target_kind"], "edge")
            self.assertEqual(target_ids[hz_id]["stack"], "")
            self.assertEqual(target_ids[hz_id]["ssm_prefix"], row["ssm_prefix"])
            self.assertEqual(target_ids[hz_id]["region"], row.get("lightsail_region", "eu-west-2"))

    def test_lightsail_standby_excluded_with_reason(self):
        proc = _run("--prod-ops-matrix", "--target-selector", "all")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        excluded = {item["target_id"]: item["reason"] for item in json.loads(proc.stdout)["excluded"]}
        for edge_id in ("uk1", "uk2", "us4", "us5"):
            if resolve_route_tab(REPO_ROOT, edge_id)[0] == "hetzner":
                self.assertIn("Hetzner", excluded[f"edge-{edge_id}-ls"])
            else:
                self.assertNotIn(f"edge-{edge_id}-ls", excluded)

    def test_prod_uses_live_hetzner_coordinates(self):
        proc = _run("--prod-ops-matrix", "--target-selector", "all")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        include = json.loads(proc.stdout)["matrix"]["include"]
        self.assertEqual({t["platform"] for t in include}, {"hetzner"} | {resolve_route_tab(REPO_ROOT, edge)[0] for edge in live_deployable_edge_ids(REPO_ROOT)})
        prod = next(t for t in include if t["target_id"] == "prod")
        self.assertEqual(prod["region"], "eu-west-2")
        self.assertEqual(prod["stack"], "")

    def test_explicit_live_edge_selector(self):
        proc = _run("--prod-ops-matrix", "--target-selector", "edge:uk1-hz")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        include = json.loads(proc.stdout)["matrix"]["include"]
        self.assertEqual(len(include), 1)
        self.assertEqual(include[0]["target_id"], "edge-uk1-" + ("hz" if resolve_route_tab(REPO_ROOT, "uk1")[0] == "hetzner" else "ls"))
        self.assertEqual(include[0]["platform"], resolve_route_tab(REPO_ROOT, "uk1")[0])


if __name__ == "__main__":
    unittest.main()
