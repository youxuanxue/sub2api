#!/usr/bin/env python3
"""Tests for anthropic OAuth priority rebalance orchestrator (plan scoring,
exit codes, SQL render quoting guard).

Loads `rebalance-anthropic-priority.py` via importlib (hyphenated filename).

Run: python3 -m unittest discover -s ops/anthropic -p \"test_*.py\"
(preflight invokes the same discovery).
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import pathlib
import tempfile
import unittest
import unittest.mock as mock

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "rebalance-anthropic-priority.py"
_spec = importlib.util.spec_from_file_location("rebalance_anthropic_priority", _MOD_PATH)
rb = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(rb)


class RebalanceAnthropicPriorityPlanTest(unittest.TestCase):
    EDGE = "uk1"

    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = pathlib.Path(self._tmp.name)
        tier_base = rb._load_tier_base_priority()
        self.assertIn("l2", tier_base)
        self.l2_base = tier_base["l2"]

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _write_snapshot(self, accounts: list[dict], captured_at: str) -> pathlib.Path:
        snap = {
            "version": rb.SNAPSHOT_VERSION,
            "captured_at": captured_at,
            "edges": {
                self.EDGE: {
                    "deployable": True,
                    "instance_id": "i-test",
                    "oauth_accounts": accounts,
                },
            },
        }
        path = self.tmp / "snap.json"
        path.write_text(json.dumps(snap), encoding="utf-8")
        return path

    def test_plan_any_stale_true_when_stale_single_account_is_noop(self) -> None:
        """Single active stale account already at tier base → no DB action but exit 1."""
        snap_path = self._write_snapshot(
            [
                {
                    "id": 1,
                    "name": "stale-alone",
                    "status": "active",
                    "stability_tier": "l2",
                    "priority": self.l2_base,
                    "session_window_utilization": 0.2,
                    "session_window_end": "2099-01-01T00:00:00Z",
                },
            ],
            captured_at="2026-05-21T12:00:00Z",
        )
        plan_path = self.tmp / "plan.json"
        ns = argparse.Namespace(
            snapshot=str(snap_path),
            out=str(plan_path),
            stale_minutes=120,
            edge_id=self.EDGE,
        )
        rc = rb.cmd_plan(ns)
        self.assertEqual(rc, 1, "stale accounts must bump exit code even if no UPDATE")
        plan = json.loads(plan_path.read_text(encoding="utf-8"))
        self.assertTrue(plan["summary"]["any_stale"])
        self.assertEqual(plan["actions"], [])
        self.assertGreaterEqual(plan["tier_summaries"][0]["stale_count"], 1)

    def test_plan_fresh_single_account_exit_zero(self) -> None:
        snap_path = self._write_snapshot(
            [
                {
                    "id": 42,
                    "name": "fresh-one",
                    "status": "active",
                    "stability_tier": "l2",
                    "priority": self.l2_base,
                    "session_window_utilization": 0.4,
                    "passive_usage_7d_utilization": 0.1,
                    "passive_usage_sampled_at": "2026-05-21T11:58:00Z",
                    "session_window_end": "2099-01-01T00:00:00Z",
                },
            ],
            captured_at="2026-05-21T12:00:00Z",
        )
        plan_path = self.tmp / "plan.json"
        ns = argparse.Namespace(
            snapshot=str(snap_path),
            out=str(plan_path),
            stale_minutes=120,
            edge_id=self.EDGE,
        )
        rc = rb.cmd_plan(ns)
        self.assertEqual(rc, 0)
        plan = json.loads(plan_path.read_text(encoding="utf-8"))
        self.assertFalse(plan["summary"]["any_stale"])

    def test_render_apply_sql_escapes_quote_rejected(self) -> None:
        with self.assertRaises(SystemExit) as ar:
            rb.render_apply_sql("bad'name", self.l2_base)
        self.assertEqual(ar.exception.code, 2)

    def test_render_apply_sql_positive(self) -> None:
        with mock.patch.object(rb, "_read_template", return_value="-- tmpl\n"):
            sql = rb.render_apply_sql("ok-account_1.slug", self.l2_base + 2)
        self.assertIn("\\set account_name 'ok-account_1.slug'", sql)
        self.assertIn(f"\\set new_priority {self.l2_base + 2}", sql)

    def test_score_missing_7d_is_dominated_by_5h(self) -> None:
        captured = rb._parse_utc_iso("2026-05-21T12:00:00Z")
        assert captured is not None
        snap = rb._score_account(
            {
                "status": "active",
                "session_window_utilization": 0.5,
                "passive_usage_sampled_at": "2026-05-21T11:58:00Z",
                "session_window_end": "2099-01-01T00:00:00Z",
            },
            captured,
            120,
        )
        self.assertAlmostEqual(float(snap["remaining_7d"]), 1.0)
        self.assertAlmostEqual(float(snap["remaining_5h"]), 0.5)
        self.assertAlmostEqual(float(snap["remaining_score"]), 0.5)


if __name__ == "__main__":
    unittest.main()
