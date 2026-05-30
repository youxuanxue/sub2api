#!/usr/bin/env python3
"""Unit tests for ops/anthropic/capture_cc_fingerprint.py (stdlib unittest)."""
from __future__ import annotations

import importlib.util
import json
import pathlib
import tempfile
import unittest

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "capture_cc_fingerprint.py"
_spec = importlib.util.spec_from_file_location("capture_cc_fingerprint", _MOD_PATH)
mod = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
import sys

sys.modules[_spec.name] = mod
_spec.loader.exec_module(mod)


class CaptureCCFingerprintTest(unittest.TestCase):
    def test_load_tokenkey_baseline_has_expected_keys(self) -> None:
        baseline = mod.load_tokenkey_baseline(mod.REPO_ROOT)
        self.assertEqual(baseline["tls"]["ja3_hash"], "d871d02cecbde59abbf8f4806134addf")
        self.assertEqual(baseline["canonical_http"]["default_version"], "2.1.157")
        self.assertEqual(baseline["mimic_http"]["stainless_package_version"], "0.94.0")
        self.assertIn("claude-code-20250219", baseline["betas"]["sonnet_mimicry"])
        self.assertNotIn("effort-2025-11-24", baseline["betas"]["sonnet_mimicry"])
        self.assertIn("thinking-token-count-2026-05-13", baseline["betas"]["haiku_mimicry"])
        self.assertNotIn("effort-2025-11-24", baseline["betas"]["haiku_mimicry"])

    def test_diff_all_match_when_capture_matches_baseline(self) -> None:
        baseline = mod.load_tokenkey_baseline(mod.REPO_ROOT)
        capture = {
            "schema_version": 1,
            "cc_version": baseline["canonical_http"]["default_version"],
            "tls": {
                "ja3_hash": baseline["tls"]["ja3_hash"],
                "ja3_raw": baseline["tls"]["ja3_raw"],
                "stainless_package_version": baseline["canonical_http"]["stainless_package_version"],
            },
            "http": {
                "haiku": {
                    "anthropic_beta": ",".join(baseline["betas"]["haiku_mimicry"]),
                },
                "sonnet": {
                    "anthropic_beta": ",".join(baseline["betas"]["sonnet_mimicry"]),
                },
            },
        }
        rows = mod.diff_baseline_vs_capture(baseline, capture)
        self.assertFalse(mod.has_actionable_mismatch(rows))
        self.assertTrue(all(r.status == "match" for r in rows if not r.field.startswith("mimic.cli")))

    def test_diff_flags_stainless_mismatch(self) -> None:
        baseline = mod.load_tokenkey_baseline(mod.REPO_ROOT)
        capture = {
            "schema_version": 1,
            "cc_version": baseline["canonical_http"]["default_version"],
            "tls": {
                "ja3_hash": baseline["tls"]["ja3_hash"],
                "ja3_raw": baseline["tls"]["ja3_raw"],
                "stainless_package_version": "0.70.0",
            },
            "http": {},
        }
        rows = mod.diff_baseline_vs_capture(baseline, capture)
        self.assertTrue(mod.has_actionable_mismatch(rows))
        stainless_rows = [r for r in rows if "stainless" in r.field]
        self.assertTrue(any(r.status == "mismatch" for r in stainless_rows))

    def test_diff_flags_ja3_mismatch_with_tls_action_note(self) -> None:
        baseline = mod.load_tokenkey_baseline(mod.REPO_ROOT)
        capture = {
            "schema_version": 1,
            "cc_version": baseline["canonical_http"]["default_version"],
            "tls": {"ja3_hash": "deadbeef", "ja3_raw": "771"},
            "http": {},
        }
        rows = mod.diff_baseline_vs_capture(baseline, capture)
        report = mod.format_diff_report(rows)
        self.assertIn("action_tls", report)
        ja3_row = next(r for r in rows if r.field == "tls.ja3_hash")
        self.assertEqual(ja3_row.status, "mismatch")

    def test_bundle_roundtrip_write_and_load(self) -> None:
        baseline = mod.load_tokenkey_baseline(mod.REPO_ROOT)
        bundle = mod.bundle_from_artifacts(
            cc_version=baseline["canonical_http"]["default_version"],
            tls_observed={
                "ja3_hash": baseline["tls"]["ja3_hash"],
                "ja3_raw": baseline["tls"]["ja3_raw"],
                "stainless_package_version": "0.94.0",
            },
            http_by_variant={
                "haiku": {"anthropic_beta": ",".join(baseline["betas"]["haiku_mimicry"])},
            },
        )
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "bundle.json"
            path.write_text(json.dumps(bundle), encoding="utf-8")
            loaded = mod.load_capture_bundle(path)
            self.assertEqual(loaded["cc_version"], baseline["canonical_http"]["default_version"])

    def test_has_tls_mismatch_true_only_for_tls_fields(self) -> None:
        baseline = mod.load_tokenkey_baseline(mod.REPO_ROOT)
        tls_rows = mod.diff_baseline_vs_capture(
            baseline,
            {
                "schema_version": 1,
                "cc_version": baseline["canonical_http"]["default_version"],
                "tls": {"ja3_hash": "deadbeef", "ja3_raw": "771"},
                "http": {},
            },
        )
        self.assertTrue(mod.has_tls_mismatch(tls_rows))
        http_rows = mod.diff_baseline_vs_capture(
            baseline,
            {
                "schema_version": 1,
                "cc_version": baseline["canonical_http"]["default_version"],
                "tls": {
                    "ja3_hash": baseline["tls"]["ja3_hash"],
                    "ja3_raw": baseline["tls"]["ja3_raw"],
                    "stainless_package_version": "0.70.0",
                },
                "http": {},
            },
        )
        self.assertFalse(mod.has_tls_mismatch(http_rows))
        self.assertTrue(mod.has_actionable_mismatch(http_rows))

    def test_check_env_reports_launcher_paths(self) -> None:
        rows = mod.run_check_env(relax_desktop=True, skip_egress=True)
        components = {r.component for r in rows}
        self.assertIn("cc0-here", components)
        self.assertIn("claude0-here", components)
        self.assertIn("cc0.gost", components)

    def test_write_tls_drift_spec_creates_markdown(self) -> None:
        baseline = mod.load_tokenkey_baseline(mod.REPO_ROOT)
        bundle = mod.bundle_from_artifacts(
            cc_version=baseline["canonical_http"]["default_version"],
            tls_observed={
                "ja3_hash": "deadbeefdeadbeefdeadbeefdeadbeef",
                "ja3_raw": "771,4865",
            },
            http_by_variant={},
        )
        with tempfile.TemporaryDirectory() as tmp:
            bundle_path = pathlib.Path(tmp) / "bundle.json"
            bundle_path.write_text(json.dumps(bundle), encoding="utf-8")
            out = mod.write_tls_drift_spec(
                bundle_path=bundle_path,
                repo_root=mod.REPO_ROOT,
                out_path=pathlib.Path(tmp) / "spec-delta.md",
            )
            text = out.read_text(encoding="utf-8")
            self.assertIn("ja3 drift", text)
            self.assertIn("deadbeef", text)

    def test_load_http_log_parses_cc_capture_prefix(self) -> None:
        line = (
            'CC_CAPTURE {"model":"claude-haiku-4-5-20251001",'
            '"anthropic_beta":"oauth-2025-04-20,claude-code-20250219"}'
        )
        with tempfile.TemporaryDirectory() as tmp:
            log = pathlib.Path(tmp) / "http.log"
            log.write_text(line + "\n", encoding="utf-8")
            picked = mod.load_http_log(log)
            self.assertIn("haiku", picked)
            self.assertIn("oauth-2025-04-20", picked["haiku"]["anthropic_beta"])

    def test_load_http_log_last_wins_per_variant(self) -> None:
        lines = [
            'CC_CAPTURE {"model":"claude-haiku-4-5-20251001","anthropic_beta":"legacy-variant"}',
            'CC_CAPTURE {"model":"claude-haiku-4-5-20251001","anthropic_beta":"dominant-variant"}',
            'CC_CAPTURE {"model":"claude-sonnet-4-20250514","anthropic_beta":"sonnet-first"}',
            'CC_CAPTURE {"model":"claude-sonnet-4-20250514","anthropic_beta":"sonnet-last"}',
            'CC_CAPTURE {"model":"claude-opus-4-5-20251101","anthropic_beta":"opus-with-effort"}',
        ]
        with tempfile.TemporaryDirectory() as tmp:
            log = pathlib.Path(tmp) / "http.log"
            log.write_text("\n".join(lines) + "\n", encoding="utf-8")
            picked = mod.load_http_log(log)
            self.assertEqual("dominant-variant", picked["haiku"]["anthropic_beta"])
            self.assertEqual("sonnet-last", picked["sonnet"]["anthropic_beta"])
            self.assertEqual("opus-with-effort", picked["opus"]["anthropic_beta"])


if __name__ == "__main__":
    unittest.main()
