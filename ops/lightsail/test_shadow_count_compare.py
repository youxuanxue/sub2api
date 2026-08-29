#!/usr/bin/env python3
"""Behavior tests for shadow restore count compare + billing slack."""

from __future__ import annotations

import pathlib
import sys
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from shadow_count_compare import (
    billing_dedup_slack,
    compare_billing,
    compare_identity,
    main,
    parse_counts,
)


def _blob(**rows: int) -> str:
    return ",".join(f"{key}={value}" for key, value in rows.items())


class BillingDedupSlackTest(unittest.TestCase):
    def test_floor_covers_small_tables(self) -> None:
        self.assertEqual(billing_dedup_slack(0), 20)
        self.assertEqual(billing_dedup_slack(199_999), 20)

    def test_scales_with_us6_sized_ledger(self) -> None:
        # Boundary sample from the live us6 false-negative: 24-row drift on ~751k.
        self.assertEqual(billing_dedup_slack(751_000), 75)


class CompareCountsTest(unittest.TestCase):
    def test_identity_match_without_billing(self) -> None:
        dump = parse_counts(_blob(users=2, accounts=8, api_keys=3, groups=1, settings=40))
        compare_identity(dump, dict(dump))

    def test_identity_mismatch_exits(self) -> None:
        dump = parse_counts(_blob(users=2, accounts=8, api_keys=3, groups=1, settings=40))
        shadow = dict(dump, accounts=7)
        with self.assertRaises(SystemExit) as raised:
            compare_identity(dump, shadow)
        self.assertIn("identity table accounts", str(raised.exception))

    def test_billing_within_scaled_slack_passes(self) -> None:
        dump = {"usage_billing_dedup": 751_000}
        shadow = {"usage_billing_dedup": 751_024}
        self.assertEqual(compare_billing(dump, shadow), (24, 75))

    def test_billing_over_floor_slack_exits(self) -> None:
        with self.assertRaises(SystemExit) as raised:
            compare_billing({"usage_billing_dedup": 1000}, {"usage_billing_dedup": 1021})
        self.assertIn("usage_billing_dedup drift 21 exceeds slack 20", str(raised.exception))

    def test_cli_billing_ok(self) -> None:
        identity = _blob(users=1, accounts=1, api_keys=1, groups=1, settings=1)
        dump = f"{identity},usage_billing_dedup=751000"
        shadow = f"{identity},usage_billing_dedup=751024"
        self.assertEqual(main([dump, shadow, "--billing"]), 0)

    def test_cli_rejects_bad_blob(self) -> None:
        with self.assertRaises(SystemExit):
            main(["not-a-blob", "users=1"])


if __name__ == "__main__":
    unittest.main()
