#!/usr/bin/env python3
"""Focused behavior tests for the complete pricing-registry gate."""

from __future__ import annotations

import importlib.util
import pathlib
import unittest


MODULE_PATH = pathlib.Path(__file__).with_name("pricing-overlay.py")
SPEC = importlib.util.spec_from_file_location("pricing_overlay_check", MODULE_PATH)
assert SPEC and SPEC.loader
CHECK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECK)

PARITY_MODULE_PATH = pathlib.Path(__file__).with_name(
    "pricing-registry-migration-parity.py"
)
PARITY_SPEC = importlib.util.spec_from_file_location(
    "pricing_registry_migration_parity", PARITY_MODULE_PATH
)
assert PARITY_SPEC and PARITY_SPEC.loader
PARITY = importlib.util.module_from_spec(PARITY_SPEC)
PARITY_SPEC.loader.exec_module(PARITY)


class PricingRegistryGateTest(unittest.TestCase):
    def test_rejects_malformed_runtime_owner_shapes(self) -> None:
        cases = (
            ("provider/model", {"mode": "chat"}),
            ("model", {"max_input_tokens": "bad"}),
            ("model", {"intervals": "bad"}),
            ("model", {"intervals": [
                {"min_tokens": 0, "max_tokens": 100, "input_cost_per_token": 1e-6},
                {"min_tokens": 50, "max_tokens": 200, "input_cost_per_token": 1e-6},
            ]}),
        )
        for model, row in cases:
            with self.subTest(model=model, row=row):
                self.assertTrue(CHECK.validate_runtime_owner_shape(model, row))

    def test_accepts_distinct_settlement_dimensions(self) -> None:
        rows = {
            "token": {"mode": "chat", "input_cost_per_token": 1e-6,
                      "output_cost_per_token": 2e-6},
            "image token": {"mode": "image_generation",
                            "output_cost_per_image_token": 4e-5},
            "per image": {"mode": "image_generation", "output_cost_per_image": 0.02},
            "video": {"mode": "video_generation", "output_cost_per_second": 0.1},
            "embedding": {"mode": "embedding", "input_cost_per_token": 1e-7},
            "free": {"mode": "chat", "input_cost_per_token": 0,
                     "output_cost_per_token": 0, "explicit_free": True},
        }
        for name, row in rows.items():
            alternatives = CHECK.MODE_FIELDS[row["mode"]]
            valid = row.get("explicit_free") is True or any(
                all(CHECK._finite_number(row.get(field)) and row[field] > 0 for field in fields)
                for fields in alternatives
            )
            with self.subTest(name=name):
                self.assertTrue(valid)

    def test_rejects_unknown_zero_and_partial_long_context(self) -> None:
        zero = {"mode": "chat", "input_cost_per_token": 0, "output_cost_per_token": 0}
        alternatives = CHECK.MODE_FIELDS[zero["mode"]]
        self.assertFalse(any(
            all(CHECK._finite_number(zero.get(field)) and zero[field] > 0 for field in fields)
            for fields in alternatives
        ))
        self.assertTrue(CHECK.validate_priced_dimension_completeness("model", {
            "mode": "chat", "input_cost_per_token": 1e-6,
            "output_cost_per_token": 2e-6,
            "long_context_input_token_threshold": 272000,
        }))


