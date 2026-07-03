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

    def test_analyze_tool_result_only_shape(self) -> None:
        records = [
            {
                "scenario": "incident_tool_result_only",
                "body_wire": {
                    "messages": [
                        {
                            "role": "user",
                            "content": [
                                {
                                    "type": "tool_result",
                                    "tool_use_id": "toolu_01",
                                    "content": "raw json:\n-rw-r--r-- data.json",
                                    "is_error": False,
                                }
                            ],
                        }
                    ]
                },
            },
            {
                "scenario": "sdk_tool_continuation",
                "body_wire": {
                    "system": [
                        {
                            "type": "text",
                            "text": "You are a Claude agent, built on Anthropic's Claude Agent SDK.",
                        }
                    ],
                    "messages": [
                        {
                            "role": "user",
                            "content": [
                                {"type": "text", "text": "<system-reminder>\nToday's date is 2026-07-03.\n</system-reminder>"}
                            ],
                        },
                        {
                            "role": "assistant",
                            "content": [{"type": "tool_use", "id": "toolu_01", "name": "Bash", "input": {}}],
                        },
                        {
                            "role": "user",
                            "content": [{"type": "tool_result", "tool_use_id": "toolu_01", "content": "ok"}],
                        },
                    ],
                },
            },
            {
                "scenario": "non_immediate_tool_result",
                "body_wire": {
                    "system": [
                        {
                            "type": "text",
                            "text": "You are a Claude agent, built on Anthropic's Claude Agent SDK.",
                        }
                    ],
                    "messages": [
                        {
                            "role": "assistant",
                            "content": [{"type": "tool_use", "id": "toolu_01", "name": "Bash", "input": {}}],
                        },
                        {
                            "role": "user",
                            "content": [{"type": "text", "text": "unrelated"}],
                        },
                        {
                            "role": "user",
                            "content": [{"type": "tool_result", "tool_use_id": "toolu_01", "content": "late"}],
                        },
                    ],
                },
            },
            {
                "scenario": "raw_body_system_reminder_tool_result",
                "body": {
                    "messages": [
                        {
                            "role": "user",
                            "content": [
                                {
                                    "type": "text",
                                    "text": "<system-reminder>\nToday's date is 2026-07-03.\n</system-reminder>",
                                }
                            ],
                        },
                        {
                            "role": "assistant",
                            "content": [{"type": "tool_use", "id": "toolu_01", "name": "Bash", "input": {}}],
                        },
                        {
                            "role": "user",
                            "content": [{"type": "tool_result", "tool_use_id": "toolu_01", "content": "ok"}],
                        },
                    ]
                },
            },
            {
                "scenario": "duplicate_tool_result",
                "body_wire": {
                    "messages": [
                        {
                            "role": "assistant",
                            "content": [{"type": "tool_use", "id": "toolu_01", "name": "Bash", "input": {}}],
                        },
                        {
                            "role": "user",
                            "content": [
                                {"type": "tool_result", "tool_use_id": "toolu_01", "content": "first"},
                                {"type": "tool_result", "tool_use_id": "toolu_01", "content": "second"},
                            ],
                        },
                    ]
                },
            },
        ]
        with tempfile.NamedTemporaryFile("w", suffix=".jsonl", delete=False) as f:
            for rec in records:
                f.write(json.dumps(rec) + "\n")
            path = Path(f.name)
        proc = subprocess.run(
            [sys.executable, str(PROBE), str(path), "--json"],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        rows = json.loads(proc.stdout)
        incident = next(r for r in rows if r.get("scenario") == "incident_tool_result_only")
        self.assertTrue(incident["has_tool_result"])
        self.assertFalse(incident["has_assistant_tool_use"])
        self.assertIn("orphan_tool_result_context", incident["unknown_surfaces"])
        self.assertIn("tool_result_without_system_surface", incident["unknown_surfaces"])
        sdk = next(r for r in rows if r.get("scenario") == "sdk_tool_continuation")
        self.assertTrue(sdk["has_tool_result"])
        self.assertTrue(sdk["has_assistant_tool_use"])
        self.assertNotIn("orphan_tool_result_context", sdk["unknown_surfaces"])
        non_immediate = next(r for r in rows if r.get("scenario") == "non_immediate_tool_result")
        self.assertTrue(non_immediate["has_tool_result"])
        self.assertTrue(non_immediate["has_assistant_tool_use"])
        self.assertIn("orphan_tool_result_context", non_immediate["unknown_surfaces"])
        raw_body = next(r for r in rows if r.get("scenario") == "raw_body_system_reminder_tool_result")
        self.assertTrue(raw_body["has_tool_result"])
        self.assertNotIn("tool_result_without_system_surface", raw_body["unknown_surfaces"])
        duplicate = next(r for r in rows if r.get("scenario") == "duplicate_tool_result")
        self.assertTrue(duplicate["has_tool_result"])
        self.assertIn("duplicate_tool_result_for_tool_use", duplicate["unknown_surfaces"])


class TestPromptSurfaceAggregate(unittest.TestCase):
    def test_flags_unknown_surfaces(self) -> None:
        payload = {
            "fingerprints": [
                {
                    "surface_signature": "abc",
                    "reminder_date_line_class": "SLASH_UNICODE",
                    "identity_anchor_id": "claude_code_cli",
                    "unknown_surfaces": "geo_stego_date_line",
                    "has_tool_result": True,
                    "has_assistant_tool_use": False,
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
        self.assertIn("tool_result_without_assistant_tool_use: 1", proc.stdout)


if __name__ == "__main__":
    unittest.main()
