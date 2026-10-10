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
    def _route(self, *args: str) -> dict:
        proc = subprocess.run(
            [sys.executable, str(SCRIPT), "--json", *args],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            check=True,
        )
        return json.loads(proc.stdout)

    def test_explicit_lightsail_routes_standby(self) -> None:
        edge_id = _deployable_lightsail_edge()
        if edge_id is None:
            self.skipTest("no deployable Lightsail edge in matrix")
        targets = json.loads(LIGHTSAIL_MATRIX.read_text(encoding="utf-8")).get("targets") or {}
        expected_instance = str((targets.get(edge_id) or {}).get("instance_name") or "")
        route = self._route("--edge-id", edge_id, "--platform", "lightsail")
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

    def test_live_hetzner_wins_auto(self) -> None:
        proc = subprocess.run(
            [sys.executable, str(SCRIPT), "--edge-id", "uk1", "--json"],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        route = json.loads(proc.stdout)
        self.assertEqual(route["platform"], "hetzner")
        self.assertEqual(route["workflow_file"], "deploy-edge-hetzner-stage0.yml")
        self.assertEqual(route["confirm_value"], "tokenkey-edge-uk1-hz-cax21")

        # Backlog Hetzner (us3) still needs explicit platform + allow-planned.
        route_hz = self._route(
            "--edge-id", "us3", "--platform", "hetzner", "--allow-planned"
        )
        self.assertEqual(route_hz["platform"], "hetzner")
        self.assertEqual(route_hz["workflow_file"], "deploy-edge-hetzner-stage0.yml")
        self.assertEqual(route_hz["confirm_value"], "tokenkey-edge-us3-hz-cax21")
    def test_hetzner_deployable_wins_auto(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            # Minimal dual matrices: lightsail deployable + hetzner deployable → hetzner wins
            ls = root / "deploy/aws/lightsail/edge-targets-lightsail.json"
            hz = root / "deploy/hetzner/edge-targets-hetzner.json"
            ls.parent.mkdir(parents=True)
            hz.parent.mkdir(parents=True)
            ls.write_text(json.dumps({"targets": {"canary": {
                "deployable": True,
                "lightsail_region": "eu-west-2",
                "ssm_prefix": "/tokenkey/lightsail/canary",
                "instance_name": "ls-canary",
            }}}))
            hz.write_text(json.dumps({"targets": {"canary": {
                "deployable": True,
                "location": "fsn1",
                "server_type": "cax21",
                "ssm_prefix": "/tokenkey/hetzner/canary",
                "instance_name": "hz-canary",
            }}}))
            # Patch script's REPO_ROOT by running resolve_route_tab directly
            from edge_routing_matrix import resolve_route_tab
            transport, loc, _ = resolve_route_tab(root, "canary", "auto")
            self.assertEqual(transport, "hetzner")
            self.assertEqual(loc, "fsn1")


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
            hz = root / "deploy/hetzner/edge-targets-hetzner.json"
            matrix.parent.mkdir(parents=True)
            hz.parent.mkdir(parents=True)
            matrix.write_text(json.dumps({"targets": {"pilot": {
                "deployable": False, "lightsail_region": "us-east-1", "ssm_prefix": "/pilot",
            }}}))
            hz.write_text(json.dumps({"targets": {}}))
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
