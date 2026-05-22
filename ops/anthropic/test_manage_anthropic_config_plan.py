#!/usr/bin/env python3
"""Unit tests for the plan-edge-account-tier stage of the anthropic config
orchestrator — specifically the fields_match noop short-circuit and the
--force-template-rewrite escape hatch.

stdlib-only (unittest); CI runs `python3 -m unittest discover -s ops`.
The module under test has a hyphen in its filename, so it is loaded via
importlib rather than a normal import.
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import pathlib
import tempfile
import unittest

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "manage-anthropic-config.py"
_spec = importlib.util.spec_from_file_location("manage_anthropic_config", _MOD_PATH)
mgr = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(mgr)


class PlanEdgeAccountTierTest(unittest.TestCase):
    TIER = "l5"
    EDGE = "us1"
    ACCOUNT = "acct-under-test"

    def setUp(self) -> None:
        # Build a snapshot account whose four match-fields equal the live l5
        # baseline, so the orchestrator's fields_match check is True. Reading
        # the values from the real baselines file (single source of truth)
        # keeps the test green if the baseline numbers are retuned later — it
        # asserts behavior (noop vs action), not specific magic numbers.
        baseline = mgr._load_tier_baselines()[self.TIER]
        self.match_fields = {
            k: baseline[k]
            for k in ("base_rpm", "rpm_sticky_buffer", "concurrency", "max_sessions")
        }
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = pathlib.Path(self._tmp.name)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _write_snapshot(self, account_overrides: dict | None = None) -> pathlib.Path:
        account = {
            "id": 1,
            "name": self.ACCOUNT,
            "type": "oauth",
            "status": "active",
            "platform": "anthropic",
            "stability_tier": self.TIER,
            **self.match_fields,
        }
        if account_overrides:
            account.update(account_overrides)
        snap = {
            "version": mgr.SNAPSHOT_VERSION,
            "captured_at": "2026-05-22T00:00:00Z",
            "edges": {
                self.EDGE: {
                    "deployable": True,
                    "instance_id": "i-test",
                    "region": "us-west-2",
                    "oauth_accounts": [account],
                },
            },
        }
        path = self.tmp / "snap.json"
        path.write_text(json.dumps(snap))
        return path

    def _run_plan(self, snap_path: pathlib.Path, *, force: bool) -> dict:
        out_path = self.tmp / "plan.json"
        ns = argparse.Namespace(
            edge_id=self.EDGE,
            account_name=self.ACCOUNT,
            tier=self.TIER,
            snapshot=str(snap_path),
            out=str(out_path),
            force_template_rewrite=force,
        )
        rc = mgr.cmd_plan_edge_account_tier(ns)
        self.assertEqual(rc, 0)
        return json.loads(out_path.read_text())

    def test_fields_match_without_flag_is_noop(self) -> None:
        plan = self._run_plan(self._write_snapshot(), force=False)
        self.assertTrue(plan["noop"])
        self.assertEqual(plan["actions"], [])
        # R-002: noop intent carries force_template_rewrite for schema parity.
        self.assertIn("force_template_rewrite", plan["intent"])
        self.assertFalse(plan["intent"]["force_template_rewrite"])

    def test_fields_match_with_force_flag_emits_action(self) -> None:
        plan = self._run_plan(self._write_snapshot(), force=True)
        self.assertNotEqual(plan.get("noop"), True)
        self.assertEqual(len(plan["actions"]), 1)
        self.assertTrue(plan["intent"]["force_template_rewrite"])
        action = plan["actions"][0]
        self.assertEqual(action["kind"], "edge_account_tier")
        self.assertEqual(action["variables"]["stability_tier"], self.TIER)

    def test_fields_mismatch_emits_action_regardless_of_flag(self) -> None:
        # Bump one match-field so fields_match is False; the noop branch must
        # not fire even without the force flag.
        bumped = self.match_fields["base_rpm"] + 1
        snap = self._write_snapshot({"base_rpm": bumped})
        plan = self._run_plan(snap, force=False)
        self.assertNotEqual(plan.get("noop"), True)
        self.assertEqual(len(plan["actions"]), 1)


class SingleSourceRenderTest(unittest.TestCase):
    """Tier baseline values live only in the JSON file; the apply SQL is rendered
    from it at runtime. These tests lock that wiring so the retired dual-source
    SQL template (with its own hand-aligned VALUES table) cannot creep back."""

    def test_render_embeds_json_values(self) -> None:
        # Derive expected numbers from the JSON (single source) so this stays
        # green if tiers are retuned — it asserts the values flow into the SQL,
        # not specific magic numbers.
        baseline_json = mgr.load_json_file(mgr.TIER_BASELINES, "tier baselines")
        eff = mgr._GUARD.effective_baseline_for_tier(baseline_json, "l5")["baseline"]
        sql = mgr.render_edge_account_tier_sql("acct-x", "l5", "us1")
        extra = eff["extra"]
        for key in (
            "base_rpm", "rpm_sticky_buffer", "max_sessions", "window_cost_limit",
            "session_idle_timeout_minutes", "window_cost_sticky_reserve",
        ):
            self.assertIn(f"'{key}', '{extra[key]}'::jsonb", sql)
        self.assertIn(f"concurrency = {eff['account']['concurrency']}", sql)
        self.assertIn(f"priority = {eff['account']['priority']}", sql)
        self.assertIn("'stability_tier', '\"l5\"'::jsonb", sql)

    def test_retired_sql_template_absent(self) -> None:
        legacy = mgr.REPO_ROOT / "deploy/aws/stage0/anthropic-oauth-stability-tiered-apply-template.sql"
        self.assertFalse(
            legacy.exists(),
            "retired dual-source SQL apply template reappeared; tier values must "
            "live only in anthropic-oauth-stability-baselines-tiered.json",
        )


if __name__ == "__main__":
    unittest.main()
