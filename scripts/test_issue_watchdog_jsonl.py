#!/usr/bin/env python3
"""Regression tests for scripts/upstream/issue-watchdog.py load_jsonl.

Same producer/consumer contract as issue-triage-generate: JSONL records are
newline-delimited only; str.splitlines() must not be used because Unicode line
separators inside issue title/body would split valid JSON mid-string.
"""
from __future__ import annotations

import importlib.util
import json
import pathlib
import sys
import tempfile
import unittest

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "upstream" / "issue-watchdog.py"
_spec = importlib.util.spec_from_file_location("issue_watchdog", _MOD_PATH)
assert _spec and _spec.loader
mod = importlib.util.module_from_spec(_spec)
sys.modules[_spec.name] = mod
_spec.loader.exec_module(mod)


class IssueWatchdogJsonlTest(unittest.TestCase):
    def test_u2028_inside_string_value_does_not_split_record(self) -> None:
        issue = {
            "number": 99,
            "title": "upstream bug\u2028second visual line",
            "body": "repro",
            "state": "open",
        }
        line = json.dumps(issue, ensure_ascii=False)
        self.assertGreater(len((line + "\n").splitlines()), 1)
        self.assertEqual(len([s for s in (line + "\n").split("\n") if s.strip()]), 1)

        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "issues.jsonl"
            path.write_text(line + "\n", encoding="utf-8")
            rows = mod.load_jsonl(path)

        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["number"], 99)

    def test_multiple_records_and_blank_lines(self) -> None:
        lines = [
            json.dumps({"number": 1, "title": "a"}, ensure_ascii=False),
            "",
            json.dumps({"number": 2, "title": "b"}, ensure_ascii=False),
        ]
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "issues.jsonl"
            path.write_text("\n".join(lines) + "\n", encoding="utf-8")
            rows = mod.load_jsonl(path)

        self.assertEqual([row["number"] for row in rows], [1, 2])


if __name__ == "__main__":
    unittest.main()
