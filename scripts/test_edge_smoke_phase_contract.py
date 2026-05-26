#!/usr/bin/env python3
"""Contract tests for edge_post_deploy_smoke.sh chat-model gating logic.

Mirrors the bash predicate in ops/stage0/edge_post_deploy_smoke.sh — update
both when the rule changes.
"""
from __future__ import annotations

import unittest

from scripts.stage0.smoke_suite import edge_phase_gateway_suite, needs_chat_model


class EdgeSmokePhaseContractTest(unittest.TestCase):
    def test_infra_default_skips_chat_model(self) -> None:
        self.assertFalse(needs_chat_model("infra", "infra"))

    def test_infra_api_mode_requires_chat_model(self) -> None:
        self.assertTrue(needs_chat_model("infra", "api"))

    def test_main_via_edge_skips_chat_model(self) -> None:
        self.assertFalse(needs_chat_model("main-via-edge", "infra"))
        self.assertFalse(needs_chat_model("main-via-edge", "api"))

    def test_full_without_api_skips_chat_model(self) -> None:
        self.assertFalse(needs_chat_model("full", "infra"))

    def test_main_via_edge_uses_messages_suite(self) -> None:
        self.assertEqual(edge_phase_gateway_suite("main-via-edge"), "main-via-edge")


if __name__ == "__main__":
    unittest.main()
