#!/usr/bin/env python3
"""Unit tests for pick_oauth_canary_edge.py selection logic."""
from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path

_MODULE_PATH = Path(__file__).resolve().parent / "pick_oauth_canary_edge.py"
_spec = importlib.util.spec_from_file_location("pick_oauth_canary_edge", _MODULE_PATH)
assert _spec and _spec.loader
_mod = importlib.util.module_from_spec(_spec)
sys.modules[_spec.name] = _mod
_spec.loader.exec_module(_mod)


class PickOAuthCanaryEdgeTest(unittest.TestCase):
    def test_picks_first_edge_with_positive_count(self) -> None:
        counts = {"us3": 0, "us4": 0, "us6": 2, "us5": 5}

        def probe(edge_id: str) -> int | None:
            return counts[edge_id]

        canary, audit = _mod.pick_oauth_canary(
            ["us3", "us4", "us6", "us5"],
            probe_count=probe,
            source_group="default",
        )
        self.assertEqual(canary, "us6")
        self.assertEqual(len(audit), 3)

    def test_skips_probe_failures_and_continues(self) -> None:
        def probe(edge_id: str) -> int | None:
            if edge_id == "us3":
                return None
            if edge_id == "us4":
                return 0
            return 1

        canary, audit = _mod.pick_oauth_canary(
            ["us3", "us4", "us5"],
            probe_count=probe,
            source_group="default",
        )
        self.assertEqual(canary, "us5")
        self.assertEqual(audit[0]["oauth_account_count"], None)

    def test_returns_none_when_no_pool(self) -> None:
        canary, audit = _mod.pick_oauth_canary(
            ["us3", "us4"],
            probe_count=lambda _e: 0,
            source_group="default",
        )
        self.assertIsNone(canary)
        self.assertEqual(len(audit), 2)


if __name__ == "__main__":
    unittest.main()
