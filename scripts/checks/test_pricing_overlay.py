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


if __name__ == "__main__":
    unittest.main()
