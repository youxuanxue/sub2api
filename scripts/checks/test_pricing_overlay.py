#!/usr/bin/env python3
"""Focused regression tests for the pricing registry gate."""

from __future__ import annotations

import importlib.util
import pathlib
import unittest


MODULE_PATH = pathlib.Path(__file__).with_name("pricing-overlay.py")
SPEC = importlib.util.spec_from_file_location("pricing_overlay_check", MODULE_PATH)
assert SPEC and SPEC.loader
CHECK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECK)


class PricingOverlayShapeTest(unittest.TestCase):
    def test_accepts_runtime_owner_shape(self) -> None:
        errors = CHECK.validate_runtime_owner_shape(
            "model",
            {
                "mode": "chat",
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
                "max_input_tokens": 128000,
                "supports_reasoning": True,
            },
        )
        self.assertEqual([], errors)

    def test_rejects_owner_shape_that_go_decoder_rejects(self) -> None:
        cases = {
            "typed metadata": (
                "model",
                {"max_input_tokens": "not-an-int"},
            ),
            "provider-prefixed owner": (
                "provider/model",
                {"max_input_tokens": 128000},
            ),
            "malformed intervals": (
                "model",
                {"intervals": "not-an-array"},
            ),
            "negative interval price": (
                "model",
                {"intervals": [{"min_tokens": 0, "max_tokens": 10, "input_cost_per_token": -1e-6}]},
            ),
            "overlapping intervals": (
                "model",
                {"intervals": [
                    {"min_tokens": 0, "max_tokens": 100, "input_cost_per_token": 1e-6},
                    {"min_tokens": 50, "max_tokens": 200, "input_cost_per_token": 1e-6},
                ]},
            ),
        }
        for name, (model, row) in cases.items():
            with self.subTest(name=name):
                self.assertTrue(CHECK.validate_runtime_owner_shape(model, row))


class PricedDimensionCompletenessTest(unittest.TestCase):
    """Pins the gate that replaced BillingService.applyModelSpecificPricingPolicy.

    That Go function used to complete missing price dimensions at runtime
    (deriving gpt-5.6 cache-write as input x 1.25, back-filling the 272K
    long-context triple). Pricing policy is now data-owned and the numeric
    completion is gone, so an incomplete owner row bills $0 / base-rate instead
    of getting a silent Go rescue. These cases keep that failure mechanical.
    """

    def test_accepts_complete_rows(self) -> None:
        rows = {
            "full priority tier": {
                "mode": "chat",
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
                "input_cost_per_token_priority": 2e-6,
                "output_cost_per_token_priority": 4e-6,
                "cache_creation_input_token_cost_priority": 2.5e-6,
                "cache_read_input_token_cost_priority": 2e-7,
            },
            "no priority tier at all": {
                "mode": "chat",
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
            },
            "long-context derivable from above_272k rate": {
                "mode": "chat",
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
                "cache_read_input_token_cost": 1e-7,
                "input_cost_per_token_above_272k_tokens": 2e-6,
                "output_cost_per_token_above_272k_tokens": 3e-6,
                "cache_read_input_token_cost_above_272k_tokens": 2e-7,
            },
            "explicit long-context triple": {
                "mode": "chat",
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
                "long_context_input_token_threshold": 272000,
                "long_context_input_cost_multiplier": 2,
                "long_context_output_cost_multiplier": 1.5,
            },
            "explicit_free row": {
                "mode": "chat",
                "explicit_free": True,
                "input_cost_per_token": 0,
                "output_cost_per_token": 0,
            },
        }
        for name, row in rows.items():
            with self.subTest(name=name):
                self.assertEqual([], CHECK.validate_priced_dimension_completeness("m", row))

    def test_rejects_incomplete_rows(self) -> None:
        rows = {
            "priority input with $0 priority cache-write": {
                "mode": "chat",
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
                "cache_creation_input_token_cost": 1.25e-6,
                "input_cost_per_token_priority": 2e-6,
                "output_cost_per_token_priority": 4e-6,
                "cache_creation_input_token_cost_priority": 0,
            },
            "priority cache-write omitted": {
                "mode": "chat",
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
                "cache_creation_input_token_cost": 1.25e-6,
                "input_cost_per_token_priority": 2e-6,
                "output_cost_per_token_priority": 4e-6,
            },
            "priority output omitted": {
                "mode": "chat",
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
                "input_cost_per_token_priority": 2e-6,
            },
            "long-context threshold with no multipliers": {
                "mode": "chat",
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
                "long_context_input_token_threshold": 272000,
            },
            "long-context missing output multiplier": {
                "mode": "chat",
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
                "long_context_input_token_threshold": 272000,
                "long_context_input_cost_multiplier": 2,
            },
            "partial above-272K rates": {
                "mode": "chat",
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
                "input_cost_per_token_above_272k_tokens": 2e-6,
            },
            "above-272K cache rate omitted": {
                "mode": "chat",
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
                "cache_read_input_token_cost": 1e-7,
                "input_cost_per_token_above_272k_tokens": 2e-6,
                "output_cost_per_token_above_272k_tokens": 3e-6,
            },
        }
        for name, row in rows.items():
            with self.subTest(name=name):
                self.assertTrue(CHECK.validate_priced_dimension_completeness("m", row))

    def test_shipped_registry_is_complete(self) -> None:
        """The registry we ship must already satisfy the gate."""
        import json
        data = json.loads(CHECK.OVERLAY.read_text(encoding="utf-8"))
        failures = []
        for model, row in data.items():
            if model.startswith("_") or not isinstance(row, dict):
                continue
            failures.extend(CHECK.validate_priced_dimension_completeness(model, row))
        self.assertEqual([], failures)


class WebSearchPriceOwnerTest(unittest.TestCase):
    """web_search_price_per_call replaced a hardcoded Go default, so an absent or
    zero key must fail loudly instead of silently billing searches at $0."""

    BASE_TAX = {
        "multiplier": 1.06,
        "rules": [{"provider": "dashscope", "model_prefixes": ["qwen"]}],
    }

    def _errors(self, config: dict) -> list[str]:
        return CHECK.validate_official_list_base_tax({"_config": config})

    def test_requires_web_search_price(self) -> None:
        errors = self._errors({"official_list_base_tax": self.BASE_TAX})
        self.assertTrue(any("web_search_price_per_call" in e for e in errors))

    def test_rejects_zero_and_negative_web_search_price(self) -> None:
        for value in (0, -0.01):
            with self.subTest(value=value):
                errors = self._errors({
                    "official_list_base_tax": self.BASE_TAX,
                    "web_search_price_per_call": value,
                })
                self.assertTrue(any("web_search_price_per_call" in e for e in errors))

    def test_accepts_positive_web_search_price(self) -> None:
        errors = self._errors({
            "official_list_base_tax": self.BASE_TAX,
            "web_search_price_per_call": 0.01,
        })
        self.assertEqual([], errors)

    def test_shipped_registry_declares_web_search_price(self) -> None:
        import json
        data = json.loads(CHECK.OVERLAY.read_text(encoding="utf-8"))
        self.assertEqual([], CHECK.validate_official_list_base_tax(data))


if __name__ == "__main__":
    unittest.main()
