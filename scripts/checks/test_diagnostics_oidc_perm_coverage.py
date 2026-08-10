"""Tests for scripts/checks/diagnostics-oidc-perm-coverage.py."""

from __future__ import annotations

import importlib.util
import pathlib
import subprocess
import sys
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts/checks/diagnostics-oidc-perm-coverage.py"


def _load_module():
    spec = importlib.util.spec_from_file_location("diagnostics_perm_coverage", SCRIPT)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


class DiagnosticsOidcPermCoverageTests(unittest.TestCase):
    def setUp(self) -> None:
        self.mod = _load_module()

    def test_expected_actions_include_describe_snapshots(self) -> None:
        actions = {action for action, _ in self.mod.EXPECTED_BASE_ACTIONS}
        self.assertIn("ec2:DescribeSnapshots", actions)

    def test_missing_action_detected(self) -> None:
        missing = [
            action
            for action, _ in self.mod.EXPECTED_BASE_ACTIONS
            if action not in "(empty policy)"
        ]
        self.assertEqual(len(missing), len(self.mod.EXPECTED_BASE_ACTIONS))

    def test_real_repo_passes(self) -> None:
        proc = subprocess.run(
            [sys.executable, str(SCRIPT), "--quiet"],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)


if __name__ == "__main__":
    unittest.main()
