#!/usr/bin/env python3
"""Contract tests for edge_post_deploy_smoke.sh chat-model gating logic.

Mirrors the bash predicate in ops/stage0/edge_post_deploy_smoke.sh — update
both when the rule changes.
"""
from __future__ import annotations

import unittest

from scripts.stage0.smoke_suite import edge_phase_gateway_suite, edge_phase_runs_native_oauth, needs_chat_model


class EdgeSmokePhaseContractTest(unittest.TestCase):
    def test_infra_default_skips_chat_model(self) -> None:
        self.assertFalse(needs_chat_model("infra", "infra"))
        self.assertFalse(needs_chat_model("infra", "api"))

    def test_main_via_edge_skips_chat_model(self) -> None:
        self.assertFalse(needs_chat_model("main-via-edge", "infra"))
        self.assertFalse(needs_chat_model("main-via-edge", "api"))

    def test_full_skips_legacy_chat_model_export(self) -> None:
        self.assertFalse(needs_chat_model("full", "api"))

    def test_main_via_edge_uses_messages_suite(self) -> None:
        self.assertEqual(edge_phase_gateway_suite("main-via-edge"), "main-via-edge")

    def test_full_uses_native_oauth_not_prod_gateway_suite(self) -> None:
        self.assertIsNone(edge_phase_gateway_suite("full"))
        self.assertTrue(edge_phase_runs_native_oauth("full"))

    def test_edge_native_oauth_phase(self) -> None:
        self.assertTrue(edge_phase_runs_native_oauth("edge-native-oauth"))
        self.assertIsNone(edge_phase_gateway_suite("edge-native-oauth"))


if __name__ == "__main__":
    unittest.main()
