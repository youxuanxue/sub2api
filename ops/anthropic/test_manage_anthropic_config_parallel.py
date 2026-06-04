#!/usr/bin/env python3
"""Unit tests for remediate P0 parallel + batch guard paths (stdlib-only)."""
from __future__ import annotations

import importlib.util
import json
import pathlib
import time
import unittest
from unittest import mock

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "manage-anthropic-config.py"
_spec = importlib.util.spec_from_file_location("manage_anthropic_config", _MOD_PATH)
mgr = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(mgr)

_GUARD_PATH = pathlib.Path(__file__).resolve().parent / "check-edge-oauth-stability.py"
_guard_spec = importlib.util.spec_from_file_location("check_edge_oauth_stability", _GUARD_PATH)
guard = importlib.util.module_from_spec(_guard_spec)
assert _guard_spec and _guard_spec.loader
_guard_spec.loader.exec_module(guard)


class ParallelOrderedTest(unittest.TestCase):
    def test_run_parallel_ordered_preserves_input_order(self) -> None:
        seen: list[int] = []

        def work(n: int) -> int:
            time.sleep(0.02 * (5 - n))
            seen.append(n)
            return n * 10

        out = mgr._run_parallel_ordered(list(range(5)), work, 4, label="test")
        self.assertEqual(out, [0, 10, 20, 30, 40])
        self.assertEqual(len(seen), 5)


class SqlBundleTest(unittest.TestCase):
    def test_sql_select_body_strips_leading_select(self) -> None:
        self.assertEqual(
            mgr._sql_select_body("SELECT 1 AS x"),
            "1 AS x",
        )

    def test_edge_capture_bundle_aggregates_snapshot_fragments(self) -> None:
        sql = mgr.EDGE_CAPTURE_BUNDLE_SQL
        self.assertIn("oauth_accounts", sql)
        self.assertIn("anthropic_groups", sql)
        self.assertIn("operator_balance", sql)


class GuardBatchTest(unittest.TestCase):
    def test_build_all_oauth_batch_query_aggregates_accounts(self) -> None:
        sql = guard.build_all_oauth_guard_live_batch_query()
        self.assertIn("jsonb_agg", sql)
        self.assertIn("platform = 'anthropic'", sql)
        self.assertIn("type = 'oauth'", sql)

    def test_guard_items_from_batch_reports_tls_drift(self) -> None:
        baseline = mgr.load_json_file(mgr.TIER_BASELINES, "tier baseline")
        live_row = {
            "account_name": "acct-a",
            "found": True,
            "account": {
                "name": "acct-a",
                "platform": "anthropic",
                "type": "oauth",
                "stability_tier": "l5",
                "concurrency": 10,
                "priority": 50,
                "rate_multiplier": 1.0,
                "auto_pause_on_expired": True,
                "channel_type": 0,
            },
            "credentials": baseline["shared_baseline"]["credentials"],
            "extra": {
                "enable_tls_fingerprint": True,
                "tls_fingerprint_profile_id": "999",
            },
            "groups": {"ids": [], "names": []},
            "tls_profile": {"name": "wrong-profile"},
        }
        items = mgr._guard_items_from_batch(
            "uk1",
            {"edge_id": "uk1", "region": "eu-west-2", "instance_id": "i-test"},
            [live_row],
            baseline,
        )
        self.assertEqual(len(items), 1)
        self.assertEqual(items[0]["status"], "drift")
        self.assertGreater(items[0]["diff_count"], 0)

    def test_run_guard_batch_for_edge_uses_one_ssm_call(self) -> None:
        baseline = mgr.load_json_file(mgr.TIER_BASELINES, "tier baseline")
        tier_key = "l5"
        tier_base = mgr._GUARD.effective_baseline_for_tier(baseline, tier_key)
        acct = tier_base["baseline"]["account"]
        live_payload = {
            "found": True,
            "account": {
                **acct,
                "name": "acct-b",
                "stability_tier": tier_key,
            },
            "credentials": tier_base["baseline"]["credentials"],
            "extra": {
                k: v for k, v in tier_base["baseline"]["extra"].items()
                if k not in guard.TIER_MANAGED_EXTRA_KEYS
            },
            "groups": {"ids": [], "names": []},
            "tls_profile": tier_base["baseline"]["tls_profile"],
        }
        batch_json = [
            {"account_name": "acct-b", **live_payload},
        ]
        ident = mock.Mock(
            region="eu-west-2",
            instance_id="i-mock",
            domain="api-uk1.tokenkey.dev",
            routing="lightsail",
        )
        with mock.patch.object(
            mgr._EDGE_SSM,
            "resolve_edge_execution_identity",
            return_value=ident,
        ), mock.patch.object(
            mgr,
            "ssm_run_sql",
            return_value=(json.dumps(batch_json), "cid-mock"),
        ) as ssm_mock:
            result = mgr._run_guard_batch_for_edge("uk1", allow_planned=False)

        self.assertEqual(ssm_mock.call_count, 1)
        self.assertEqual(result["exit_code"], 0)
        self.assertEqual(result["report"]["summary"]["drift_count"], 0)


class ApplyGroupKeyTest(unittest.TestCase):
    def test_apply_group_key_same_edge_same_instance(self) -> None:
        with mock.patch.object(
            mgr,
            "_resolve_edge_target",
            return_value=("eu-west-2", "i-uk1", "edge:uk1"),
        ):
            a1 = {
                "step": 1,
                "kind": "edge_account_tier",
                "target": {"edge_id": "uk1", "account_name": "a"},
                "variables": {},
            }
            a2 = {
                "step": 2,
                "kind": "edge_account_tier",
                "target": {"edge_id": "uk1", "account_name": "b"},
                "variables": {},
            }
            k1 = mgr._apply_group_instance_key(a1)
            k2 = mgr._apply_group_instance_key(a2)
        self.assertEqual(k1, k2)


if __name__ == "__main__":
    unittest.main()
