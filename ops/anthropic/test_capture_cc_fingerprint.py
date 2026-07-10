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
        # default_version is derived from the Go const, which check-cc-version-sync.py
        # keeps == cc_version in the source JSON. Assert that invariant rather than
        # hard-coding the patch — so a cc bump never has to touch this test.
        source_cc = json.loads(
            (mod.REPO_ROOT / "deploy/aws/stage0/anthropic-http-mimicry-baselines.json")
            .read_text(encoding="utf-8")
        )["cc_version"]
        self.assertEqual(baseline["canonical_http"]["default_version"], source_cc)
        source_stainless = json.loads(
            (mod.REPO_ROOT / "deploy/aws/stage0/tk_canonical_cc_oauth.json")
            .read_text(encoding="utf-8")
        )["observed"]["stainless_package_version"]
        self.assertEqual(baseline["mimic_http"]["stainless_package_version"], source_stainless)
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
                    "x_stainless": {
                        "X-Stainless-Runtime-Version": baseline["canonical_http"][
                            "stainless_runtime_version"
                        ],
                        "X-Stainless-Package-Version": baseline["canonical_http"][
                            "stainless_package_version"
                        ],
                    },
                },
                "sonnet": {
                    "anthropic_beta": ",".join(baseline["betas"]["sonnet_mimicry"]),
                    "x_stainless": {
                        "X-Stainless-Runtime-Version": baseline["canonical_http"][
                            "stainless_runtime_version"
                        ],
                        "X-Stainless-Package-Version": baseline["canonical_http"][
                            "stainless_package_version"
                        ],
                    },
                },
            },
            "system": {
                "anchors": [
                    baseline["system"]["billing_prefix"] + ": cc_entrypoint=cli",
                    baseline["system"]["identity_prefixes"][0]
                    + ".\n\ndynamic cwd=/x git=main",
                ]
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

    def test_diff_flags_runtime_mismatch(self) -> None:
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
                    "x_stainless": {"X-Stainless-Runtime-Version": "v24.3.0"},
                },
            },
        }
        rows = mod.diff_baseline_vs_capture(baseline, capture)
        self.assertTrue(mod.has_actionable_mismatch(rows))
        runtime_rows = [r for r in rows if "runtime_version" in r.field]
        self.assertTrue(any(r.status == "mismatch" for r in runtime_rows))

    def test_june27_http_bundle_runtime_matches_baseline(self) -> None:
        bundle_path = mod.REPO_ROOT / ".tls_list/20260627T020627Z-cc-capture.bundle.json"
        if not bundle_path.is_file():
            self.skipTest("6/27 capture bundle not present locally")
        baseline = mod.load_tokenkey_baseline(mod.REPO_ROOT)
        capture = mod.load_capture_bundle(bundle_path)
        rows = mod.diff_baseline_vs_capture(baseline, capture)
        runtime_rows = [r for r in rows if "runtime_version" in r.field]
        self.assertTrue(runtime_rows, "expected runtime_version diff rows")
        self.assertTrue(all(r.status == "match" for r in runtime_rows))

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

class TLSObservedFromPcapTests(unittest.TestCase):
    def test_builds_observed_json_from_tshark_tsv(self) -> None:
        kiro = mod._load_kiro_ja3_engine()
        header = "\t".join(kiro.TSHARK_FIELDS)
        row = "\t".join(
            [
                "0x0303",
                "4865,4866,4867",
                "0,23,65281",
                "29,23,24",
                "0",
                "0x0403,0x0804",
                "h2,http/1.1",
                "0x0304,0x0303",
                "29",
                "1",
                "api.anthropic.com",
            ]
        )
        source_cc = json.loads(
            (mod.REPO_ROOT / "deploy/aws/stage0/anthropic-http-mimicry-baselines.json")
            .read_text(encoding="utf-8")
        )["cc_version"]
        observed = mod.tls_observed_from_tshark_tsv(
            header + "\n" + row + "\n",
            cc_version=source_cc,
            source="passive-pcap:test",
        )
        self.assertEqual(observed["user_agent"], f"claude-cli/{source_cc} (external, cli)")
        self.assertEqual(observed["server_name"], "api.anthropic.com")
        self.assertEqual(observed["source"], "passive-pcap:test")
        self.assertRegex(observed["ja3_hash"], r"^[a-f0-9]{32}$")
        self.assertTrue(observed["ja3_raw"].startswith("771,"))

    def test_diff_matches_baseline_ja3_from_pcap_observed(self) -> None:
        baseline = mod.load_tokenkey_baseline(mod.REPO_ROOT)
        capture = {
            "schema_version": 1,
            "cc_version": baseline["canonical_http"]["default_version"],
            "tls": {
                "ja3_hash": baseline["tls"]["ja3_hash"],
                "ja3_raw": baseline["tls"]["ja3_raw"],
            },
            "http": {},
        }
        rows = mod.diff_baseline_vs_capture(baseline, capture)
        ja3_row = next(r for r in rows if r.field == "tls.ja3_hash")
        self.assertEqual(ja3_row.status, "match")


