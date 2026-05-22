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
        # Build a snapshot account whose pipeline-owned fields all equal the live
        # baseline, so the orchestrator's fields_match check is True. Reading the
        # values from the real baselines file (single source of truth) keeps the
        # test green if the baseline numbers are retuned later — it asserts
        # behavior (noop vs action), not specific magic numbers. The field set is
        # _TIER_BASELINE_FIELDS (R-001: wider than the original 4).
        baseline = mgr._load_tier_baselines()[self.TIER]
        self.match_fields = {k: baseline[k] for k in mgr._TIER_BASELINE_FIELDS}
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


class PlanTierBumpTest(unittest.TestCase):
    """plan-tier-bump enumerates every account on a tier across deployable edges
    into one multi-action plan — the correct shape for a tier-VALUE bump."""

    TIER = "l5"

    def setUp(self) -> None:
        baseline = mgr._load_tier_baselines()[self.TIER]
        self.match_fields = {k: baseline[k] for k in mgr._TIER_BASELINE_FIELDS}
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = pathlib.Path(self._tmp.name)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _acct(self, name, *, tier=None, overrides=None):
        a = {"id": 0, "name": name, "type": "oauth", "status": "active",
             "platform": "anthropic", "stability_tier": tier or self.TIER,
             **self.match_fields}
        if overrides:
            a.update(overrides)
        return a

    def _write_snapshot(self, edges: dict) -> pathlib.Path:
        snap = {"version": mgr.SNAPSHOT_VERSION, "captured_at": "2026-05-22T00:00:00Z",
                "edges": edges}
        path = self.tmp / "snap.json"
        path.write_text(json.dumps(snap))
        return path

    def _run(self, snap_path, *, force=False) -> dict:
        out = self.tmp / "plan.json"
        ns = argparse.Namespace(tier=self.TIER, snapshot=str(snap_path),
                                out=str(out), force_template_rewrite=force)
        self.assertEqual(mgr.cmd_plan_tier_bump(ns), 0)
        return json.loads(out.read_text())

    def test_enumerates_all_matching_tier_accounts_when_value_bumped(self) -> None:
        # Stale fields (bumped base_rpm) → both l5 accounts get an action; a
        # planned edge and an off-tier account are excluded.
        stale = {"base_rpm": self.match_fields["base_rpm"] + 1}
        snap = self._write_snapshot({
            "us1": {"deployable": True, "instance_id": "i-1", "region": "us-west-2",
                    "oauth_accounts": [self._acct("a1", overrides=stale),
                                       self._acct("a4", overrides=stale),
                                       self._acct("other", tier="l3")]},
            "uk1": {"deployable": False, "skipped_reason": "planned"},
        })
        plan = self._run(snap)
        names = sorted(a["target"]["account_name"] for a in plan["actions"])
        self.assertEqual(names, ["a1", "a4"])
        self.assertEqual([a["step"] for a in plan["actions"]], [1, 2])
        for a in plan["actions"]:
            self.assertEqual(a["expected_after"]["max_sessions"],
                             self.match_fields["max_sessions"])
        self.assertFalse(plan["noop"])

    def test_already_matching_accounts_are_skipped_not_actioned(self) -> None:
        snap = self._write_snapshot({
            "us1": {"deployable": True, "instance_id": "i-1", "region": "us-west-2",
                    "oauth_accounts": [self._acct("a1")]},
        })
        plan = self._run(snap)
        self.assertEqual(plan["actions"], [])
        self.assertTrue(plan["noop"])
        self.assertEqual(plan["summary"]["skipped"], 1)

    def test_force_includes_matching_accounts(self) -> None:
        snap = self._write_snapshot({
            "us1": {"deployable": True, "instance_id": "i-1", "region": "us-west-2",
                    "oauth_accounts": [self._acct("a1")]},
        })
        plan = self._run(snap, force=True)
        self.assertEqual(len(plan["actions"]), 1)
        self.assertFalse(plan["noop"])

    def test_window_cost_only_change_is_not_skipped(self) -> None:
        # R-001 regression: an account matching the 4 capacity fields but with a
        # stale window_cost_limit must NOT be skipped, and expected_after must
        # carry window_cost_limit so verify can confirm it landed.
        stale_cost = self.match_fields["window_cost_limit"] - 1
        snap = self._write_snapshot({
            "us1": {"deployable": True, "instance_id": "i-1", "region": "us-west-2",
                    "oauth_accounts": [self._acct("a1", overrides={"window_cost_limit": stale_cost})]},
        })
        plan = self._run(snap)
        self.assertEqual(len(plan["actions"]), 1, "window_cost_limit-only drift must emit an action")
        self.assertFalse(plan["noop"])
        self.assertIn("window_cost_limit", plan["actions"][0]["expected_after"])


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
        self.assertIn("UPDATE users u SET concurrency", sql)
        self.assertIn("SUM(a.concurrency)", sql)

    def test_retired_sql_template_absent(self) -> None:
        legacy = mgr.REPO_ROOT / "deploy/aws/stage0/anthropic-oauth-stability-tiered-apply-template.sql"
        self.assertFalse(
            legacy.exists(),
            "retired dual-source SQL apply template reappeared; tier values must "
            "live only in anthropic-oauth-stability-baselines-tiered.json",
        )


class InjectBeforeCommitTest(unittest.TestCase):
    def test_fragments_land_before_commit(self) -> None:
        tx = "BEGIN;\nSELECT 1;\nCOMMIT;\n"
        out = mgr._inject_sql_before_commit(tx, "SELECT 2;")
        self.assertGreater(out.index("SELECT 2;"), out.index("SELECT 1;"))
        self.assertLess(out.index("COMMIT;"), len(out))
        self.assertGreater(out.rindex("COMMIT;"), out.index("SELECT 2;"))


if __name__ == "__main__":
    unittest.main()
