"""Tests for scripts/checks/edge-platform-exclusivity.py.

stdlib-only.
"""
from __future__ import annotations

import json
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts/checks/edge-platform-exclusivity.py"


def _stub_matrices(tmpdir: pathlib.Path, ec2: dict, ls: dict) -> pathlib.Path:
    """Lay out a stand-in REPO_ROOT with the two matrix files at the relative
    paths the script expects, so we can swap REPO_ROOT via PYTHONPATH-like means.
    Actually simpler: copy the script next to a fake repo_root that mimics the
    project layout, and invoke it via subprocess so REPO_ROOT resolution picks
    up the stand-in.
    """
    fake_root = tmpdir / "repo"
    (fake_root / "scripts/checks").mkdir(parents=True)
    (fake_root / "deploy/aws/stage0").mkdir(parents=True)
    (fake_root / "deploy/aws/lightsail").mkdir(parents=True)
    shutil.copy(SCRIPT, fake_root / "scripts/checks/edge-platform-exclusivity.py")
    (fake_root / "deploy/aws/stage0/edge-targets.json").write_text(json.dumps(ec2), encoding="utf-8")
    (fake_root / "deploy/aws/lightsail/edge-targets-lightsail.json").write_text(json.dumps(ls), encoding="utf-8")
    return fake_root


def _run(fake_root: pathlib.Path) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(fake_root / "scripts/checks/edge-platform-exclusivity.py")],
        capture_output=True,
        text=True,
        check=False,
    )


class EdgePlatformExclusivityTests(unittest.TestCase):
    def test_disjoint_passes(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = _stub_matrices(
                pathlib.Path(tmp),
                {"targets": {"uk1": {"deployable": True}}},
                {"targets": {"us1": {"deployable": True}}},
            )
            proc = _run(fake)
            self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_collision_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = _stub_matrices(
                pathlib.Path(tmp),
                {"targets": {"uk1": {"deployable": True}}},
                {"targets": {"uk1": {"deployable": True}}},
            )
            proc = _run(fake)
            self.assertEqual(proc.returncode, 1, proc.stdout)
            self.assertIn("uk1", proc.stderr)
            self.assertIn("deployable=true on BOTH", proc.stderr)

    def test_one_side_planned_passes(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = _stub_matrices(
                pathlib.Path(tmp),
                {"targets": {"uk1": {"deployable": True}}},
                {"targets": {"uk1": {"deployable": False}}},
            )
            proc = _run(fake)
            self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_lightsail_matrix_missing_passes(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake_root = pathlib.Path(tmp) / "repo"
            (fake_root / "scripts/checks").mkdir(parents=True)
            (fake_root / "deploy/aws/stage0").mkdir(parents=True)
            shutil.copy(SCRIPT, fake_root / "scripts/checks/edge-platform-exclusivity.py")
            (fake_root / "deploy/aws/stage0/edge-targets.json").write_text(
                json.dumps({"targets": {"uk1": {"deployable": True}}}),
                encoding="utf-8",
            )
            proc = _run(fake_root)
            self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_malformed_matrix_fails_with_2(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake_root = pathlib.Path(tmp) / "repo"
            (fake_root / "scripts/checks").mkdir(parents=True)
            (fake_root / "deploy/aws/stage0").mkdir(parents=True)
            (fake_root / "deploy/aws/lightsail").mkdir(parents=True)
            shutil.copy(SCRIPT, fake_root / "scripts/checks/edge-platform-exclusivity.py")
            (fake_root / "deploy/aws/stage0/edge-targets.json").write_text("{not json", encoding="utf-8")
            (fake_root / "deploy/aws/lightsail/edge-targets-lightsail.json").write_text(
                json.dumps({"targets": {}}), encoding="utf-8"
            )
            proc = _run(fake_root)
            self.assertEqual(proc.returncode, 2, proc.stderr)

    def test_real_matrices_are_disjoint(self):
        """Smoke against the actual repository matrices — should always be
        green; if this test ever fails, an operator has accidentally flipped
        deployable=true on both sides for the same id, which is exactly what
        the gate is designed to catch pre-merge."""
        proc = subprocess.run(
            [sys.executable, str(SCRIPT)],
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(proc.returncode, 0, f"real-repo collision: {proc.stderr}")


if __name__ == "__main__":
    unittest.main()
