#!/usr/bin/env python3
"""Tests for scripts/stage0/resolve-edge-deploy-route.py."""
from __future__ import annotations

import json
import contextlib
import io
import pathlib
import subprocess
import sys
import tempfile
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[1]
SCRIPT = REPO_ROOT / "scripts/stage0/resolve-edge-deploy-route.py"
LIGHTSAIL_MATRIX = REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json"

sys.path.insert(0, str(REPO_ROOT / "ops/stage0"))
from edge_routing_matrix import resolve_route_tab  # noqa: E402


def _deployable_lightsail_edge() -> str | None:
    matrix = json.loads(LIGHTSAIL_MATRIX.read_text(encoding="utf-8"))
    targets = matrix.get("targets") or {}
    deployable = sorted(
        edge_id for edge_id, target in targets.items()
        if isinstance(target, dict) and target.get("deployable") is True
    )
    return deployable[0] if deployable else None


class ResolveEdgeDeployRouteTest(unittest.TestCase):
    def _route(self, edge_id: str, *, platform: str = "auto") -> dict:
        proc = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--edge-id", edge_id,
                "--platform", platform,
                "--json",
            ],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            check=True,
        )
        return json.loads(proc.stdout)

    def _fake_root(
        self,
        temp: pathlib.Path,
        ec2_target: dict,
        lightsail_target: dict | None = None,
    ) -> pathlib.Path:
        root = temp / "repo"
        (root / "deploy/aws/stage0").mkdir(parents=True)
        (root / "deploy/aws/lightsail").mkdir(parents=True)
        (root / "deploy/aws/stage0/edge-targets.json").write_text(
            json.dumps({"targets": {"us9": ec2_target}}), encoding="utf-8"
        )
        (root / "deploy/aws/lightsail/edge-targets-lightsail.json").write_text(
            json.dumps({"targets": {"us9": lightsail_target} if lightsail_target else {}}),
            encoding="utf-8",
        )
        return root

    def test_deployable_edge_routes_to_lightsail(self) -> None:
        edge_id = _deployable_lightsail_edge()
        if edge_id is None:
            self.skipTest("no deployable Lightsail edge in matrix")
        route = self._route(edge_id)
        self.assertEqual(route["platform"], "lightsail")
        self.assertEqual(route["workflow_file"], "deploy-edge-lightsail-stage0.yml")
        self.assertEqual(route["confirm_flag"], "confirm_instance")
        self.assertTrue(route["confirm_value"].endswith("-ls"))

    def test_explicit_ec2_routes_migration_candidate(self) -> None:
        route = self._route("us5", platform="ec2")
        self.assertEqual(route["platform"], "ec2")
        self.assertEqual(route["workflow_file"], "deploy-edge-stage0.yml")
        self.assertEqual(route["confirm_flag"], "confirm_stack")
        self.assertEqual(route["confirm_value"], "tokenkey-edge-us5-stage0")
        self.assertTrue(route["allow_migration_candidate"])

    def test_auto_does_not_fall_back_to_candidate(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = self._fake_root(pathlib.Path(raw), {
                "deployable": False,
                "migration_candidate": True,
                "region": "us-west-2",
                "stack": "tokenkey-edge-us9-stage0",
            })
            with contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    resolve_route_tab(root, "us9", "auto")

    def test_explicit_ec2_rejects_ordinary_planned_target(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = self._fake_root(pathlib.Path(raw), {
                "deployable": False,
                "migration_candidate": False,
                "region": "us-west-2",
                "stack": "tokenkey-edge-us9-stage0",
            })
            with contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    resolve_route_tab(root, "us9", "ec2")

    def test_auto_rejects_dual_deployable_owners(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = self._fake_root(
                pathlib.Path(raw),
                {"deployable": True, "region": "us-west-2", "stack": "tokenkey-edge-us9-stage0"},
                {"deployable": True, "lightsail_region": "us-west-2", "ssm_prefix": "/x"},
            )
            with contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    resolve_route_tab(root, "us9", "auto")

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
