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
    def test_historical_matrix_is_not_deployable(self) -> None:
        data = json.loads(MATRIX.read_text(encoding="utf-8"))
        self.assertFalse(bool(data["target"].get("deployable")))

    def test_retired_cannot_be_reenabled_with_allow_planned(self) -> None:
        for allowed in (False, True):
            with self.subTest(allowed=allowed), self.assertRaisesRegex(RuntimeError, "retired"):
                run_resolver(allow_planned=allowed)

    def test_new_planned_identity_resolves(self) -> None:
        data = json.loads(MATRIX.read_text())
        data["target"].update(retired=False, instance_name="new-recovery-host")
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "matrix.json"
            path.write_text(json.dumps(data))
            resolved = run_resolver(allow_planned=True, confirm_instance="new-recovery-host", matrix=path)
        self.assertEqual(resolved["instance_name"], "new-recovery-host")
        self.assertEqual(resolved["volume_mount"], "/var/lib/tokenkey")

    def test_planned_fails_without_allow_planned(self) -> None:
        data = json.loads(MATRIX.read_text(encoding="utf-8"))
        data["target"].update(deployable=False, retired=False)
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
        bad["target"].update(deployable=True, retired=False)
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
        bad["target"].update(deployable=True, retired=False)
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
        data = json.loads(MATRIX.read_text())
        data["target"]["retired"] = False
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "matrix.json"
            path.write_text(json.dumps(data))
            with self.assertRaisesRegex(RuntimeError, "confirm_instance mismatch"):
                run_resolver(confirm_instance="wrong-name", allow_planned=True, matrix=path)


if __name__ == "__main__":
    unittest.main()
