#!/usr/bin/env python3
"""Smoke tests for deploy/aws/stage0/local-bootstrap.sh — verify idempotency,
that the .env is never overwritten when it already exists (preserves
POSTGRES_PASSWORD across runs), and that --dry-run writes nothing.

stdlib-only.
"""
from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import unittest

_SCRIPT = pathlib.Path(__file__).resolve()
_REPO_ROOT = _SCRIPT.parents[3]


def _run(env: dict[str, str], *args: str) -> subprocess.CompletedProcess:
    merged = os.environ.copy()
    merged.update(env)
    return subprocess.run(
        ["bash", str(_REPO_ROOT / "deploy/aws/stage0/local-bootstrap.sh"), *args],
        env=merged, capture_output=True, text=True, check=False,
    )


class LocalBootstrapTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self._tmp.name) / "stage0-local"
        # ensure REPO_ROOT is resolvable (cwd-agnostic by setting explicitly)
        self.env = {
            "REPO_ROOT": str(_REPO_ROOT),
            "TOKENKEY_STAGE0_LOCAL_ROOT": str(self.root),
            "HOME": str(self._tmp.name),  # so --reset's $HOME check accepts it
        }

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def test_dry_run_writes_nothing(self) -> None:
        proc = _run(self.env, "--dry-run")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        # Root dir must not exist after dry-run (we created tmp but never wrote)
        self.assertFalse(self.root.exists(), "dry-run should not create the root")

    def test_initial_bootstrap_creates_files(self) -> None:
        proc = _run(self.env)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        for sub in ("caddy", "app", "postgres", "pgdump", "redis"):
            self.assertTrue((self.root / sub).is_dir(), f"missing dir: {sub}")
        self.assertTrue((self.root / ".env").is_file())
        self.assertTrue((self.root / "caddy/Caddyfile").is_file())
        self.assertTrue((self.root / "docker-compose.override.yml").is_file())
        # .env permissions should be 600
        mode = (self.root / ".env").stat().st_mode & 0o777
        self.assertEqual(mode, 0o600)
        # Password is not in any of the script's stdout/stderr
        env_text = (self.root / ".env").read_text()
        self.assertIn("POSTGRES_PASSWORD=", env_text)
        # Extract the password and confirm it is NOT echoed anywhere
        pw_line = [ln for ln in env_text.splitlines() if ln.startswith("POSTGRES_PASSWORD=")][0]
        pw = pw_line.split("=", 1)[1]
        self.assertNotIn(pw, proc.stdout)
        self.assertNotIn(pw, proc.stderr)

    def test_second_run_preserves_env(self) -> None:
        proc1 = _run(self.env)
        self.assertEqual(proc1.returncode, 0, msg=proc1.stderr)
        env_before = (self.root / ".env").read_text()
        # Re-run; .env should be identical
        proc2 = _run(self.env)
        self.assertEqual(proc2.returncode, 0, msg=proc2.stderr)
        env_after = (self.root / ".env").read_text()
        self.assertEqual(env_before, env_after, "second run must not regenerate .env")
        self.assertIn("preserving", proc2.stdout)

    def test_reset_wipes_root(self) -> None:
        _run(self.env)
        (self.root / "extra.txt").write_text("hi")
        proc = _run(self.env, "--reset")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertFalse((self.root / "extra.txt").exists())
        # After reset, .env should be freshly generated
        self.assertTrue((self.root / ".env").is_file())

    def test_reset_refuses_outside_home(self) -> None:
        env = dict(self.env)
        env["TOKENKEY_STAGE0_LOCAL_ROOT"] = "/tmp/danger-zone-local-bootstrap-test"
        proc = _run(env, "--reset")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("refusing --reset", proc.stderr)


if __name__ == "__main__":
    unittest.main()
