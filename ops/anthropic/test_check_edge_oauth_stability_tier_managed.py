#!/usr/bin/env python3
"""Unit tests for check-edge-oauth-stability.py tier-managed extra handling.

Post-PR #472 the 8 TierManagedExtraKeys (base_rpm / max_sessions / ...) are
overlaid from the `tiers` table at read time and stripped from the persisted
account.extra on write. The guard must NOT diff them against the per-account
baseline (doing so was a guaranteed false positive). Non-tier-managed extra,
account, credentials and tls_profile diffs must still work; a missing/unknown
stability_tier must still fail.
"""
from __future__ import annotations

import importlib.util
import pathlib
import unittest

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "check-edge-oauth-stability.py"
_spec = importlib.util.spec_from_file_location("check_edge_oauth_stability_guard", _MOD_PATH)
guard = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(guard)


def _baseline(extra: dict, *, extra_absent=None) -> dict:
    return {"baseline": {
        "account": {},
        "credentials": {},
        "extra": extra,
        "extra_absent": extra_absent or [],
        "tls_profile": {},
    }}


def _live(extra: dict) -> dict:
    return {"found": True, "account": {}, "credentials": {}, "extra": extra, "tls_profile": {}}


class TierManagedExtraTest(unittest.TestCase):
    def test_tier_managed_keys_are_not_diffed(self) -> None:
        # baseline says base_rpm=56/max_sessions=120; live extra has them stripped
        # (the correct post-#472 state) AND a stale leftover value.
        baseline = _baseline({"base_rpm": 56, "max_sessions": 120})
        diffs = guard.compare_live_to_baseline(_live({}), baseline)
        self.assertEqual(diffs, [], "tier-managed keys must not produce drift when absent")

        diffs2 = guard.compare_live_to_baseline(_live({"base_rpm": 14}), baseline)
        self.assertEqual(diffs2, [], "stale persisted tier-managed value must not produce drift")

    def test_non_tier_managed_extra_still_diffs(self) -> None:
        baseline = _baseline({"base_rpm": 56, "enable_tls_fingerprint": True})
        diffs = guard.compare_live_to_baseline(_live({"enable_tls_fingerprint": False}), baseline)
        paths = [d["path"] for d in diffs]
        self.assertIn("/extra/enable_tls_fingerprint", paths)
        self.assertNotIn("/extra/base_rpm", paths)

    def test_all_eight_tier_managed_keys_covered(self) -> None:
        # Mirror of Go model.TierManagedExtraKeys — guard must exclude exactly these.
        self.assertEqual(guard.TIER_MANAGED_EXTRA_KEYS, frozenset({
            "base_rpm", "max_sessions", "rpm_sticky_buffer",
            "session_idle_timeout_minutes",
            "cache_ttl_override_enabled",
            "cache_ttl_override_target",
        }))
        baseline = _baseline({k: 1 for k in guard.TIER_MANAGED_EXTRA_KEYS})
        self.assertEqual(guard.compare_live_to_baseline(_live({}), baseline), [])

    def test_missing_account_still_flagged(self) -> None:
        diffs = guard.compare_live_to_baseline({"found": False}, _baseline({"base_rpm": 56}))
        self.assertEqual(diffs, [{"path": "/account",
                                  "expected": "existing anthropic oauth account",
                                  "actual": None}])


if __name__ == "__main__":
    unittest.main()
