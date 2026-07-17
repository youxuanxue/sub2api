#!/usr/bin/env python3
"""Unit tests for probe_cc_geo_stego.py analyzer."""
from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

OPS = Path(__file__).resolve().parent
REPO_ROOT = OPS.parents[1]
PROBE = OPS / "probe_cc_geo_stego.py"


class TestProbeCCGeoStego(unittest.TestCase):
    def _run(self, records: list[dict], extra_args: list[str] | None = None) -> subprocess.CompletedProcess:
        with tempfile.NamedTemporaryFile("w", suffix=".jsonl", delete=False) as f:
            for rec in records:
                f.write(json.dumps(rec, ensure_ascii=False) + "\n")
            path = f.name
        args = [sys.executable, str(PROBE), path, "--json"]
        if extra_args:
            args.extend(extra_args)
        return subprocess.run(args, capture_output=True, text=True, check=False)

    def test_system_reminder_in_messages_needs_normalize(self) -> None:
        rec = {
            "scenario": "shanghai_tokenkey",
            "tz": "Asia/Shanghai",
            "host": "api.tokenkey.dev",
            "body": {
                "system": [{"type": "text", "text": "You are a Claude agent."}],
                "messages": [
                    {
                        "index": 0,
                        "role": "user",
                        "text_blocks": [
                            {
                                "index": 0,
                                "has_system_reminder": True,
                                "has_current_date": True,
                                "date_lines": ["Today\u2019s date is 2026/06/30."],
                                "head": "<system-reminder>",
                            }
                        ],
                    }
                ],
            },
        }
        cp = self._run([rec])
        self.assertEqual(cp.returncode, 0, cp.stderr)
        rows = json.loads(cp.stdout)
        self.assertTrue(rows[0]["needs_normalize"])
        self.assertEqual(rows[0]["date_lines"][0]["surface"], "messages[0].content[0].text")

    def test_canonical_shape_no_normalize(self) -> None:
        rec = {
            "scenario": "utc_tokenkey",
            "tz": "UTC",
            "host": "api.tokenkey.dev",
            "body": {
                "messages": [
                    {
                        "index": 0,
                        "role": "user",
                        "text_blocks": [
                            {
                                "index": 0,
                                "date_lines": ["Today's date is 2026-06-30."],
                            }
                        ],
                    }
                ],
            },
        }
        cp = self._run([rec], ["--check"])
        rows = json.loads(cp.stdout)
        self.assertFalse(rows[0]["needs_normalize"])
        self.assertEqual(cp.returncode, 0)

    def test_check_gateway_fixture_passes(self) -> None:
        fixture = OPS / "testdata" / "cc_geo_probe_fixture.jsonl"
        cp = subprocess.run(
            [sys.executable, str(PROBE), str(fixture.resolve()), "--check-gateway"],
            cwd=str(REPO_ROOT / "backend"),
            capture_output=True,
            text=True,
        )
        self.assertEqual(0, cp.returncode, cp.stdout + cp.stderr)

    def test_date_change_attachment_slash_needs_normalize(self) -> None:
        rec = {
            "scenario": "shanghai_tokenkey",
            "body": {
                "messages": [
                    {
                        "index": 0,
                        "role": "user",
                        "text_blocks": [],
                        "date_change_attachments": [{"content_index": 0, "newDate": "2026/06/30"}],
                    }
                ],
            },
        }
        cp = self._run([rec])
        rows = json.loads(cp.stdout)
        self.assertTrue(rows[0]["needs_normalize"])
        surfaces = [dl["surface"] for dl in rows[0]["date_lines"]]
        self.assertIn("messages[0].content[0].attachment.newDate", surfaces)


if __name__ == "__main__":
    unittest.main()
