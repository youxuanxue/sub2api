#!/usr/bin/env python3
"""Unit tests for capacity/traffic-aware Edge canary selection."""
from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path

_MODULE_PATH = Path(__file__).resolve().parent / "pick_release_canary_edge.py"
_spec = importlib.util.spec_from_file_location("pick_release_canary_edge", _MODULE_PATH)
assert _spec and _spec.loader
_mod = importlib.util.module_from_spec(_spec)
sys.modules[_spec.name] = _mod
_spec.loader.exec_module(_mod)


def row(*, traffic: int, headroom: int, oauth: int = 0, eligible: bool = True) -> dict:
    required = 335_544_320
    return {
        "mem_available_bytes": required + headroom,
        "active_app_working_set_bytes": 150_000_000,
        "memory_required_bytes": required,
        "memory_headroom_bytes": headroom,
        "disk_available_bytes": 8_000_000_000,
        "completed_requests_30m": traffic,
        "oauth_account_count": oauth,
        "eligible": eligible,
        "rejection_reasons": [] if eligible else ["memory_below_required"],
    }


class PickReleaseCanaryEdgeTest(unittest.TestCase):
    def test_probes_entire_fleet_and_sorts_by_traffic_then_memory(self) -> None:
        facts = {
            "us3": row(traffic=20, headroom=100, oauth=9),
            "us4": row(traffic=5, headroom=10),
            "us5": row(traffic=5, headroom=30),
        }
        called: list[str] = []

        def probe(edge_id: str) -> dict:
            called.append(edge_id)
            return facts[edge_id]

        canary, audit = _mod.pick_release_canary(
            ["us3", "us4", "us5"], probe_facts=probe
        )
        self.assertEqual(canary, "us5")
        self.assertEqual(called, ["us3", "us4", "us5"])
        self.assertEqual(len(audit), 3)

    def test_matrix_order_breaks_exact_tie(self) -> None:
        canary, _ = _mod.pick_release_canary(
            ["us4", "us5"],
            probe_facts=lambda _edge: row(traffic=1, headroom=20),
        )
        self.assertEqual(canary, "us4")

    def test_transport_failure_rejects_only_that_edge(self) -> None:
        facts = {"us3": None, "us4": row(traffic=8, headroom=10)}
        canary, audit = _mod.pick_release_canary(
            ["us3", "us4"],
            probe_facts=lambda edge: facts[edge],
        )
        self.assertEqual(canary, "us4")
        self.assertFalse(audit[0]["eligible"])
        self.assertEqual(audit[0]["rejection_reasons"], ["probe_transport_failed"])

    def test_oauth_count_is_audit_only_including_fleet_wide_zero(self) -> None:
        facts = {
            "us3": row(traffic=9, headroom=100, oauth=12),
            "us4": row(traffic=1, headroom=10, oauth=0),
        }
        canary, _ = _mod.pick_release_canary(
            ["us3", "us4"],
            probe_facts=lambda edge: facts[edge],
        )
        self.assertEqual(canary, "us4")

        zero_canary, _ = _mod.pick_release_canary(
            ["us3", "us4"],
            probe_facts=lambda edge: row(traffic=0 if edge == "us4" else 2, headroom=1),
        )
        self.assertEqual(zero_canary, "us4")

    def test_returns_none_when_no_edge_is_eligible(self) -> None:
        canary, audit = _mod.pick_release_canary(
            ["us3", "us4"],
            probe_facts=lambda _edge: row(traffic=0, headroom=-1, eligible=False),
        )
        self.assertIsNone(canary)
        self.assertEqual(len(audit), 2)

    def test_invalid_eligible_facts_reject_only_that_edge(self) -> None:
        invalid = row(traffic=0, headroom=10)
        invalid["completed_requests_30m"] = None
        facts = {"us3": invalid, "us4": row(traffic=5, headroom=1)}
        canary, audit = _mod.pick_release_canary(
            ["us3", "us4"],
            probe_facts=lambda edge: facts[edge],
        )
        self.assertEqual(canary, "us4")
        self.assertEqual(audit[0]["rejection_reasons"], ["probe_facts_invalid"])

    def test_picker_does_not_implement_capacity_thresholds(self) -> None:
        text = _MODULE_PATH.read_text(encoding="utf-8")
        self.assertNotIn("335544320", text)
        self.assertNotIn("335_544_320", text)
        self.assertNotIn("134217728", text)
        self.assertNotIn("134_217_728", text)
        self.assertNotIn("5368709120", text)
        self.assertNotIn("5_368_709_120", text)
        small_disk = row(traffic=0, headroom=1)
        small_disk["disk_available_bytes"] = 4_000_000_000
        self.assertTrue(_mod.validate_release_facts(small_disk))

    def test_release_fact_schema_rejects_inconsistent_rows(self) -> None:
        cases = []
        missing_sort_fact = row(traffic=0, headroom=1)
        missing_sort_fact["memory_headroom_bytes"] = None
        cases.append(missing_sort_fact)
        malformed_reasons = row(traffic=0, headroom=1, eligible=False)
        malformed_reasons["rejection_reasons"] = [7]
        cases.append(malformed_reasons)
        eligible_with_reason = row(traffic=0, headroom=1)
        eligible_with_reason["rejection_reasons"] = ["unexpected"]
        cases.append(eligible_with_reason)
        ineligible_without_reason = row(traffic=0, headroom=1, eligible=False)
        ineligible_without_reason["rejection_reasons"] = []
        cases.append(ineligible_without_reason)
        missing_memory = row(traffic=0, headroom=1)
        missing_memory["mem_available_bytes"] = None
        cases.append(missing_memory)
        missing_working_set = row(traffic=0, headroom=1)
        missing_working_set["active_app_working_set_bytes"] = None
        cases.append(missing_working_set)
        missing_disk = row(traffic=0, headroom=1)
        missing_disk["disk_available_bytes"] = None
        cases.append(missing_disk)
        inconsistent_headroom = row(traffic=0, headroom=1)
        inconsistent_headroom["memory_headroom_bytes"] = 2
        cases.append(inconsistent_headroom)
        for payload in cases:
            with self.subTest(payload=payload):
                self.assertFalse(_mod.validate_release_facts(payload))


if __name__ == "__main__":
    unittest.main()
