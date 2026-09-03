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
    def test_aliases_require_one_canonical_owner(self) -> None:
        owners = {
            "owner": {"mode": "chat", "input_cost_per_token": 1e-6,
                      "output_cost_per_token": 2e-6},
        }
        self.assertFalse(CHECK.validate_aliases(
            {"alias": "owner"}, owners,
        ))
        for aliases in (
            {"owner": "owner"},
            {"missing-alias": "missing-owner"},
            {"alias-a": "alias-b", "alias-b": "owner"},
            {" Provider/Alias ": "owner"},
        ):
            with self.subTest(aliases=aliases):
                self.assertTrue(CHECK.validate_aliases(aliases, owners))

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


class LongContextThresholdInclusiveBoolShapeTest(unittest.TestCase):
    """Validate that long_context_threshold_inclusive is treated as a boolean."""

    def test_absent_field_passes(self) -> None:
        row = {"mode": "chat", "input_cost_per_token": 1e-6,
               "output_cost_per_token": 2e-6}
        errors = CHECK.validate_runtime_owner_shape("model-no-inclusive", row)
        self.assertFalse(errors)

    def test_explicit_false_passes_with_complete_policy(self) -> None:
        row = {"mode": "chat", "input_cost_per_token": 1e-6,
               "output_cost_per_token": 2e-6,
               "long_context_input_token_threshold": 200000,
               "long_context_input_cost_multiplier": 2.0,
               "long_context_output_cost_multiplier": 2.0,
               "long_context_threshold_inclusive": False}
        errors = CHECK.validate_runtime_owner_shape("model-false", row)
        self.assertFalse(errors)

    def test_bool_without_policy_is_rejected(self) -> None:
        for value in (False, True):
            with self.subTest(value=value):
                row = {"mode": "chat", "input_cost_per_token": 1e-6,
                       "output_cost_per_token": 2e-6,
                       "long_context_threshold_inclusive": value}
                shape_errors = CHECK.validate_runtime_owner_shape("model-partial", row)
                policy_errors = CHECK.validate_priced_dimension_completeness("model-partial", row)
                self.assertFalse(shape_errors)
                self.assertTrue(policy_errors)

    def test_explicit_true_passes(self) -> None:
        row = {"mode": "chat", "input_cost_per_token": 1e-6,
               "output_cost_per_token": 2e-6,
               "long_context_threshold_inclusive": True,
               "long_context_input_token_threshold": 200000,
               "long_context_input_cost_multiplier": 2.0,
               "long_context_output_cost_multiplier": 2.0}
        errors = CHECK.validate_runtime_owner_shape("model-true", row)
        self.assertFalse(errors)

    def test_non_bool_value_rejected(self) -> None:
        for bad in (1, 0, "true", "false", None):
            with self.subTest(value=bad):
                row = {"mode": "chat", "input_cost_per_token": 1e-6,
                       "output_cost_per_token": 2e-6,
                       "long_context_threshold_inclusive": bad}
                errors = CHECK.validate_runtime_owner_shape("model-bad", row)
                # None is treated as absent by the validator (field present but None skipped)
                if bad is None:
                    self.assertFalse(errors)
                else:
                    self.assertTrue(errors, f"Expected error for value {bad!r}")


class XAILongContextEvidenceContractTest(unittest.TestCase):
    def base_row(self) -> dict:
        return {
            "mode": "chat",
            "input_cost_per_token": 1e-6,
            "output_cost_per_token": 2e-6,
            "source": "Dated evidence; xAI 200k long-context billing SSOT.",
        }

    def errors(self, row: dict) -> list[str]:
        return CHECK.validate_priced_dimension_completeness("grok-test", row)

    def test_rejects_tagged_row_missing_inclusive_bool(self) -> None:
        row = self.base_row()
        row.update({
            "long_context_input_token_threshold": 200000,
            "long_context_input_cost_multiplier": 2,
            "long_context_output_cost_multiplier": 2,
        })
        self.assertTrue(self.errors(row))

    def test_rejects_tagged_row_with_explicit_false(self) -> None:
        row = self.base_row()
        row.update({
            "long_context_input_token_threshold": 200000,
            "long_context_input_cost_multiplier": 2,
            "long_context_output_cost_multiplier": 2,
            "long_context_threshold_inclusive": False,
        })
        self.assertTrue(self.errors(row))

    def test_rejects_tagged_row_with_partial_policy(self) -> None:
        row = self.base_row()
        row.update({
            "long_context_input_token_threshold": 200000,
            "long_context_input_cost_multiplier": 2,
            "long_context_threshold_inclusive": True,
        })
        self.assertTrue(self.errors(row))

    def test_accepts_tagged_row_with_complete_inclusive_policy(self) -> None:
        row = self.base_row()
        row.update({
            "long_context_input_token_threshold": 200000,
            "long_context_input_cost_multiplier": 2,
            "long_context_output_cost_multiplier": 2,
            "long_context_threshold_inclusive": True,
        })
        self.assertFalse(self.errors(row))

    def test_does_not_apply_contract_to_untagged_xai_row(self) -> None:
        row = {
            "mode": "chat",
            "input_cost_per_token": 1e-6,
            "output_cost_per_token": 2e-6,
            "litellm_provider": "xai",
            "source": "Official flat xAI pricing without a tier contract.",
        }
        self.assertFalse(self.errors(row))


if __name__ == "__main__":
    unittest.main()
