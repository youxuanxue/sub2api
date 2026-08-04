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


if __name__ == "__main__":
    unittest.main()
