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
import tempfile
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

    def test_upstream_event_query_keeps_reason_and_bounded_message(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            fake_docker = pathlib.Path(tmp) / "docker"
            fake_docker.write_text(
                "#!/bin/sh\n"
                "case \"$*\" in\n"
                "  *\"ev->>'reason'\"*\"ev->>'message'\"*)\n"
                "    printf '%s\\n' '{\"kind\":\"response_error\",\"reason\":\"empty_response\",\"message\":\"kiro upstream returned an empty response\"}' ;;\n"
                "  *) printf '%s\\n' '{}' ;;\n"
                "esac\n",
                encoding="utf-8",
            )
            fake_docker.chmod(0o755)
            proc = subprocess.run(
                ["/bin/bash", str(_SCRIPT)],
                env={"PATH": f"{tmp}:/usr/bin:/bin", "WINDOW_MINUTES": "5"},
                capture_output=True,
                text=True,
                check=False,
            )

        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        upstream_section = proc.stdout.split("=== upstream_events ===", 1)[1]
        upstream_section = upstream_section.split("=== by_minute_429 ===", 1)[0]
        self.assertIn('"reason":"empty_response"', upstream_section)
        self.assertIn('"message":"kiro upstream returned an empty response"', upstream_section)


if __name__ == "__main__":
    unittest.main()
