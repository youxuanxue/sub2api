#!/usr/bin/env python3
"""Unit tests for model_release_watch classification and baseline loading."""
from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import model_release_watch as mrw  # noqa: E402


class ModelReleaseWatchClassifyTest(unittest.TestCase):
    def setUp(self) -> None:
        self.baseline = mrw.TokenKeyBaseline(
            explicit_keys={
                "claude-opus-5-5": [mrw.SupplyHit("platform:kiro", "claude-opus-5-5")],
                "claude-fable-5-1": [
                    mrw.SupplyHit(
                        "override:newapi:14:https://agentn.global.api5.cursor.sh",
                        "claude-fable-5-1",
                    )
                ],
                "claude-sonnet-5-5": [mrw.SupplyHit("platform:bedrock", "x")],
                "claude-haiku-5-5": [mrw.SupplyHit("platform:bedrock", "claude-haiku-5-5")],
                "gpt-6-astra": [mrw.SupplyHit("platform:openai", "gpt-6-astra")],
            },
            priced_owners={
                "claude-opus-5-5",
                "claude-fable-5-1",
                "gpt-6-astra",
                "claude-haiku-5-5",
            },
        )

    def test_multi_channel_served_via_kiro(self) -> None:
        finding = mrw.classify(
            mrw.UpstreamModel("anthropic", "claude-opus-5-5", "https://example"),
            self.baseline,
        )
        self.assertEqual(finding.status, "served")
        self.assertTrue(finding.primary_coverage)

    def test_cursor_override_counts_as_primary(self) -> None:
        finding = mrw.classify(
            mrw.UpstreamModel("anthropic", "claude-fable-5-1", "https://example"),
            self.baseline,
        )
        self.assertEqual(finding.status, "served")

    def test_unpriced_bedrock_mapping(self) -> None:
        finding = mrw.classify(
            mrw.UpstreamModel("anthropic", "claude-sonnet-5-5", "https://example"),
            self.baseline,
        )
        self.assertEqual(finding.status, "unpriced")
        self.assertFalse(finding.priced)

    def test_legacy_generation_is_out_of_scope(self) -> None:
        finding = mrw.classify(
            mrw.UpstreamModel("anthropic", "claude-opus-4-8", "https://example"),
            self.baseline,
        )
        self.assertEqual(finding.status, "out_of_scope")
        self.assertFalse(finding.actionable)

    def test_narrow_priced_non_primary(self) -> None:
        finding = mrw.classify(
            mrw.UpstreamModel("anthropic", "claude-haiku-5-5", "https://example"),
            self.baseline,
        )
        self.assertEqual(finding.status, "narrow")

    def test_denylist_live_models(self) -> None:
        finding = mrw.classify(
            mrw.UpstreamModel("google", "gemini-3.8-live", "https://example"),
            self.baseline,
        )
        self.assertEqual(finding.status, "out_of_scope")

    def test_watch_only_announcement(self) -> None:
        finding = mrw.classify(
            mrw.UpstreamModel("google", "gemini-4-argon", "https://example", watch_only=True),
            self.baseline,
        )
        self.assertEqual(finding.status, "watch_only")
        self.assertFalse(finding.actionable)


