#!/usr/bin/env python3
"""Tests for prompt surface registry/probe/aggregate tooling."""
from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
OPS = REPO_ROOT / "ops" / "anthropic"
OBS = REPO_ROOT / "ops" / "observability"
PROBE = OPS / "probe_prompt_surfaces.py"
AGG = OBS / "prompt_surface_aggregate.py"
REGISTRY = OPS / "prompt_surface_registry.json"
FIXTURE = OPS / "testdata" / "prompt_surface_probe_fixture.jsonl"


class TestPromptSurfaceRegistry(unittest.TestCase):
    def test_registry_valid(self) -> None:
        proc = subprocess.run(
            [sys.executable, str(PROBE), "--check-registry"],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)

    def test_fixture_gateway_coverage(self) -> None:
        proc = subprocess.run(
            [sys.executable, str(PROBE), "--check-fixture-gateway"],
            cwd=str(REPO_ROOT),
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)


class TestPromptSurfaceAnalyze(unittest.TestCase):
    def test_analyze_system_anchor(self) -> None:
        proc = subprocess.run(
            [sys.executable, str(PROBE), str(FIXTURE), "--json"],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        rows = json.loads(proc.stdout)
        billing = next(r for r in rows if r.get("scenario") == "billing_block_tokenkey")
        self.assertEqual(billing["identity_anchor_id"], "claude_code_cli")
        self.assertTrue(billing["billing_prefix_present"])


class TestPromptSurfaceAggregate(unittest.TestCase):
    def test_flags_unknown_surfaces(self) -> None:
        payload = {
            "fingerprints": [
                {
                    "surface_signature": "abc",
                    "reminder_date_line_class": "SLASH_UNICODE",
                    "identity_anchor_id": "claude_code_cli",
                    "unknown_surfaces": "geo_stego_date_line",
                }
            ]
        }
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump(payload, f)
            path = Path(f.name)
        proc = subprocess.run(
            [sys.executable, str(AGG), "--input", str(path)],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 1)
        self.assertIn("actionable_drift", proc.stdout)


if __name__ == "__main__":
    unittest.main()
