#!/usr/bin/env python3
from __future__ import annotations

import copy
import datetime as dt
import unittest
from pathlib import Path

from ops.pricing import servable_reprobe_ledger as ledger


REAL_LEDGER = Path(__file__).with_name("servable-reprobe-ledger.json")


def probe_family_for(platform: str, _model: str, override: str | None) -> str:
    return override or f"{platform}_chat"


class ServableReprobeLedgerTest(unittest.TestCase):
    def test_repository_durable_policy_has_no_point_in_time_reason(self) -> None:
        ledger.validate(ledger.load(REAL_LEDGER), probe_family_for=probe_family_for)

    def test_reason_rewording_cannot_change_candidate_projection(self) -> None:
        data = {
            "probe_candidates": [
                {
                    "platform": "openai",
                    "model": "candidate",
                    "reason": "Backend support can change; retain for reprobe.",
                    "probe_family": "openai_responses",
                }
            ],
            "watchlist": [],
            "skiplist": [
                {"platform": "openai", "model": "blocked", "reason": "Compatibility alias."}
            ],
            "structurally_gone": [],
        }
        rewritten = copy.deepcopy(data)
        rewritten["probe_candidates"][0]["reason"] = "Retain as a durable candidate."
        rewritten["skiplist"][0]["reason"] = "Non-display alias."
        kwargs = {
            "probe_families_by_platform": {"openai": ("openai_responses",)},
            "family_platform": {"openai_responses": "openai"},
            "probe_family_for": probe_family_for,
        }
        self.assertEqual(
            ledger.augment_candidates({"openai_responses": ["blocked"]}, data, **kwargs),
            ledger.augment_candidates({"openai_responses": ["blocked"]}, rewritten, **kwargs),
        )

    def test_durable_reason_rejects_time_bound_state(self) -> None:
        samples = (
            "2026-09-03 probe failed",
            "live mapping excludes the model",
            "group_id=16 rejected the request",
        )
        for reason in samples:
            with self.subTest(reason=reason):
                data = {
                    "probe_candidates": [
                        {"platform": "openai", "model": "candidate", "reason": reason}
                    ],
                    "watchlist": [],
                    "skiplist": [],
                    "structurally_gone": [],
                }
                with self.assertRaisesRegex(ValueError, "durable reason contains"):
                    ledger.validate(data, probe_family_for=probe_family_for)

    def test_watchlist_owns_time_bounded_evidence(self) -> None:
        data = {
            "probe_candidates": [],
            "watchlist": [
                {
                    "platform": "openai",
                    "model": "follow-up",
                    "reason": "live check needs follow-up",
                    "last_probe": "2026-09-01",
                    "freshness_days": 7,
                }
            ],
            "skiplist": [],
            "structurally_gone": [],
        }
        ledger.validate(
            data,
            probe_family_for=probe_family_for,
            today=dt.date(2026, 9, 3),
        )


if __name__ == "__main__":
    unittest.main()
