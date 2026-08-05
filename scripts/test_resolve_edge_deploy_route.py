#!/usr/bin/env python3
"""Tests for scripts/stage0/resolve-edge-deploy-route.py."""
from __future__ import annotations

import contextlib
import io
import json
import os
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
    def _fake_route_root(
        self,
        temp: pathlib.Path,
        ec2_target: dict,
        lightsail_target: dict | None = None,
    ) -> pathlib.Path:
        root = temp / "repo"
        (root / "deploy/aws/stage0").mkdir(parents=True)
        (root / "deploy/aws/lightsail").mkdir(parents=True)
        (root / "deploy/aws/stage0/edge-targets.json").write_text(
            json.dumps({"targets": {"us9": ec2_target}}),
            encoding="utf-8",
        )
        (root / "deploy/aws/lightsail/edge-targets-lightsail.json").write_text(
            json.dumps(
                {"targets": {"us9": lightsail_target} if lightsail_target else {}},
            ),
            encoding="utf-8",
        )
        return root

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

    def _dispatch(self, *arguments: str) -> tuple[subprocess.CompletedProcess, list[str] | None]:
        with tempfile.TemporaryDirectory() as tmp:
            temp = pathlib.Path(tmp)
            gh_log = temp / "gh.log"
            fake_gh = temp / "gh"
            fake_gh.write_text(
                "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > \"$GH_LOG\"\n",
                encoding="utf-8",
            )
            fake_gh.chmod(0o755)
            env = os.environ.copy()
            env["PATH"] = f"{temp}:{env['PATH']}"
            env["GH_LOG"] = str(gh_log)
            proc = subprocess.run(
                ["bash", "scripts/stage0/dispatch-edge-deploy.sh", *arguments],
                cwd=REPO_ROOT,
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            gh_args = (
                gh_log.read_text(encoding="utf-8").splitlines()
                if gh_log.exists()
                else None
            )
            return proc, gh_args

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

    def test_dispatch_explicit_ec2_sets_candidate_workflow_gate(self) -> None:
        proc, gh_args = self._dispatch(
            "--edge-id", "us5",
            "--platform", "ec2",
            "--operation", "provision",
            "--tag", "1.2.3",
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIsNotNone(gh_args)
        self.assertIn("deploy-edge-stage0.yml", gh_args)
        self.assertIn("confirm_stack=tokenkey-edge-us5-stage0", gh_args)
        self.assertIn("allow_migration_candidate=true", gh_args)

    def test_dispatch_rejects_rotation_without_reason_before_gh(self) -> None:
        proc, gh_args = self._dispatch(
            "--edge-id", "us5",
            "--platform", "ec2",
            "--operation", "rotate_egress_ip",
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("--rotation-reason is required", proc.stderr)
        self.assertIsNone(gh_args)

    def test_dispatch_rejects_decommission_without_ack_before_gh(self) -> None:
        proc, gh_args = self._dispatch(
            "--edge-id", "us5",
            "--platform", "ec2",
            "--operation", "decommission",
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("--ack-decommission is required", proc.stderr)
        self.assertIsNone(gh_args)

    def test_dispatch_forwards_candidate_rotation_reason(self) -> None:
        proc, gh_args = self._dispatch(
            "--edge-id", "us5",
            "--platform", "ec2",
            "--operation", "rotate_egress_ip",
            "--rotation-reason", "provider-risk-block",
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIsNotNone(gh_args)
        self.assertIn("rotation_reason=provider-risk-block", gh_args)

    def test_dispatch_forwards_decommission_ack_and_eip_release(self) -> None:
        proc, gh_args = self._dispatch(
            "--edge-id", "us5",
            "--platform", "ec2",
            "--operation", "decommission",
            "--ack-decommission",
            "--release-eip",
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIsNotNone(gh_args)
        self.assertIn("i_understand_decommissions_edge=true", gh_args)
        self.assertIn("release_eip=true", gh_args)

    def test_dispatch_stops_before_gh_when_route_resolution_fails(self) -> None:
        proc, gh_args = self._dispatch(
            "--edge-id", "fra1",
            "--operation", "smoke",
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIsNone(gh_args, "route failure must not dispatch a workflow")

    def test_auto_does_not_fall_back_to_ec2_candidate(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = self._fake_route_root(
                pathlib.Path(tmp),
                {
                    "deployable": False,
                    "migration_candidate": True,
                    "region": "us-west-2",
                    "stack": "tokenkey-edge-us9-stage0",
                },
            )
            with contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    resolve_route_tab(root, "us9", "auto")

    def test_explicit_ec2_rejects_ordinary_planned_target(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = self._fake_route_root(
                pathlib.Path(tmp),
                {
                    "deployable": False,
                    "migration_candidate": False,
                    "region": "us-west-2",
                    "stack": "tokenkey-edge-us9-stage0",
                },
            )
            with contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    resolve_route_tab(root, "us9", "ec2")

    def test_auto_rejects_dual_deployable_platforms(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = self._fake_route_root(
                pathlib.Path(tmp),
                {
                    "deployable": True,
                    "region": "us-west-2",
                    "stack": "tokenkey-edge-us9-stage0",
                },
                {
                    "deployable": True,
                    "lightsail_region": "us-west-2",
                    "ssm_prefix": "/tokenkey/lightsail/us9",
                },
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
