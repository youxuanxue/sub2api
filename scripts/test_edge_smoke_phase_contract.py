#!/usr/bin/env python3
"""Contract tests for edge_post_deploy_smoke.sh chat-model gating logic.

Mirrors the bash predicate in ops/stage0/edge_post_deploy_smoke.sh — update
both when the rule changes.
"""
from __future__ import annotations

import unittest


def needs_chat_model(phase: str, self_mode: str, main_gateway_key: str) -> bool:
    if phase != "main-via-edge" and self_mode == "api":
        return True
    if phase != "infra" and bool(main_gateway_key):
        return True
    return False


class EdgeSmokePhaseContractTest(unittest.TestCase):
    def test_infra_default_skips_chat_model(self) -> None:
        self.assertFalse(needs_chat_model("infra", "infra", ""))

    def test_infra_api_mode_requires_chat_model(self) -> None:
        self.assertTrue(needs_chat_model("infra", "api", ""))

    def test_main_via_edge_without_key_skips_chat_model(self) -> None:
        self.assertFalse(needs_chat_model("main-via-edge", "infra", ""))

    def test_main_via_edge_with_key_requires_chat_model(self) -> None:
        self.assertTrue(needs_chat_model("main-via-edge", "infra", "sk-test"))

    def test_full_without_key_or_api_skips_chat_model(self) -> None:
        self.assertFalse(needs_chat_model("full", "infra", ""))

    def test_full_with_main_gateway_key_requires_chat_model(self) -> None:
        self.assertTrue(needs_chat_model("full", "infra", "sk-test"))


if __name__ == "__main__":
    unittest.main()
