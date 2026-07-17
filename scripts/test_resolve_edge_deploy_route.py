#!/usr/bin/env python3
"""Tests for scripts/stage0/resolve-edge-deploy-route.py."""
from __future__ import annotations

import json
import pathlib
import subprocess
import sys
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[1]
SCRIPT = REPO_ROOT / "scripts/stage0/resolve-edge-deploy-route.py"
LIGHTSAIL_MATRIX = REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json"


def _deployable_lightsail_edge() -> str | None:
    matrix = json.loads(LIGHTSAIL_MATRIX.read_text(encoding="utf-8"))
    targets = matrix.get("targets") or {}
    deployable = sorted(
        edge_id for edge_id, target in targets.items()
        if isinstance(target, dict) and target.get("deployable") is True
    )
    return deployable[0] if deployable else None


class ResolveEdgeDeployRouteTest(unittest.TestCase):
    def _route(self, edge_id: str) -> dict:
        proc = subprocess.run(
            [sys.executable, str(SCRIPT), "--edge-id", edge_id, "--json"],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            check=True,
        )
        return json.loads(proc.stdout)

    def test_deployable_edge_routes_to_lightsail(self) -> None:
        edge_id = _deployable_lightsail_edge()
        if edge_id is None:
            self.skipTest("no deployable Lightsail edge in matrix")
        route = self._route(edge_id)
        self.assertEqual(route["platform"], "lightsail")
        self.assertEqual(route["workflow_file"], "deploy-edge-lightsail-stage0.yml")
        self.assertEqual(route["confirm_flag"], "confirm_instance")
        self.assertTrue(route["confirm_value"].endswith("-ls"))

    # NOTE: us1 was the last EC2 edge; it is being retired (deployable=false →
    # decommission, replaced by the us6 Lightsail edge). With no deployable EC2 edge
    # left in the matrix there is no live fixture for the EC2 routing branch, so the
    # former `test_us1_routes_to_ec2` happy-path test is dropped. The rejection path
    # below still exercises non-deployable resolution.

    def test_non_deployable_edge_fails(self) -> None:
        proc = subprocess.run(
            [sys.executable, str(SCRIPT), "--edge-id", "fra1", "--json"],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("not effectively deployable", proc.stderr)


if __name__ == "__main__":
    unittest.main()
