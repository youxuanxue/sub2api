#!/usr/bin/env python3
"""Smoke tests for ops/observability/run-probe.sh — validation only (no AWS).

The script needs SSM to actually transport a script to a remote host; in
unit-test mode we only verify argv parsing, target validation, missing-script
detection, and that --help works. End-to-end execution belongs to integration
tests in the operator's session.

stdlib-only.
"""
from __future__ import annotations

import pathlib
import subprocess
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "run-probe.sh"


def _run(*args: str) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["bash", str(_SCRIPT), *args],
        capture_output=True, text=True, check=False,
    )


class RunProbeValidationTest(unittest.TestCase):
    def test_help_exits_zero(self) -> None:
        proc = _run("--help")
        self.assertEqual(proc.returncode, 0)
        self.assertIn("run-probe.sh", proc.stdout)
        self.assertIn("--target", proc.stdout)

    def test_missing_required_args(self) -> None:
        proc = _run()
        self.assertEqual(proc.returncode, 1)
        self.assertIn("--target and --script are required", proc.stderr)

    def test_unknown_arg_rejected(self) -> None:
        proc = _run("--bogus")
        self.assertEqual(proc.returncode, 1)
        self.assertIn("unknown arg", proc.stderr)

    def test_script_must_exist(self) -> None:
        proc = _run("--target", "prod", "--script", "/nonexistent/probe.sh")
        self.assertEqual(proc.returncode, 1)
        self.assertIn("script not found", proc.stderr)

    def test_invalid_target_shape(self) -> None:
        # Need an existing script to pass the prior check
        existing = pathlib.Path(__file__).resolve().parent / "probe-caps.sh"
        proc = _run("--target", "weird", "--script", str(existing))
        self.assertEqual(proc.returncode, 1)
        self.assertIn("--target must be", proc.stderr)


if __name__ == "__main__":
    unittest.main()
