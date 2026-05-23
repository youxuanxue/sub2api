#!/usr/bin/env python3
"""Smoke tests for ops/observability/ops-error-triage.sh — validation only.

The script is shipped via run-probe.sh and executes psql inside the remote
TokenKey host, so a real run needs SSM. In unit-test mode we verify:
  - bash -n syntax check passes
  - env defaults are visible in the script body (no typo drift)
  - WINDOW_HOURS gets validated as a positive integer

stdlib-only.
"""
from __future__ import annotations

import pathlib
import subprocess
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "ops-error-triage.sh"


class OpsErrorTriageTest(unittest.TestCase):
    def test_syntax_clean(self) -> None:
        proc = subprocess.run(
            ["bash", "-n", str(_SCRIPT)],
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)

    def test_env_defaults_documented(self) -> None:
        # Sanity-check that the documented env names appear in the script body
        body = _SCRIPT.read_text()
        for name in ("WINDOW_HOURS", "PATH_FILTER", "MODEL_FILTER",
                     "STATUS_MIN", "TOP_KIND_LIMIT", "TOP_MIN_LIMIT"):
            self.assertIn(name, body, f"missing env: {name}")

    def test_window_hours_validation(self) -> None:
        # Run with a non-integer WINDOW_HOURS; should exit 2 before reaching psql
        proc = subprocess.run(
            ["bash", str(_SCRIPT)],
            env={"PATH": "/usr/bin:/bin", "WINDOW_HOURS": "abc"},
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(proc.returncode, 2)
        self.assertIn("WINDOW_HOURS not positive int", proc.stderr)


if __name__ == "__main__":
    unittest.main()
