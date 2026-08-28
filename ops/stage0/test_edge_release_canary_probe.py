#!/usr/bin/env python3
"""Contract tests for the read-only Edge release canary probe."""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import unittest

SCRIPT = pathlib.Path(__file__).resolve().parent / "edge_release_canary_probe.sh"


def run_probe(**facts: str) -> subprocess.CompletedProcess[str]:
    env = {**os.environ, "EDGE_RELEASE_CANARY_TEST_MODE": "1", **facts}
    return subprocess.run(
        ["bash", str(SCRIPT)], env=env, capture_output=True, text=True, check=False
    )


class EdgeReleaseCanaryProbeTest(unittest.TestCase):
    def test_probe_has_no_non_bootstrap_json_dependency(self) -> None:
        text = SCRIPT.read_text()
        self.assertNotIn("jq ", text)
        self.assertNotIn("numfmt", text)
        self.assertIn('printf "%.0f\\n", $2 * 1024', text)

    def test_emits_eligible_strict_json(self) -> None:
        proc = run_probe(
            TEST_MEM_AVAILABLE_BYTES="500000000",
            TEST_ACTIVE_WORKING_SET_BYTES="200000000",
            TEST_DISK_AVAILABLE_BYTES="6000000000",
            TEST_COMPLETED_REQUESTS_30M="7",
            TEST_OAUTH_ACCOUNT_COUNT="0",
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        payload = json.loads(proc.stdout)
        self.assertTrue(payload["eligible"])
        self.assertEqual(payload["memory_required_bytes"], 335_544_320)
        self.assertEqual(payload["memory_headroom_bytes"], 164_455_680)
        self.assertEqual(payload["oauth_account_count"], 0)

    def test_active_working_set_can_raise_memory_requirement(self) -> None:
        proc = run_probe(
            TEST_MEM_AVAILABLE_BYTES="450000000",
            TEST_ACTIVE_WORKING_SET_BYTES="350000000",
            TEST_DISK_AVAILABLE_BYTES="6000000000",
            TEST_COMPLETED_REQUESTS_30M="0",
            TEST_OAUTH_ACCOUNT_COUNT="2",
        )
        payload = json.loads(proc.stdout)
        self.assertFalse(payload["eligible"])
        self.assertEqual(payload["memory_required_bytes"], 484_217_728)
        self.assertIn("memory_below_required", payload["rejection_reasons"])

    def test_missing_required_fact_is_null_and_ineligible(self) -> None:
        proc = run_probe(
            TEST_MEM_AVAILABLE_BYTES="invalid",
            TEST_ACTIVE_WORKING_SET_BYTES="200000000",
            TEST_DISK_AVAILABLE_BYTES="6000000000",
            TEST_COMPLETED_REQUESTS_30M="0",
            TEST_OAUTH_ACCOUNT_COUNT="0",
        )
        payload = json.loads(proc.stdout)
        self.assertIsNone(payload["mem_available_bytes"])
        self.assertFalse(payload["eligible"])
        self.assertIn("mem_available_unknown", payload["rejection_reasons"])

    def test_disk_floor_is_hard_but_oauth_is_not(self) -> None:
        proc = run_probe(
            TEST_MEM_AVAILABLE_BYTES="500000000",
            TEST_ACTIVE_WORKING_SET_BYTES="200000000",
            TEST_DISK_AVAILABLE_BYTES="5368709119",
            TEST_COMPLETED_REQUESTS_30M="0",
            TEST_OAUTH_ACCOUNT_COUNT="invalid",
        )
        payload = json.loads(proc.stdout)
        self.assertFalse(payload["eligible"])
        self.assertIn("disk_below_required", payload["rejection_reasons"])
        self.assertIsNone(payload["oauth_account_count"])
        self.assertNotIn("oauth_account_count_unknown", payload["rejection_reasons"])


if __name__ == "__main__":
    unittest.main()