class PricingRegistryMigrationParityTest(unittest.TestCase):
    def test_reconstructs_legacy_fill_only_precedence(self) -> None:
        external = {
            "source-wins": {
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
            },
            "source-zero": {
                "input_cost_per_token": 0,
                "output_cost_per_token": 0,
            },
        }
        overlay = {
            "source-wins": {
                "input_cost_per_token": 9e-6,
                "output_cost_per_token": 9e-6,
            },
            "source-zero": {
                "input_cost_per_token": 3e-6,
                "output_cost_per_token": 4e-6,
            },
            "overlay-only": {
                "output_cost_per_image": 0.02,
            },
        }

        owners = PARITY.build_legacy_direct_owners(external, overlay)

        self.assertEqual(
            owners["source-wins"].dimensions,
            {
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
            },
        )
        self.assertEqual(owners["source-wins"].source, "external")
        self.assertEqual(
            owners["source-zero"].dimensions,
            {
                "input_cost_per_token": 3e-6,
                "output_cost_per_token": 4e-6,
            },
        )
        self.assertEqual(owners["source-zero"].source, "overlay_fill")
        self.assertEqual(
            owners["overlay-only"].dimensions,
            {"output_cost_per_image": 0.02},
        )

    def test_reports_approved_and_unapproved_price_deltas_separately(self) -> None:
        external = {
            "stable": {
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
            },
            "kimi-k2.6": {
                "input_cost_per_token": 0.95e-6,
                "output_cost_per_token": 4e-6,
            },
            "legacy-alias": {
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
            },
        }
        fallback = {
            "fallback-only": PARITY.LegacyOwner(
                source="go_fallback",
                dimensions={
                    "input_cost_per_token": 5e-6,
                    "output_cost_per_token": 6e-6,
                },
            ),
        }
        registry = {
            "stable": {
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 3e-6,
            },
            "kimi-k2.6": {
                "input_cost_per_token": 6.5 / 6.7 / 1_000_000,
                "output_cost_per_token": 4e-6,
            },
            "fallback-only": {
                "input_cost_per_token": 5e-6,
                "output_cost_per_token": 6e-6,
            },
        }

        report = PARITY.compare_migration(
            external=external,
            overlay={},
            legacy_fallbacks=fallback,
            registry=registry,
            approved_deltas={
                "kimi-k2.6": {
                    "input_cost_per_token": "approved exact CNY/USD correction",
                },
            },
            owner_redirects={"legacy-alias": "stable"},
        )

        self.assertEqual(report["summary"]["approved_delta_count"], 1)
        self.assertEqual(report["summary"]["unapproved_delta_count"], 1)
        self.assertEqual(report["summary"]["missing_registry_owner_count"], 0)
        self.assertEqual(report["summary"]["unexpected_registry_owner_count"], 0)
        self.assertEqual(
            report["approved_deltas"][0]["model"],
            "kimi-k2.6",
        )
        self.assertEqual(report["unapproved_deltas"][0]["model"], "stable")
        self.assertEqual(
            report["owner_redirects"],
            [{"legacy_owner": "legacy-alias", "registry_owner": "stable"}],
        )
        self.assertFalse(PARITY.report_passes(report))

    def test_materializes_legacy_openai_policy_before_comparison(self) -> None:
        gpt56 = PARITY.materialize_legacy_runtime_dimensions(
            "gpt-5.6-sol",
            {
                "input_cost_per_token": 5e-6,
                "input_cost_per_token_priority": 10e-6,
                "output_cost_per_token": 30e-6,
            },
        )
        self.assertEqual(gpt56["cache_creation_input_token_cost"], 6.25e-6)
        self.assertEqual(
            gpt56["cache_creation_input_token_cost_priority"], 12.5e-6
        )
        self.assertEqual(gpt56["long_context_input_token_threshold"], 272000)
        self.assertEqual(gpt56["long_context_input_cost_multiplier"], 2.0)
        self.assertEqual(gpt56["long_context_output_cost_multiplier"], 1.5)

        gpt54 = PARITY.materialize_legacy_runtime_dimensions(
            "gpt-5.4",
            {
                "input_cost_per_token": 2.5e-6,
                "output_cost_per_token": 15e-6,
            },
        )
        self.assertNotIn("cache_creation_input_token_cost", gpt54)
        self.assertEqual(gpt54["long_context_input_token_threshold"], 272000)

        gpt54_mini = PARITY.materialize_legacy_runtime_dimensions(
            "gpt-5.4-mini",
            {
                "input_cost_per_token": 0.75e-6,
                "output_cost_per_token": 4.5e-6,
            },
        )
        self.assertNotIn("long_context_input_token_threshold", gpt54_mini)

    def test_extracts_legacy_grok_image_and_web_search_prices(self) -> None:
        source = """
const (
    defaultGrokImagineImagePrice1K = 0.02
    defaultGrokImagineImagePrice2K = 0.03
    defaultGrokImagineImageQualityPrice1K = 0.05
    defaultGrokImagineImageQualityPrice2K = 0.07
    defaultWebSearchPricePerCall = 0.01
)
"""

        supplemental = PARITY.extract_go_supplemental_dimensions(source)
        self.assertEqual(
            supplemental["grok-imagine-image"],
            {"image_price_1k": 0.02, "image_price_2k": 0.03,
             "image_price_4k": 0.03},
        )
        self.assertEqual(
            supplemental["grok-imagine-image-quality"],
            {"image_price_1k": 0.05, "image_price_2k": 0.07,
             "image_price_4k": 0.07},
        )
        self.assertEqual(
            PARITY.build_legacy_global_policy({"_config": {"tax": 1.06}}, source),
            {"tax": 1.06, "web_search_price_per_call": 0.01},
        )


if __name__ == "__main__":
    unittest.main()
