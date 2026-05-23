#!/usr/bin/env python3
"""Unit tests for parse-access-log.py.

stdlib-only (unittest + subprocess); CI runs `python3 -m unittest discover -s ops`.
"""
from __future__ import annotations

import json
import pathlib
import subprocess
import sys
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "parse-access-log.py"


def _run(stdin_text: str, extra_args: list[str]) -> dict:
    proc = subprocess.run(
        [sys.executable, str(_SCRIPT), "--stdin", *extra_args],
        input=stdin_text,
        capture_output=True,
        text=True,
        check=True,
    )
    return json.loads(proc.stdout)


class ParseAccessLogTest(unittest.TestCase):
    SAMPLE = (
        # Two completed-events on the same minute, one 200 one 503
        'INFO http request completed {"path":"/v1/messages","model":"claude-sonnet-4-6",'
        '"status_code":200,"latency_ms":1234,"completed_at":"2026-05-23T12:34:56.789Z"}\n'
        'INFO http request completed {"path":"/v1/messages","model":"claude-sonnet-4-6",'
        '"status_code":503,"latency_ms":120,"completed_at":"2026-05-23T12:34:59.111Z"}\n'
        # A non-matching line (path filter excludes /v1/models)
        'INFO http request completed {"path":"/v1/models","model":"claude-sonnet-4-6",'
        '"status_code":200,"latency_ms":50,"completed_at":"2026-05-23T12:35:01.000Z"}\n'
        # A marker-only line (no JSON, should not parse but should count marker)
        "GROUP_RPM_EXCEEDED account=foo\n"
    )

    def test_basic_aggregation(self) -> None:
        out = _run(self.SAMPLE, ["--path", "/v1/messages"])
        self.assertEqual(out["totals"]["lines_parsed"], 2)
        self.assertEqual(out["status_counts"], {"200": 1, "503": 1})
        self.assertEqual(out["totals"]["bad_count"], 1)  # 503 only (status_min=400)
        self.assertEqual(out["markers"]["GROUP_RPM_EXCEEDED"], 1)
        # latency: 120 < 1234, p50=120 max=1234
        self.assertEqual(out["latency_ms"]["n"], 2)
        self.assertEqual(out["latency_ms"]["max"], 1234)
        self.assertEqual(out["latency_ms"]["p50"], 120)

    def test_path_filter_excludes(self) -> None:
        out = _run(self.SAMPLE, ["--path", "/v1/messages"])
        # /v1/models row should NOT appear in by_model_status
        models = {row["model"] for row in out["by_model_status"]}
        self.assertEqual(models, {"claude-sonnet-4-6"})
        # And only the two /v1/messages rows are in by_minute
        minutes = {(r["minute_utc"], r["status_code"]) for r in out["by_minute"]}
        self.assertIn(("2026-05-23T12:34:00Z", 200), minutes)
        self.assertIn(("2026-05-23T12:34:00Z", 503), minutes)
        self.assertNotIn(("2026-05-23T12:35:00Z", 200), minutes)

    def test_empty_input_is_clean(self) -> None:
        out = _run("", [])
        self.assertEqual(out["totals"]["lines_parsed"], 0)
        self.assertEqual(out["status_counts"], {})
        self.assertIsNone(out["latency_ms"])

    def test_determinism(self) -> None:
        # Same input ⇒ byte-identical output (stable sort)
        a = subprocess.run(
            [sys.executable, str(_SCRIPT), "--stdin"],
            input=self.SAMPLE, capture_output=True, text=True, check=True,
        ).stdout
        b = subprocess.run(
            [sys.executable, str(_SCRIPT), "--stdin"],
            input=self.SAMPLE, capture_output=True, text=True, check=True,
        ).stdout
        self.assertEqual(a, b)


if __name__ == "__main__":
    unittest.main()
