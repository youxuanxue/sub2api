#!/usr/bin/env python3
"""Regression tests for scripts/anthropic/cc-issue-triage-generate.py.

Locks the gateway-relevance filter that keeps the ~62k claude-code issues from
flooding TokenKey triage with client-side noise:
  - EXCLUDE (IDE/terminal/MCP/plugin/install) drops to not_applicable;
  - GATEWAY_HIGH (beta/400/429/OAuth/model-lifecycle) → needs_review;
  - GATEWAY_HIGH wins over a co-occurring EXCLUDE marker (the load-bearing
    false-negative guard: "VSCode: 429 on opus-4.8" is a real relay signal);
  - no-signal → unknown_low_signal/not_applicable (never a candidate);
  - MANUAL_TRIAGE pins #61348 to fixed_in_tokenkey;
  - the JSONL consumer splits only on "\\n" (U+2028 inside a string must not
    split a record — same regression locked for the upstream triage script).
"""
from __future__ import annotations

import importlib.util
import json
import pathlib
import sys
import tempfile
import unittest

_MOD_PATH = (
    pathlib.Path(__file__).resolve().parent / "anthropic" / "cc-issue-triage-generate.py"
)
_spec = importlib.util.spec_from_file_location("cc_issue_triage_generate", _MOD_PATH)
assert _spec and _spec.loader
mod = importlib.util.module_from_spec(_spec)
sys.modules[_spec.name] = mod
_spec.loader.exec_module(mod)


class RelevanceFilterTest(unittest.TestCase):
    def _impact(self, number: int, title: str, body: str = "") -> dict:
        return mod.classify(
            {"number": number, "title": title, "body": body, "html_url": "u", "updated_at": "t"}
        )

    def test_relevance_table(self) -> None:
        cases = [
            # client-side noise → not our surface
            ("VSCode extension freezes on large file", "not_applicable"),
            ("Terminal flickers when scrolling output", "not_applicable"),
            ("MCP server connection drops", "not_applicable"),
            ("slash command /compact not working", "not_applicable"),
            ("npm install fails on Windows WSL", "not_applicable"),
            ("Feature request: dark mode for the TUI", "not_applicable"),
            # gateway-relevant → needs_review
            ("400 invalid_request: thinking.type.enabled not supported on opus-4.8", "needs_review"),
            ("429 overloaded errors spamming since today", "needs_review"),
            ("OAuth token refresh 401 loop", "needs_review"),
            ("anthropic-beta header missing breaks prompt caching", "needs_review"),
            # gateway-medium
            ("SSE stream idle timeout after 60s", "medium"),
            # no signal at all
            ("Question about pricing tiers", "unknown_low_signal"),
        ]
        for i, (title, want) in enumerate(cases):
            with self.subTest(title=title):
                self.assertEqual(self._impact(1000 + i, title)["impact"], want)

    def test_high_signal_wins_over_exclude_marker(self) -> None:
        # The single most important precedence case: a real relay 400/429 must NOT
        # be dropped just because the reporter mentions their IDE.
        entry = self._impact(2001, "VSCode: 429 overloaded on opus-4.8")
        self.assertEqual(entry["impact"], "needs_review")
        self.assertEqual(entry["tokenkey_status"], "candidate_unverified")
        self.assertIn("rate_limit", entry["categories"])

    def test_keyword_match_never_auto_promotes(self) -> None:
        # Automated keyword hits must stay needs_review (the watchdog only promotes
        # impact in {critical,high}); never let a keyword become a fix candidate.
        entry = self._impact(2002, "529 overloaded on /v1/messages")
        self.assertNotIn(entry["impact"], {"high", "critical"})

    def test_manual_triage_override(self) -> None:
        entry = self._impact(61348, "irrelevant title that matches nothing")
        self.assertEqual(entry["impact"], "fixed")
        self.assertEqual(entry["tokenkey_status"], "fixed_in_tokenkey")
        self.assertEqual(entry["upstream"], "anthropics/claude-code#61348")


class JsonlIntegrityTest(unittest.TestCase):
    def _run_main(self, jsonl_text: str) -> dict:
        with tempfile.TemporaryDirectory() as tmp:
            src = pathlib.Path(tmp) / "issues.jsonl"
            dst = pathlib.Path(tmp) / "triage.json"
            src.write_text(jsonl_text, encoding="utf-8")
            argv = ["cc-issue-triage-generate.py", str(src), str(dst)]
            old = sys.argv
            try:
                sys.argv = argv
                rc = mod.main()
            finally:
                sys.argv = old
            self.assertEqual(rc, 0)
            return json.loads(dst.read_text(encoding="utf-8"))

    def test_u2028_inside_string_value_does_not_split_record(self) -> None:
        issue = {
            "number": 1,
            # Raw U+2028 LINE SEPARATOR inside the title — valid JSON, single
            # producer line, but str.splitlines() would cut it in two.
            "title": "400 invalid_request" + "\u2028" + "second visual line",
            "body": "repro",
            "html_url": "https://github.com/anthropics/claude-code/issues/1",
            "updated_at": "2026-05-27T00:00:00Z",
        }
        line = json.dumps(issue, ensure_ascii=False)
        # Guard: the input genuinely exercises the bug.
        self.assertGreater(len((line + "\n").splitlines()), 1)
        self.assertEqual(len([s for s in (line + "\n").split("\n") if s.strip()]), 1)
        data = self._run_main(line + "\n")
        self.assertEqual(len(data["issues"]), 1)
        self.assertEqual(data["issues"][0]["upstream"], "anthropics/claude-code#1")

    def test_counts_total_parsed_issues(self) -> None:
        lines = [
            json.dumps({"number": 2, "title": "a", "body": ""}, ensure_ascii=False),
            "",
            json.dumps({"number": 3, "title": "b", "body": ""}, ensure_ascii=False),
        ]
        data = self._run_main("\n".join(lines) + "\n")
        self.assertEqual(len(data["issues"]), 2)
        self.assertEqual(sum(data["counts"].values()), 2)


if __name__ == "__main__":
    unittest.main()
