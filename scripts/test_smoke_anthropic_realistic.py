#!/usr/bin/env python3
"""Tests for ops/stage0/smoke_anthropic_realistic.py."""
from __future__ import annotations

import json
import subprocess
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
REALISTIC = REPO_ROOT / "ops/stage0/smoke_anthropic_realistic.py"


class SmokeAnthropicRealisticTest(unittest.TestCase):
    def test_builds_parseable_metadata_user_id(self) -> None:
        out = subprocess.check_output(
            [str(REALISTIC), "--model", "claude-sonnet-4-6", "--max-tokens", "16"],
            text=True,
        )
        payload = json.loads(out)
        user_id = payload["metadata"]["user_id"]
        parsed = json.loads(user_id)
        self.assertEqual(len(parsed["device_id"]), 64)
        self.assertTrue(parsed["session_id"])
        self.assertIn("cache_control", payload["system"][0])
        self.assertIn("cache_control", payload["messages"][0]["content"][0])
        self.assertEqual(payload["messages"][0]["content"][0]["text"], "hi")

    def test_session_id_override(self) -> None:
        out = subprocess.check_output(
            [
                str(REALISTIC),
                "--model",
                "claude-sonnet-4-6",
                "--session-id",
                "probe-session-1",
            ],
            text=True,
        )
        payload = json.loads(out)
        parsed = json.loads(payload["metadata"]["user_id"])
        self.assertEqual(parsed["session_id"], "probe-session-1")


if __name__ == "__main__":
    unittest.main()
