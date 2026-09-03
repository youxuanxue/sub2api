#!/usr/bin/env python3
"""Focused tests for the shared pricing registry policy."""

from __future__ import annotations

import unittest
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from pricing_registry import has_complete_price, resolve_price_owner


class PricingRegistryTest(unittest.TestCase):
    def test_embedding_requires_input_price(self) -> None:
        self.assertTrue(has_complete_price({"mode": "embedding", "input_cost_per_token": 0.0001}))
        self.assertFalse(has_complete_price({"mode": "embedding", "input_cost_per_token": 0}))

    def test_chat_requires_both_dimensions(self) -> None:
        self.assertFalse(has_complete_price({"mode": "chat", "input_cost_per_token": 0.001}))
        self.assertTrue(has_complete_price({
            "mode": "chat",
            "input_cost_per_token": 0.001,
            "output_cost_per_token": 0.002,
        }))

    def test_image_accepts_image_or_token_price(self) -> None:
        self.assertTrue(has_complete_price({"mode": "image_generation", "output_cost_per_image": 0.04}))
        self.assertTrue(has_complete_price({"mode": "image_generation", "output_cost_per_image_token": 0.0001}))

    def test_video_and_unknown_modes(self) -> None:
        self.assertTrue(has_complete_price({"mode": "video_generation", "output_cost_per_second": 0.2}))
        self.assertFalse(has_complete_price({"mode": "unknown", "input_cost_per_token": 1}))

    def test_alias_resolution_is_single_hop(self) -> None:
        overlay = {"_aliases": {"alias": "owner", "chain": "alias"}}
        self.assertEqual(resolve_price_owner("alias", overlay), "owner")
        self.assertEqual(resolve_price_owner("chain", overlay), "alias")
        self.assertEqual(resolve_price_owner("missing", overlay), "missing")


if __name__ == "__main__":
    unittest.main()