class ModelReleaseWatchBaselineTest(unittest.TestCase):
    def test_load_baseline_ignores_cloudwise_wildcards(self) -> None:
        baseline = mrw.load_baseline()
        self.assertNotIn("claude-*", baseline.explicit_keys)
        # Multi-channel Anthropic reality: Opus 5.5 is on kiro, not missing.
        self.assertIn("claude-opus-5-5", baseline.explicit_keys)
        surfaces = {hit.surface for hit in baseline.surfaces_for("claude-opus-5-5")}
        self.assertIn("platform:kiro", surfaces)

    def test_fixture_scan_builds_actionable_report(self) -> None:
        # Synthetic baseline: keep the unpriced/served matrix stable when live
        # SSOT later prices a previously-unpriced fixture id (claude-sonnet-5-5).
        baseline = mrw.TokenKeyBaseline(
            explicit_keys={
                "claude-opus-5-5": [mrw.SupplyHit("platform:kiro", "claude-opus-5-5")],
                "claude-sonnet-5-5": [mrw.SupplyHit("platform:bedrock", "x")],
            },
            priced_owners={"claude-opus-5-5"},
        )
        fixture = {
            "models": [
                {
                    "vendor": "anthropic",
                    "model_id": "claude-sonnet-5-5",
                    "source_url": "https://example/sonnet",
                },
                {
                    "vendor": "anthropic",
                    "model_id": "claude-opus-5-5",
                    "source_url": "https://example/opus",
                },
                {
                    "vendor": "google",
                    "model_id": "gemini-2.5-flash",
                    "source_url": "https://example/legacy",
                },
                {
                    "vendor": "google",
                    "model_id": "gemini-4-argon",
                    "source_url": "https://example/argon",
                    "watch_only": True,
                },
            ]
        }
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "upstream.json"
            path.write_text(json.dumps(fixture), encoding="utf-8")
            upstream, errors = mrw.collect_upstream(fixture=path)
            self.assertEqual(errors, [])
            report, _state = mrw.build_report(
                baseline=baseline,
                upstream=upstream,
                fetch_errors=[],
                state={"version": 1, "seen": {}},
                bootstrap=False,
                alert_all=True,
            )
        statuses = {f["model_id"]: f["status"] for f in report["findings"]}
        self.assertEqual(statuses["claude-opus-5-5"], "served")
        self.assertEqual(statuses["claude-sonnet-5-5"], "unpriced")
        self.assertEqual(statuses["gemini-2.5-flash"], "out_of_scope")
        self.assertEqual(statuses["gemini-4-argon"], "watch_only")
        self.assertTrue(report["summary"]["has_actionable"])
        actionable_ids = {f["model_id"] for f in report["findings"] if f.get("actionable")}
        self.assertEqual(actionable_ids, {"claude-sonnet-5-5"})

    def test_bootstrap_then_delta_only(self) -> None:
        baseline = mrw.load_baseline()
        upstream = [
            mrw.UpstreamModel("anthropic", "claude-sonnet-5-5", "https://example"),
            mrw.UpstreamModel("google", "gemini-3.1-pro-preview", "https://example"),
        ]
        report1, state = mrw.build_report(
            baseline=baseline,
            upstream=upstream,
            fetch_errors=[],
            bootstrap=True,
        )
        self.assertFalse(report1["summary"]["has_actionable"])
        upstream2 = upstream + [
            mrw.UpstreamModel("anthropic", "claude-mythos-5-1", "https://example"),
        ]
        report2, _ = mrw.build_report(
            baseline=baseline,
            upstream=upstream2,
            fetch_errors=[],
            state=state,
            bootstrap=False,
        )
        actionable = [f for f in report2["findings"] if f.get("actionable")]
        self.assertEqual([f["model_id"] for f in actionable], ["claude-mythos-5-1"])


class ModelReleaseWatchPublicCatalogTest(unittest.TestCase):
    def test_public_catalog_includes_anthropic_without_api_keys(self) -> None:
        models = mrw.load_public_catalog()
        ids = {(m.vendor, m.model_id) for m in models}
        self.assertIn(("anthropic", "claude-sonnet-5-5"), ids)
        self.assertIn(("openai", "gpt-6-astra"), ids)
        self.assertIn(("doubao", "doubao-seed-2.1-pro"), ids)

    def test_collect_upstream_merges_catalog_without_live_network(self) -> None:
        # Force live fetchers off by selecting only seeded vendors via fixture-less
        # path that still loads catalog; monkeypatch fetchers to empty.
        original = dict(mrw.FETCHERS)
        try:
            for vendor in list(mrw.FETCHERS):
                mrw.FETCHERS[vendor] = lambda: ([], [])  # type: ignore[misc]
            models, errors = mrw.collect_upstream(vendors=["anthropic", "openai", "doubao"])
        finally:
            mrw.FETCHERS.update(original)
        ids = {m.model_id for m in models}
        self.assertIn("claude-sonnet-5-5", ids)
        self.assertIn("gpt-6.1-sol", ids)
        self.assertIn("doubao-seed-2.1-pro", ids)
        self.assertEqual(errors, [])


