#!/usr/bin/env python3
"""Unit tests for require_edge_restore_approval.py."""
from __future__ import annotations

import os
import pathlib
import subprocess
import unittest

REPO = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO / "ops/stage0/require_edge_restore_approval.py"
TOKEN = "I_APPROVE_EDGE_RESTORE"


def _run(*args: str, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    base = {k: v for k, v in os.environ.items() if k not in {"CONFIRM_EDGE_RESTORE", "GITHUB_ACTIONS"}}
    if env:
        base.update(env)
    return subprocess.run(
        [sys_executable(), str(SCRIPT), *args],
        cwd=REPO,
        env=base,
        capture_output=True,
        text=True,
        check=False,
    )


def sys_executable() -> str:
    import sys

    return sys.executable


class RequireEdgeRestoreApprovalTests(unittest.TestCase):
    def test_refuses_empty(self) -> None:
        proc = _run()
        self.assertEqual(proc.returncode, 1)
        self.assertIn("retired", proc.stderr)
        self.assertIn(TOKEN, proc.stderr)

    def test_refuses_wrong_token(self) -> None:
        proc = _run("--token", "yes")
        self.assertEqual(proc.returncode, 1)

    def test_accepts_cli_token(self) -> None:
        proc = _run("--token", TOKEN)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("ok", proc.stdout)

    def test_accepts_env_token(self) -> None:
        proc = _run(env={"CONFIRM_EDGE_RESTORE": TOKEN})
        self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_cli_overrides_wrong_env(self) -> None:
        proc = _run("--token", TOKEN, env={"CONFIRM_EDGE_RESTORE": "nope"})
        self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_github_actions_emits_error_annotation(self) -> None:
        proc = _run(env={"GITHUB_ACTIONS": "true"})
        self.assertEqual(proc.returncode, 1)
        self.assertIn("::error::", proc.stderr)


if __name__ == "__main__":
    unittest.main()
