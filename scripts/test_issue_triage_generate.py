#!/usr/bin/env python3
"""Regression tests for scripts/upstream/issue-triage-generate.py.

Locks the fix for the Upstream Issue Watchdog crash: the consumer must split
the producer JSONL only on "\\n", not str.splitlines(), because the latter also
breaks on Unicode line boundaries (U+2028/U+2029/U+0085). json.dumps with
ensure_ascii=False keeps those raw inside string values, so an upstream issue
title/body containing U+2028 used to split one valid JSON record across two
"lines" and raise json.JSONDecodeError: Unterminated string.
"""
from __future__ import annotations

import importlib.util
import json
import pathlib
import sys
import tempfile
import unittest

_MOD_PATH = (
    pathlib.Path(__file__).resolve().parent / "upstream" / "issue-triage-generate.py"
)
_spec = importlib.util.spec_from_file_location("issue_triage_generate", _MOD_PATH)
assert _spec and _spec.loader
mod = importlib.util.module_from_spec(_spec)
sys.modules[_spec.name] = mod
_spec.loader.exec_module(mod)


class IssueTriageGenerateTest(unittest.TestCase):
    def _run_main(self, jsonl_text: str) -> dict:
        with tempfile.TemporaryDirectory() as tmp:
            src = pathlib.Path(tmp) / "issues.jsonl"
            dst = pathlib.Path(tmp) / "triage.json"
            src.write_text(jsonl_text, encoding="utf-8")
            argv = ["issue-triage-generate.py", str(src), str(dst)]
            old = sys.argv
            try:
                sys.argv = argv
                rc = mod.main()
            finally:
                sys.argv = old
            self.assertEqual(rc, 0)
            return json.loads(dst.read_text(encoding="utf-8"))

    def test_u2028_inside_string_value_does_not_split_record(self) -> None:
        # One issue whose title carries a raw U+2028 LINE SEPARATOR — valid JSON,
        # single producer line, but str.splitlines() would cut it in two.
        issue = {
            "number": 1,
            "title": "upstream bug second visual line",
            "body": "repro steps",
            "html_url": "https://github.com/Wei-Shaw/sub2api/issues/1",
            "updated_at": "2026-05-27T00:00:00Z",
        }
        line = json.dumps(issue, ensure_ascii=False)
        # Guard: the input genuinely exercises the bug — splitlines() over-splits,
        # split("\n") keeps it intact. If this ever stops holding, the regression
        # is no longer being tested.
        self.assertGreater(len((line + "\n").splitlines()), 1)
        self.assertEqual(len([s for s in (line + "\n").split("\n") if s.strip()]), 1)

        data = self._run_main(line + "\n")
        self.assertEqual(len(data["issues"]), 1)
        self.assertEqual(data["issues"][0]["upstream"], "Wei-Shaw/sub2api#1")

    def test_multiple_records_and_blank_lines(self) -> None:
        lines = [
            json.dumps({"number": 2, "title": "a", "body": ""}, ensure_ascii=False),
            "",
            json.dumps({"number": 3, "title": "b", "body": ""}, ensure_ascii=False),
        ]
        data = self._run_main("\n".join(lines) + "\n")
        self.assertEqual(len(data["issues"]), 2)
        self.assertEqual(
            sum(data["counts"].values()), 2, "counts must total the parsed issues"
        )


if __name__ == "__main__":
    unittest.main()