class ModelReleaseWatchGoogleScopeTest(unittest.TestCase):
    def test_text_flash_floor_is_3_8(self) -> None:
        self.assertTrue(mrw.in_candidate_scope("google", "gemini-3.8-flash"))
        self.assertTrue(mrw.in_candidate_scope("google", "gemini-3.8-flash-lite"))
        self.assertFalse(mrw.in_candidate_scope("google", "gemini-3.7-flash"))
        self.assertFalse(mrw.in_candidate_scope("google", "gemini-3.5-flash"))
        self.assertFalse(mrw.in_candidate_scope("google", "gemini-3.1-flash-lite"))

    def test_pro_3_1_line_frozen_next_pro_in_scope(self) -> None:
        self.assertFalse(mrw.in_candidate_scope("google", "gemini-3.1-pro"))
        self.assertFalse(mrw.in_candidate_scope("google", "gemini-3.1-pro-preview"))
        self.assertFalse(mrw.in_candidate_scope("google", "gemini-3.1-pro-high"))
        self.assertFalse(mrw.in_candidate_scope("google", "gemini-3-pro-preview"))
        self.assertTrue(mrw.in_candidate_scope("google", "gemini-3.2-pro"))

    def test_image_floor_and_aliases(self) -> None:
        self.assertTrue(mrw.in_candidate_scope("google", "gemini-3.1-flash-image"))
        self.assertFalse(mrw.in_candidate_scope("google", "gemini-3.1-flash-lite-image"))
        self.assertTrue(mrw.in_candidate_scope("google", "gemini-3-pro-image"))
        self.assertTrue(mrw.in_candidate_scope("google", "nano-banana-2"))
        self.assertTrue(mrw.in_candidate_scope("google", "nano-2"))
        self.assertTrue(mrw.in_candidate_scope("google", "nano-banana-pro"))
        self.assertFalse(mrw.in_candidate_scope("google", "gemini-2.5-flash-image"))
        self.assertFalse(mrw.in_candidate_scope("google", "nano-banana"))

    def test_embedding_and_veo_out_of_scope(self) -> None:
        self.assertFalse(mrw.in_candidate_scope("google", "gemini-embedding-2"))
        self.assertFalse(mrw.in_candidate_scope("google", "gemini-embedding-2-preview"))
        self.assertFalse(mrw.in_candidate_scope("google", "veo-3.1-generate-preview"))


class ModelReleaseWatchExtractTest(unittest.TestCase):
    def test_extract_ids_rejects_doc_path_noise(self) -> None:
        html = """
        <a href="/gemini-api/docs/models/gemini-3.8-flash">x</a>
        <a href="/gemini-api/docs/pricing">gemini-api/docs/pricing</a>
        <img src="gemini-social-card.jpeg"/>
        <code>gemini-3.6-flash</code>
        gemini-api-card-overview
        """
        ids = mrw._extract_ids(html, mrw.VENDOR_PREFIXES["google"], vendor="google")
        self.assertIn("gemini-3.8-flash", ids)
        self.assertIn("gemini-3.6-flash", ids)
        self.assertNotIn("gemini-api/docs/pricing", ids)
        self.assertNotIn("gemini-social-card.jpeg", ids)
        self.assertNotIn("gemini-api-card-overview", ids)

    def test_public_catalog_includes_google_scope_seeds(self) -> None:
        models = mrw.load_public_catalog()
        ids = {m.model_id for m in models if m.vendor == "google"}
        self.assertIn("gemini-3.8-flash", ids)
        self.assertIn("gemini-3.1-flash-image", ids)
        self.assertIn("nano-banana-2", ids)
        self.assertNotIn("gemini-3.1-pro-preview", ids)
        self.assertNotIn("gemini-3.1-flash-lite-image", ids)


class ModelReleaseWatchSelftestTest(unittest.TestCase):
    def test_selftest_passes(self) -> None:
        mrw.run_selftest()


if __name__ == "__main__":
    unittest.main()