class BundleRoundtripTests(unittest.TestCase):
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

    def test_load_http_log_dominant_variant_wins(self) -> None:
        # Haiku is bimodal (A x2, B x1) -> dominant A wins, deterministically,
        # not the last sample (B). Sonnet is unimodal.
        lines = [
            'CC_CAPTURE {"model":"claude-haiku-4-5-20251001","anthropic_beta":"variant-A"}',
            'CC_CAPTURE {"model":"claude-haiku-4-5-20251001","anthropic_beta":"variant-B"}',
            'CC_CAPTURE {"model":"claude-haiku-4-5-20251001","anthropic_beta":"variant-A"}',
            'CC_CAPTURE {"model":"claude-sonnet-4-6","anthropic_beta":"sonnet-only"}',
            'CC_CAPTURE {"model":"claude-opus-4-5-20251101","anthropic_beta":"opus-only"}',
        ]
        with tempfile.TemporaryDirectory() as tmp:
            log = pathlib.Path(tmp) / "http.log"
            log.write_text("\n".join(lines) + "\n", encoding="utf-8")
            picked = mod.load_http_log(log)
            self.assertEqual("variant-A", picked["haiku"]["anthropic_beta"])
            self.assertEqual("sonnet-only", picked["sonnet"]["anthropic_beta"])
            self.assertEqual("opus-only", picked["opus"]["anthropic_beta"])

    def test_aggregate_http_records_keeps_full_distribution(self) -> None:
        records = [
            {"model": "claude-haiku-4-5-20251001", "anthropic_beta": "A"},
            {"model": "claude-haiku-4-5-20251001", "anthropic_beta": "B"},
            {"model": "claude-haiku-4-5-20251001", "anthropic_beta": "A"},
            {"model": "claude-haiku-4-5-20251001", "anthropic_beta": "A"},
            {"model": "claude-haiku-4-5-20251001", "anthropic_beta": "B"},
        ]
        dist = mod.aggregate_http_records(records)
        haiku = dist["haiku"]
        self.assertEqual(5, haiku["total_requests"])
        self.assertEqual(2, len(haiku["unique"]))
        # Ordered by descending count; A (3) before B (2).
        self.assertEqual("A", haiku["unique"][0]["anthropic_beta"])
        self.assertEqual(3, haiku["unique"][0]["count"])
        self.assertEqual("B", haiku["unique"][1]["anthropic_beta"])
        self.assertEqual(2, haiku["unique"][1]["count"])
        # serialize_http_variants drops the raw record but keeps header + count.
        ser = mod.serialize_http_variants(dist)
        self.assertNotIn("record", ser["haiku"]["unique"][0])
        self.assertEqual(3, ser["haiku"]["unique"][0]["count"])

    def test_diff_bimodal_baseline_matches_one_is_needs_investigation(self) -> None:
        baseline = mod.load_tokenkey_baseline(mod.REPO_ROOT)
        haiku_betas = ",".join(baseline["betas"]["haiku_mimicry"])
        other = "oauth-2025-04-20,structured-outputs-2025-12-15"
        capture = {
            "schema_version": 1,
            "cc_version": baseline["canonical_http"]["default_version"],
            "tls": {
                "ja3_hash": baseline["tls"]["ja3_hash"],
                "ja3_raw": baseline["tls"]["ja3_raw"],
                "stainless_package_version": baseline["canonical_http"][
                    "stainless_package_version"
                ],
            },
            "http": {"haiku": {"anthropic_beta": haiku_betas}},
            "http_variants": {
                "haiku": {
                    "total_requests": 11,
                    "unique": [
                        {"anthropic_beta": haiku_betas, "count": 7},
                        {"anthropic_beta": other, "count": 4},
                    ],
                }
            },
        }
        rows = mod.diff_baseline_vs_capture(baseline, capture)
        haiku_row = next(r for r in rows if r.field == "betas.haiku_mimicry")
        self.assertEqual("needs_investigation", haiku_row.status)
        # Bimodal field must NOT fail check/diff against one arbitrary sample.
        self.assertFalse(mod.has_actionable_mismatch(rows))
        self.assertTrue(mod.has_needs_investigation(rows))
        report = mod.format_diff_report(rows)
        self.assertIn("INVESTIGATE", report)
        self.assertIn("needs_investigation=1", report)

    def test_diff_bimodal_baseline_matches_none_is_mismatch(self) -> None:
        baseline = mod.load_tokenkey_baseline(mod.REPO_ROOT)
        capture = {
            "schema_version": 1,
            "cc_version": baseline["canonical_http"]["default_version"],
            "tls": {
                "ja3_hash": baseline["tls"]["ja3_hash"],
                "ja3_raw": baseline["tls"]["ja3_raw"],
                "stainless_package_version": baseline["canonical_http"][
                    "stainless_package_version"
                ],
            },
            "http_variants": {
                "haiku": {
                    "total_requests": 4,
                    "unique": [
                        {"anthropic_beta": "drift-x", "count": 3},
                        {"anthropic_beta": "drift-y", "count": 1},
                    ],
                }
            },
        }
        rows = mod.diff_baseline_vs_capture(baseline, capture)
        haiku_row = next(r for r in rows if r.field == "betas.haiku_mimicry")
        self.assertEqual("mismatch", haiku_row.status)
        # Baseline matches no observed variant -> genuine, actionable drift.
        self.assertTrue(mod.has_actionable_mismatch(rows))

    def test_system_baseline_loaded_from_registry(self) -> None:
        baseline = mod.load_tokenkey_baseline(mod.REPO_ROOT)
        self.assertIn("system", baseline)
        self.assertTrue(baseline["system"]["identity_prefixes"])
        self.assertEqual(
            "x-anthropic-billing-header", baseline["system"]["billing_prefix"]
        )

    def test_system_identity_anchor_match_and_billing_present(self) -> None:
        baseline = mod.load_tokenkey_baseline(mod.REPO_ROOT)
        bundle = mod.bundle_from_artifacts(
            cc_version=baseline["canonical_http"]["default_version"],
            tls_observed={},
            system_anchors=[
                baseline["system"]["billing_prefix"] + ": cc_entrypoint=cli",
                baseline["system"]["identity_prefixes"][0] + ".\n\ndynamic tail",
            ],
        )
        rows = mod.diff_baseline_vs_capture(baseline, bundle)
        ident = next(r for r in rows if r.field == "system.identity_anchor")
        billing = next(r for r in rows if r.field == "system.billing_prefix")
        self.assertEqual("match", ident.status)
        self.assertEqual("match", billing.status)

    def test_system_identity_anchor_drift_is_actionable(self) -> None:
        baseline = mod.load_tokenkey_baseline(mod.REPO_ROOT)
        bundle = mod.bundle_from_artifacts(
            cc_version=baseline["canonical_http"]["default_version"],
            tls_observed={},
            system_anchors=["You are SomethingElse, a totally different tool."],
        )
        rows = mod.diff_baseline_vs_capture(baseline, bundle)
        ident = next(r for r in rows if r.field == "system.identity_anchor")
        self.assertEqual("mismatch", ident.status)
        self.assertTrue(ident.critical)
        # Identity drift is a hard, actionable mismatch (upstream 403 risk).
        self.assertTrue(mod.has_actionable_mismatch(rows))
        # Billing missing on a non-billing capture is INVESTIGATE, not actionable.
        billing = next(r for r in rows if r.field == "system.billing_prefix")
        self.assertEqual("needs_investigation", billing.status)
        self.assertFalse(billing.critical)

    def test_system_missing_capture_is_skip_not_actionable(self) -> None:
        baseline = mod.load_tokenkey_baseline(mod.REPO_ROOT)
        bundle = mod.bundle_from_artifacts(
            cc_version=baseline["canonical_http"]["default_version"],
            tls_observed={},
            system_anchors=[],  # TLS-only run: no system blocks recorded
        )
        rows = mod.diff_baseline_vs_capture(baseline, bundle)
        ident = next(r for r in rows if r.field == "system.identity_anchor")
        self.assertEqual("missing_capture", ident.status)
        # A missing system capture must never by itself fail the check.
        self.assertFalse(
            any(
                r.field.startswith("system.") and r.status == "mismatch"
                for r in rows
            )
        )

    def test_aggregate_system_anchors_dedupes_across_records(self) -> None:
        records = [
            {"system_anchors": [{"index": 0, "text_head": "alpha"}, {"index": 1, "text_head": "beta"}]},
            {"system_anchors": [{"index": 0, "text_head": "alpha"}]},
            {"model": "haiku"},  # no system_anchors key
        ]
        self.assertEqual(["alpha", "beta"], mod.aggregate_system_anchors(records))


if __name__ == "__main__":
    unittest.main()
