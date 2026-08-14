#!/usr/bin/env python3
"""Behavior tests for the provider pricing evidence sensor."""
from __future__ import annotations

import importlib.util
import pathlib
import sys
import unittest

_MODULE_PATH = pathlib.Path(__file__).with_name("pricing-registry-sensor.py")
_SPEC = importlib.util.spec_from_file_location("pricing_registry_sensor", _MODULE_PATH)
sensor = importlib.util.module_from_spec(_SPEC)
assert _SPEC and _SPEC.loader
sys.modules[_SPEC.name] = sensor
_SPEC.loader.exec_module(sensor)


class PricingRegistrySensorTests(unittest.TestCase):
    def setUp(self) -> None:
        self.registry = {
            "_meta": {"note": "owner"},
            "_config": {"web_search_price_per_call": 0.01},
            "model-a": {
                "mode": "chat",
                "litellm_provider": "provider-a",
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 2e-6,
                "supports_vision": False,
                "source": "human-approved",
            },
        }

    def test_full_dimension_drift_is_deterministic(self) -> None:
        source = {
            "provider-a/model-a": {
                "litellm_provider": "provider-a",
                "mode": "chat",
                "output_cost_per_token": 3e-6,
                "input_cost_per_token": 1e-6,
                "supports_vision": True,
            }
        }
        first = sensor.build_report(self.registry, source, source_label="fixture")
        second = sensor.build_report(self.registry, source, source_label="fixture")
        self.assertEqual(first, second)
        fields = first["owner_drifts"][0]["fields"]
        self.assertEqual([item["field"] for item in fields], [
            "output_cost_per_token", "supports_vision"
        ])
        self.assertTrue(fields[0]["actionable"])
        self.assertFalse(fields[1]["actionable"])

    def test_candidate_updates_only_existing_owner_billable_fields(self) -> None:
        source = {
            "model-a": {
                "litellm_provider": "provider-a",
                "input_cost_per_token": 4e-6,
                "long_context_threshold_inclusive": True,
                "supports_vision": True,
                "provider_private_metadata": "ignore",
            },
            "new-model": {
                "mode": "chat",
                "input_cost_per_token": 9e-6,
                "output_cost_per_token": 9e-6,
            },
        }
        report = sensor.build_report(self.registry, source, source_label="fixture")
        candidate, owners = sensor.build_candidate_registry(self.registry, report)
        self.assertEqual(owners, ["model-a"])
        self.assertEqual(candidate["model-a"]["input_cost_per_token"], 4e-6)
        self.assertTrue(candidate["model-a"]["long_context_threshold_inclusive"])
        self.assertFalse(candidate["model-a"]["supports_vision"])
        self.assertEqual(candidate["model-a"]["source"], "human-approved")
        self.assertNotIn("new-model", candidate)
        self.assertEqual(report["report_only_evidence"][0]["normalized_model"], "new-model")

    def test_gpt55_pro_is_always_report_only_without_an_owner(self) -> None:
        source = {
            "openai/gpt-5.5-pro": {
                "input_cost_per_token": 30e-6,
                "output_cost_per_token": 180e-6,
            }
        }
        report = sensor.build_report(self.registry, source, source_label="fixture")
        self.assertEqual(report["summary"]["actionable_owner_count"], 0)
        item = report["report_only_evidence"][0]
        self.assertEqual(item["normalized_model"], "gpt-5.5-pro")
        self.assertIn("gpt-5.5", item["reason"])

    def test_direct_bare_evidence_wins_between_matching_provider_rows(self) -> None:
        source = {
            "provider-a/model-a": {
                "litellm_provider": "provider-a",
                "output_cost_per_token": 7e-6,
            },
            "model-a": {
                "litellm_provider": "provider-a",
                "output_cost_per_token": 6e-6,
            },
        }
        report = sensor.build_report(self.registry, source, source_label="fixture")
        drift = report["owner_drifts"][0]
        self.assertEqual(drift["source_key"], "model-a")
        self.assertEqual(drift["fields"][0]["evidence"], 6e-6)
        self.assertIn("alternate evidence", report["report_only_evidence"][0]["reason"])

    def test_matching_provider_beats_mismatched_bare_row(self) -> None:
        source = {
            "model-a": {
                "litellm_provider": "provider-b",
                "output_cost_per_token": 99e-6,
            },
            "provider-a/model-a": {
                "litellm_provider": "provider-a",
                "output_cost_per_token": 7e-6,
            },
        }
        report = sensor.build_report(self.registry, source, source_label="fixture")
        drift = report["owner_drifts"][0]
        self.assertEqual(drift["source_key"], "provider-a/model-a")
        self.assertEqual(drift["fields"][0]["evidence"], 7e-6)
        self.assertIn("provider mismatch", report["report_only_evidence"][0]["reason"])

    def test_no_provider_match_is_report_only(self) -> None:
        source = {
            "model-a": {
                "litellm_provider": "provider-b",
                "output_cost_per_token": 99e-6,
            },
        }
        report = sensor.build_report(self.registry, source, source_label="fixture")
        candidate, owners = sensor.build_candidate_registry(self.registry, report)
        self.assertEqual(report["owner_drifts"], [])
        self.assertEqual(owners, [])
        self.assertEqual(candidate, self.registry)
        self.assertIn("provider mismatch", report["report_only_evidence"][0]["reason"])

    def test_missing_source_fields_never_delete_registry_dimensions(self) -> None:
        source = {"model-a": {"input_cost_per_token": 1e-6}}
        report = sensor.build_report(self.registry, source, source_label="fixture")
        candidate, owners = sensor.build_candidate_registry(self.registry, report)
        self.assertEqual(owners, [])
        self.assertEqual(candidate, self.registry)

    def test_report_markdown_states_non_publication_boundary(self) -> None:
        report = sensor.build_report(self.registry, {}, source_label="fixture")
        markdown = sensor.render_markdown(report)
        self.assertIn("never publishes runtime pricing", markdown)


if __name__ == "__main__":
    unittest.main()
