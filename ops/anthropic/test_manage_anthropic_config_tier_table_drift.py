#!/usr/bin/env python3
"""Unit tests for the tier_table_drift surface of manage-anthropic-config.py.

Post-PR #472 the per-tier strategy values live in the `tiers` reference table
(seeded from git embed baseline, admin-editable, reverted on restart). The check
reveals & warns when a live tiers row diverges from the git baseline — it must
NOT diff the per-account `extra` (that is overlay-only since #472).
"""
from __future__ import annotations

import importlib.util
import pathlib
import unittest

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "manage-anthropic-config.py"
_spec = importlib.util.spec_from_file_location("manage_anthropic_config_tier_drift", _MOD_PATH)
mgr = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(mgr)


def _row_for(expected_tier: dict, **overrides) -> dict:
    """Build a live tiers-table row matching the git-effective tier, with overrides."""
    row = {"name": expected_tier["name"]}
    for k in mgr.TIER_TABLE_COMPARE_KEYS:
        row[k] = expected_tier[k]
    row.update(overrides)
    return row


class TierTableDriftTest(unittest.TestCase):
    def setUp(self) -> None:
        # Real git baseline → effective per-tier rows (single source, anchored to
        # Go embed + tk_012 by check-tier-baseline-embed.py).
        self.expected = mgr._load_expected_tiers()
        self.assertIn("l4", self.expected)

    def test_clean_node_no_drift(self) -> None:
        live = [_row_for(self.expected[t]) for t in self.expected]
        items = mgr._tier_table_drift_items("edge:uk1", live, self.expected)
        self.assertEqual(items, [])

    def test_backend_edit_is_revealed_with_warning(self) -> None:
        live = [_row_for(self.expected[t]) for t in self.expected]
        # Simulate an admin backend edit to l4.base_rpm.
        for r in live:
            if r["name"] == "l4":
                r["base_rpm"] = 999
        items = mgr._tier_table_drift_items("edge:uk1", live, self.expected)
        self.assertEqual(len(items), 1)
        it = items[0]
        self.assertEqual(it["tier"], "l4")
        self.assertEqual(it["status"], "drift")
        self.assertEqual(it["diffs"], [{"path": "/base_rpm",
                                        "expected": self.expected["l4"]["base_rpm"],
                                        "actual": 999}])
        self.assertIn("modified via backend admin", it["warning"])
        self.assertIn("will be reverted", it["warning"])

    def test_missing_tier_row(self) -> None:
        live = [_row_for(self.expected[t]) for t in self.expected if t != "l2"]
        items = mgr._tier_table_drift_items("prod", live, self.expected)
        self.assertEqual([i["tier"] for i in items], ["l2"])
        self.assertEqual(items[0]["status"], "missing")

    def test_extra_tier_row(self) -> None:
        live = [_row_for(self.expected[t]) for t in self.expected]
        live.append({"name": "l9", **{k: 1 for k in mgr.TIER_TABLE_COMPARE_KEYS}})
        items = mgr._tier_table_drift_items("edge:us1", live, self.expected)
        self.assertEqual([i["status"] for i in items], ["extra"])
        self.assertEqual(items[0]["tier"], "l9")

    def test_numeric_equality_tolerates_float(self) -> None:
        live = [_row_for(self.expected[t]) for t in self.expected]
        for r in live:
            if r["name"] == "l4":
                r["base_rpm"] = float(self.expected["l4"]["base_rpm"])  # 56 -> 56.0
        items = mgr._tier_table_drift_items("edge:uk1", live, self.expected)
        self.assertEqual(items, [])

    def test_snapshot_aggregation_skips_planned_and_errored(self) -> None:
        clean = [_row_for(self.expected[t]) for t in self.expected]
        drifted = [_row_for(self.expected[t]) for t in self.expected]
        for r in drifted:
            if r["name"] == "l5":
                r["max_sessions"] = 7
        snap = {
            "edges": {
                "uk1": {"deployable": True, "tiers": clean},
                "us1": {"deployable": True, "tiers": drifted},
                "us9": {"deployable": False, "skipped_reason": "planned"},
                "usX": {"error": "no instance"},
            },
            "prod": {"tiers": clean},
        }
        items = mgr._tier_table_drift_from_snapshot(snap, self.expected)
        nodes = sorted({i["node"] for i in items})
        self.assertEqual(nodes, ["edge:us1"])  # only the drifted deployable node
        self.assertEqual(items[0]["tier"], "l5")


if __name__ == "__main__":
    unittest.main()
