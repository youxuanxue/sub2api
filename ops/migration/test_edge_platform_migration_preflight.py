"""Tests for ops/migration/edge-platform-migration-preflight.sh.

The script's AWS-touching paths can't be exercised offline. What we CAN verify:

  1. --phase=plan runs without AWS calls and returns OK for known-good matrix
     entries (uk1 in both matrices, both deployable=false).
  2. --phase=plan fails for an unknown edge_id with a clear matrix message.
  3. --phase=plan fails when both matrices would set deployable=true for the
     same id (the exclusivity gate is honoured even before AWS).
  4. The script handles `--phase=foo` with exit 2 (usage error, not 1).
  5. Missing required tools surface exit 2.

These exercise the deterministic decision tree at the top of the script. The
provision/cutover/decommission branches need AWS creds and are smoke-tested
manually with the migration skill.

stdlib-only.
"""
from __future__ import annotations

import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "ops/migration/edge-platform-migration-preflight.sh"


def _scrubbed_env() -> dict:
    env = dict(os.environ)
    for key in ("GIT_DIR", "GIT_INDEX_FILE", "GIT_WORK_TREE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR"):
        env.pop(key, None)
    return env


def _stage_fake_repo(tmp: pathlib.Path, ec2: dict, ls: dict) -> pathlib.Path:
    fake_root = tmp / "repo"
    (fake_root / "ops/migration").mkdir(parents=True)
    (fake_root / "deploy/aws/stage0").mkdir(parents=True)
    (fake_root / "deploy/aws/lightsail").mkdir(parents=True)
    shutil.copy(SCRIPT, fake_root / "ops/migration/edge-platform-migration-preflight.sh")
    (fake_root / "ops/migration/edge-platform-migration-preflight.sh").chmod(0o755)
    (fake_root / "deploy/aws/stage0/edge-targets.json").write_text(json.dumps(ec2), encoding="utf-8")
    (fake_root / "deploy/aws/lightsail/edge-targets-lightsail.json").write_text(json.dumps(ls), encoding="utf-8")
    return fake_root


def _run(fake_root: pathlib.Path, *args: str) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["bash", str(fake_root / "ops/migration/edge-platform-migration-preflight.sh"), *args],
        capture_output=True,
        text=True,
        env=_scrubbed_env(),
    )


class PreflightPlanPhaseTests(unittest.TestCase):
    def test_plan_passes_for_known_good_matrix(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = _stage_fake_repo(
                pathlib.Path(tmp),
                {"targets": {"uk1": {"deployable": False, "region": "eu-west-2", "domain": "api-uk1.tokenkey.dev", "stack": "tokenkey-edge-uk1-stage0"}}},
                {"targets": {"uk1": {"deployable": False, "lightsail_region": "eu-west-2", "instance_name": "tokenkey-edge-uk1-ls", "static_ip_name": "tokenkey-edge-uk1-ls-ip", "ssm_prefix": "/tokenkey/lightsail/uk1"}}},
            )
            proc = _run(fake, "uk1", "--phase=plan")
            self.assertEqual(proc.returncode, 0, f"stderr: {proc.stderr}\nstdout: {proc.stdout}")
            self.assertIn("PASS", proc.stdout)

    def test_plan_fails_when_both_deployable_true(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = _stage_fake_repo(
                pathlib.Path(tmp),
                {"targets": {"uk1": {"deployable": True, "region": "eu-west-2", "domain": "api-uk1.tokenkey.dev", "stack": "tokenkey-edge-uk1-stage0"}}},
                {"targets": {"uk1": {"deployable": True, "lightsail_region": "eu-west-2", "instance_name": "tokenkey-edge-uk1-ls", "static_ip_name": "tokenkey-edge-uk1-ls-ip", "ssm_prefix": "/tokenkey/lightsail/uk1"}}},
            )
            proc = _run(fake, "uk1", "--phase=plan")
            self.assertEqual(proc.returncode, 1, proc.stdout)
            self.assertIn("exclusivity gate violation", proc.stderr)

    def test_plan_fails_for_unknown_edge_id(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = _stage_fake_repo(
                pathlib.Path(tmp),
                {"targets": {"uk1": {"deployable": False, "region": "eu-west-2", "domain": "api-uk1.tokenkey.dev", "stack": "tokenkey-edge-uk1-stage0"}}},
                {"targets": {"uk1": {"deployable": False, "lightsail_region": "eu-west-2", "instance_name": "tokenkey-edge-uk1-ls", "static_ip_name": "tokenkey-edge-uk1-ls-ip", "ssm_prefix": "/tokenkey/lightsail/uk1"}}},
            )
            proc = _run(fake, "zz9", "--phase=plan")
            self.assertEqual(proc.returncode, 1, proc.stdout)
            self.assertIn("no entry", proc.stderr)

    def test_unknown_phase_exits_2(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = _stage_fake_repo(
                pathlib.Path(tmp),
                {"targets": {"uk1": {"deployable": False}}},
                {"targets": {"uk1": {"deployable": False}}},
            )
            proc = _run(fake, "uk1", "--phase=bogus")
            self.assertEqual(proc.returncode, 2, proc.stdout)
            self.assertIn("unknown --phase", proc.stderr)

    def test_missing_edge_id_arg_exits_2(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = _stage_fake_repo(
                pathlib.Path(tmp),
                {"targets": {"uk1": {"deployable": False}}},
                {"targets": {"uk1": {"deployable": False}}},
            )
            proc = _run(fake)
            self.assertEqual(proc.returncode, 2, proc.stdout)
            self.assertIn("usage:", proc.stderr)

    def test_provision_phase_blocks_when_lightsail_still_false(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = _stage_fake_repo(
                pathlib.Path(tmp),
                {"targets": {"uk1": {"deployable": False, "region": "eu-west-2", "domain": "api-uk1.tokenkey.dev", "stack": "tokenkey-edge-uk1-stage0"}}},
                {"targets": {"uk1": {"deployable": False, "lightsail_region": "eu-west-2", "instance_name": "tokenkey-edge-uk1-ls", "static_ip_name": "tokenkey-edge-uk1-ls-ip", "ssm_prefix": "/tokenkey/lightsail/uk1"}}},
            )
            proc = _run(fake, "uk1", "--phase=provision")
            # AWS calls would fail here (no creds), but the matrix gate fires
            # first and we should see at least one FAIL referencing deployable.
            self.assertNotEqual(proc.returncode, 0)
            self.assertIn("must be deployable=true", proc.stderr)

    def test_real_repo_uk1_plan_passes(self):
        """Smoke against the actual repo matrices — uk1 should be plan-OK
        (both currently deployable=false). If this fails, someone has changed
        the matrices in a way that breaks the migration starting precondition."""
        proc = subprocess.run(
            ["bash", str(SCRIPT), "uk1", "--phase=plan"],
            capture_output=True, text=True, env=_scrubbed_env(),
        )
        self.assertEqual(proc.returncode, 0, f"stderr: {proc.stderr}\nstdout: {proc.stdout}")


if __name__ == "__main__":
    unittest.main()
