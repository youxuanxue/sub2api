#!/usr/bin/env python3
"""Tests for scripts/stage0/resolve-edge-deploy-route.py."""
from __future__ import annotations

import json
import tempfile
import pathlib
import subprocess
import sys
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[1]
SCRIPT = REPO_ROOT / "scripts/stage0/resolve-edge-deploy-route.py"
sys.path.insert(0, str(REPO_ROOT / "ops/stage0"))
from edge_routing_matrix import load_lightsail_targets, resolve_route_tab

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
        targets = json.loads(LIGHTSAIL_MATRIX.read_text(encoding="utf-8")).get("targets") or {}
        expected_instance = str((targets.get(edge_id) or {}).get("instance_name") or "")
        route = self._route(edge_id)
        self.assertEqual(route["platform"], "lightsail")
        self.assertEqual(route["workflow_file"], "deploy-edge-lightsail-stage0.yml")
        self.assertEqual(route["confirm_flag"], "confirm_instance")
        self.assertEqual(route["confirm_value"], expected_instance)
        self.assertTrue(expected_instance)

    def test_non_deployable_edge_fails(self) -> None:
        proc = subprocess.run(
            [sys.executable, str(SCRIPT), "--edge-id", "fra1", "--json"],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("not deployable", proc.stderr)


class EdgeRoutingBoundaryTest(unittest.TestCase):
    def test_missing_or_corrupt_matrix_fails_closed(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            matrix = root / "deploy/aws/lightsail/edge-targets-lightsail.json"
            with self.assertRaises(OSError):
                load_lightsail_targets(root)
            matrix.parent.mkdir(parents=True)
            for invalid in ('{broken', '{"targets":[]}', '{"targets":{"us3":null}}'):
                matrix.write_text(invalid)
                with self.assertRaises(ValueError):
                    load_lightsail_targets(root)

    def test_planned_edge_requires_explicit_lightsail_preference(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            matrix = root / "deploy/aws/lightsail/edge-targets-lightsail.json"
            matrix.parent.mkdir(parents=True)
            matrix.write_text(json.dumps({"targets": {"pilot": {
                "deployable": False, "lightsail_region": "us-east-1", "ssm_prefix": "/pilot",
            }}}))
            with self.assertRaisesRegex(SystemExit, "not deployable"):
                resolve_route_tab(root, "pilot")
            self.assertEqual(resolve_route_tab(root, "pilot", "lightsail"),
                             ("lightsail", "us-east-1", None))
            with self.assertRaisesRegex(SystemExit, "retired"):
                resolve_route_tab(root, "pilot", "ec2")
            with self.assertRaisesRegex(SystemExit, "unknown"):
                resolve_route_tab(root, "missing")


if __name__ == "__main__":
    unittest.main()
