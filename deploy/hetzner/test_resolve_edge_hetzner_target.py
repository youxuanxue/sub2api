#!/usr/bin/env python3
"""Behavior tests for resolve-edge-hetzner-target.py (Phase-1 skeleton)."""
from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
RESOLVER = REPO_ROOT / "deploy/hetzner/resolve-edge-hetzner-target.py"
MATRIX = REPO_ROOT / "deploy/hetzner/edge-targets-hetzner.json"


def run_resolver(
    edge_id: str,
    *,
    confirm_instance: str = "",
    allow_planned: bool = False,
    matrix: Path | None = None,
) -> dict[str, str]:
    cmd = [sys.executable, str(RESOLVER), "--edge-id", edge_id]
    if confirm_instance:
        cmd.extend(["--confirm-instance", confirm_instance])
    if allow_planned:
        cmd.append("--allow-planned")
    if matrix is not None:
        cmd.extend(["--matrix", str(matrix)])
    proc = subprocess.run(cmd, capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        raise RuntimeError(proc.stderr.strip() or proc.stdout)
    out: dict[str, str] = {}
    for line in proc.stdout.splitlines():
        if "=" in line:
            k, v = line.split("=", 1)
            out[k] = v
    return out


class ResolveEdgeHetznerTargetTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.data = json.loads(MATRIX.read_text(encoding="utf-8"))
        cls.targets = cls.data.get("targets") or {}
        cls.any_id = sorted(cls.targets)[0] if cls.targets else None

    def test_matrix_all_planned_during_phase1(self) -> None:
        """Phase-1 invariant: no live hetzner cutover rows (after defaults)."""
        sys.path.insert(0, str(REPO_ROOT / "ops" / "stage0"))
        from edge_routing_matrix import load_hetzner_targets

        materialized = load_hetzner_targets(REPO_ROOT)
        deployable = [k for k, t in materialized.items() if t.get("deployable") is True]
        self.assertEqual(deployable, [], f"Phase-1 must keep deployable=false; got {deployable}")

    def test_planned_fails_without_allow_planned(self) -> None:
        if not self.any_id:
            self.skipTest("matrix empty")
        proc = subprocess.run(
            [sys.executable, str(RESOLVER), "--edge-id", self.any_id],
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("not deployable", proc.stderr)

    def test_planned_resolves_with_allow_planned(self) -> None:
        if not self.any_id:
            self.skipTest("matrix empty")
        expected = f"tokenkey-edge-{self.any_id}-hz-cax21"
        resolved = run_resolver(self.any_id, allow_planned=True, confirm_instance=expected)
        self.assertEqual(resolved["edge_id"], self.any_id)
        self.assertEqual(resolved["platform"], "hetzner")
        self.assertEqual(resolved["deployable"], "false")
        self.assertEqual(resolved["location"], "fsn1")
        self.assertEqual(resolved["server_type"], "cax21")
        self.assertEqual(resolved["architecture"], "arm")
        self.assertEqual(resolved["instance_name"], expected)
        self.assertEqual(resolved["domain"], f"api-{self.any_id}.tokenkey.dev")
        self.assertEqual(resolved["staging_domain"], f"api-{self.any_id}-hz.tokenkey.dev")
        self.assertEqual(resolved["ssm_prefix"], f"/tokenkey/hetzner/{self.any_id}")
        self.assertEqual(resolved["ssm_region"], "eu-west-2")
        self.assertEqual(
            resolved["ssm_hybrid_role_name"],
            f"tokenkey-hetzner-ssm-hybrid-{self.any_id}",
        )

    def test_unknown_edge_id_fails(self) -> None:
        proc = subprocess.run(
            [sys.executable, str(RESOLVER), "--edge-id", "zz9", "--allow-planned"],
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("unknown edge_id", proc.stderr)

    def test_reject_non_fsn1_location(self) -> None:
        if not self.any_id:
            self.skipTest("matrix empty")
        bad = json.loads(MATRIX.read_text(encoding="utf-8"))
        bad["targets"][self.any_id] = {
            **bad["targets"][self.any_id],
            "deployable": True,
            "location": "ash",
        }
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "matrix.json"
            path.write_text(json.dumps(bad), encoding="utf-8")
            proc = subprocess.run(
                [sys.executable, str(RESOLVER), "--edge-id", self.any_id, "--matrix", str(path)],
                capture_output=True,
                text=True,
            )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("location", proc.stderr)

    def test_reject_non_cax21_server_type(self) -> None:
        if not self.any_id:
            self.skipTest("matrix empty")
        bad = json.loads(MATRIX.read_text(encoding="utf-8"))
        bad["targets"][self.any_id] = {
            **bad["targets"][self.any_id],
            "deployable": True,
            "server_type": "cx33",
        }
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "matrix.json"
            path.write_text(json.dumps(bad), encoding="utf-8")
            proc = subprocess.run(
                [sys.executable, str(RESOLVER), "--edge-id", self.any_id, "--matrix", str(path)],
                capture_output=True,
                text=True,
            )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("server_type", proc.stderr)

    def test_reject_amd64_architecture(self) -> None:
        if not self.any_id:
            self.skipTest("matrix empty")
        bad = json.loads(MATRIX.read_text(encoding="utf-8"))
        bad["targets"][self.any_id] = {
            **bad["targets"][self.any_id],
            "deployable": True,
            "architecture": "x86",
        }
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "matrix.json"
            path.write_text(json.dumps(bad), encoding="utf-8")
            proc = subprocess.run(
                [sys.executable, str(RESOLVER), "--edge-id", self.any_id, "--matrix", str(path)],
                capture_output=True,
                text=True,
            )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("architecture", proc.stderr)

    def test_confirm_instance_mismatch_fails(self) -> None:
        if not self.any_id:
            self.skipTest("matrix empty")
        proc = subprocess.run(
            [
                sys.executable,
                str(RESOLVER),
                "--edge-id",
                self.any_id,
                "--allow-planned",
                "--confirm-instance",
                "wrong-name",
            ],
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("confirm_instance mismatch", proc.stderr)


if __name__ == "__main__":
    unittest.main()
