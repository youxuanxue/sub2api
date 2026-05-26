#!/usr/bin/env python3
"""Tests for gateway smoke suite gating and model pick logic."""
from __future__ import annotations

import unittest

from scripts.stage0.smoke_suite import (
    edge_phase_gateway_suite,
    needs_chat_model,
    normalize_suite,
    pick_model,
    suite_runs,
)


class SmokeSuiteTest(unittest.TestCase):
    def test_normalize_aliases(self) -> None:
        self.assertEqual(normalize_suite("prod"), "full")
        self.assertEqual(normalize_suite("edge-via-prod"), "main-via-edge")
        self.assertEqual(normalize_suite("minimal"), "quick")

    def test_main_via_edge_skips_chat(self) -> None:
        self.assertTrue(suite_runs("messages", "main-via-edge"))
        self.assertFalse(suite_runs("chat", "main-via-edge"))
        self.assertFalse(suite_runs("gemini", "main-via-edge"))

    def test_full_runs_all_sections(self) -> None:
        for section in (
            "public",
            "frontend",
            "models",
            "chat",
            "messages",
            "gemini",
            "openai_oauth",
        ):
            self.assertTrue(suite_runs(section, "full"))

    def test_pick_model_fallback_when_override_missing(self) -> None:
        models = [{"id": "gpt-4o"}, {"id": "claude-sonnet-4-6"}]
        model, warn = pick_model(models, "claude-sonnet-4-6")
        self.assertEqual(model, "claude-sonnet-4-6")
        self.assertIsNone(warn)

        model, warn = pick_model(models, "claude-opus-4")
        self.assertEqual(model, "claude-sonnet-4-6")
        self.assertIn("not listed", warn or "")

    def test_edge_chat_model_gate(self) -> None:
        self.assertFalse(needs_chat_model("main-via-edge", "infra"))
        self.assertFalse(needs_chat_model("main-via-edge", "api"))
        self.assertTrue(needs_chat_model("infra", "api"))
        self.assertFalse(needs_chat_model("full", "infra"))

    def test_edge_phase_gateway_suite(self) -> None:
        self.assertEqual(edge_phase_gateway_suite("main-via-edge"), "main-via-edge")
        self.assertEqual(edge_phase_gateway_suite("full"), "main-via-edge")
        self.assertIsNone(edge_phase_gateway_suite("infra"))


if __name__ == "__main__":
    unittest.main()
