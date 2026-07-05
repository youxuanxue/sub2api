#!/usr/bin/env python3
"""Unit tests for edge operator balance surface (E) of manage-anthropic-config.py."""
from __future__ import annotations

import argparse
import importlib.util
import json
import pathlib
import tempfile
import unittest

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "manage-anthropic-config.py"
_spec = importlib.util.spec_from_file_location("manage_anthropic_config_balance", _MOD_PATH)
mgr = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(mgr)


def _edge(*, balance: float | None, deployable: bool = True, exists: bool = True) -> dict:
    return {
        "deployable": deployable,
        "instance_id": "i-test",
        "region": "us-west-2",
        "oauth_accounts": [],
        "operator_user_balance": balance,
        "operator_user_exists": exists,
        "operator_user_concurrency": 10,
        "schedulable_concurrency_sum": 10,
    }


def _snap(edges: dict) -> dict:
    return {
        "version": mgr.SNAPSHOT_VERSION,
        "captured_at": "2026-05-28T00:00:00Z",
        "edges": edges,
        "prod": {"skipped_reason": "test"},
    }


class OperatorBalancePolicyTest(unittest.TestCase):
    def test_load_policy_defaults(self) -> None:
        pol = mgr._load_operator_balance_policy()
        self.assertEqual(pol["operator_user_id"], 1)
        self.assertEqual(pol["min_balance_threshold"], 100)
        self.assertEqual(pol["default_balance"], 9999999)

    def test_needs_top_up(self) -> None:
        self.assertTrue(mgr._operator_balance_needs_top_up(0, threshold=100))
        self.assertTrue(mgr._operator_balance_needs_top_up(99.99, threshold=100))
        self.assertFalse(mgr._operator_balance_needs_top_up(100, threshold=100))
        self.assertFalse(mgr._operator_balance_needs_top_up(9999999, threshold=100))
        self.assertTrue(mgr._operator_balance_needs_top_up(None, threshold=100))


class RenderEdgeOperatorBalanceSqlTest(unittest.TestCase):
    def test_sql_sets_balance_and_checks_row(self) -> None:
        sql = mgr.render_edge_operator_balance_sql(9999999)
        self.assertIn("BEGIN;", sql)
        self.assertIn("9999999", sql)
        self.assertIn("GET DIAGNOSTICS rows = ROW_COUNT", sql)
        self.assertIn(f"WHERE id = {mgr.ADMIN_OPERATOR_USER_CONCURRENCY_SYNC_ID}", sql)


class PlanEdgeOperatorBalanceTest(unittest.TestCase):
    def test_plans_top_up_when_below_threshold(self) -> None:
        snap = _snap({
            "us1": _edge(balance=50),
            "us2": _edge(balance=9999999),
        })
        with tempfile.TemporaryDirectory() as td:
            snap_path = pathlib.Path(td) / "snap.json"
            plan_path = pathlib.Path(td) / "plan.json"
            snap_path.write_text(json.dumps(snap))
            rc = mgr.cmd_plan_edge_operator_balance(argparse.Namespace(
                snapshot=str(snap_path), out=str(plan_path), force_template_rewrite=False,
            ))
            self.assertEqual(rc, 0)
            plan = json.loads(plan_path.read_text())
        self.assertFalse(plan["noop"])
        self.assertEqual(len(plan["actions"]), 1)
        self.assertEqual(plan["actions"][0]["kind"], "edge_operator_balance")
        self.assertEqual(plan["actions"][0]["target"]["edge_id"], "us1")
        self.assertEqual(plan["actions"][0]["expected_after"]["operator_user_balance"], 9999999)

    def test_noop_when_all_edges_above_threshold(self) -> None:
        snap = _snap({"us1": _edge(balance=5000)})
        with tempfile.TemporaryDirectory() as td:
            snap_path = pathlib.Path(td) / "snap.json"
            plan_path = pathlib.Path(td) / "plan.json"
            snap_path.write_text(json.dumps(snap))
            mgr.cmd_plan_edge_operator_balance(argparse.Namespace(
                snapshot=str(snap_path), out=str(plan_path), force_template_rewrite=False,
            ))
            plan = json.loads(plan_path.read_text())
        self.assertTrue(plan["noop"])
        self.assertEqual(plan["actions"], [])


class BalanceViolationsFromSnapshotTest(unittest.TestCase):
    def test_detects_violation_and_ok(self) -> None:
        pol = mgr._load_operator_balance_policy()
        snap = _snap({"low": _edge(balance=1), "ok": _edge(balance=1000)})
        items = mgr._balance_violations_from_snapshot(snap, pol)
        self.assertEqual(len(items), 1)
        self.assertEqual(items[0]["edge_id"], "low")
        self.assertEqual(items[0]["status"], "violation")


if __name__ == "__main__":
    unittest.main()
