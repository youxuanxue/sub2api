#!/usr/bin/env python3
"""Behavior tests for resolve-prod-hetzner-target.py."""
from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
RESOLVER = REPO_ROOT / "deploy/hetzner/resolve-prod-hetzner-target.py"
MATRIX = REPO_ROOT / "deploy/hetzner/prod-target-hetzner.json"


def run_resolver(
    *,
    confirm_instance: str = "",
    allow_planned: bool = False,
    matrix: Path | None = None,
) -> dict[str, str]:
    cmd = [sys.executable, str(RESOLVER)]
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


class ResolveProdHetznerTargetTests(unittest.TestCase):
    def test_matrix_is_deployable(self) -> None:
        data = json.loads(MATRIX.read_text(encoding="utf-8"))
        self.assertTrue(bool(data["target"].get("deployable")))

    def test_live_resolves_without_allow_planned(self) -> None:
        resolved = run_resolver(confirm_instance="tokenkey-prod-hz-cax21")
        self.assertEqual(resolved["role"], "prod")
        self.assertEqual(resolved["platform"], "hetzner")
        self.assertEqual(resolved["deployable"], "true")
        self.assertEqual(resolved["location"], "fsn1")
        self.assertEqual(resolved["server_type"], "cax21")
        self.assertEqual(resolved["architecture"], "arm")
        self.assertEqual(resolved["instance_name"], "tokenkey-prod-hz-cax21")
        self.assertEqual(resolved["domain"], "api.tokenkey.dev")
        self.assertEqual(resolved["staging_domain"], "api-hz.tokenkey.dev")
        self.assertEqual(resolved["ssm_prefix"], "/tokenkey/hetzner/prod")
        self.assertEqual(resolved["volume_name"], "tokenkey-prod-data")
        self.assertEqual(resolved["volume_mount"], "/var/lib/tokenkey")
        self.assertGreaterEqual(int(resolved["volume_size_gb"]), 40)
        self.assertEqual(
            resolved["ssm_hybrid_role_name"],
            "tokenkey-hetzner-ssm-hybrid-prod",
        )

    def test_planned_fails_without_allow_planned(self) -> None:
        data = json.loads(MATRIX.read_text(encoding="utf-8"))
        data["target"]["deployable"] = False
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "matrix.json"
            path.write_text(json.dumps(data), encoding="utf-8")
            proc = subprocess.run(
                [sys.executable, str(RESOLVER), "--matrix", str(path)],
                capture_output=True,
                text=True,
            )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("not deployable", proc.stderr)

    def test_reject_non_fsn1_location(self) -> None:
        bad = json.loads(MATRIX.read_text(encoding="utf-8"))
        bad["target"]["location"] = "ash"
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "matrix.json"
            path.write_text(json.dumps(bad), encoding="utf-8")
            proc = subprocess.run(
                [sys.executable, str(RESOLVER), "--matrix", str(path)],
                capture_output=True,
                text=True,
            )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("location", proc.stderr)

    def test_reject_wrong_volume_mount(self) -> None:
        bad = json.loads(MATRIX.read_text(encoding="utf-8"))
        bad["target"]["volume_mount"] = "/data"
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "matrix.json"
            path.write_text(json.dumps(bad), encoding="utf-8")
            proc = subprocess.run(
                [sys.executable, str(RESOLVER), "--matrix", str(path)],
                capture_output=True,
                text=True,
            )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("volume_mount", proc.stderr)

    def test_confirm_instance_mismatch_fails(self) -> None:
        proc = subprocess.run(
            [
                sys.executable,
                str(RESOLVER),
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
