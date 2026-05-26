#!/usr/bin/env python3
"""Unit tests for anthropic group Claude Code only (surface D).

stdlib-only; run via preflight unittest discover -s ops/anthropic.
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import pathlib
import tempfile
import unittest

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "manage-anthropic-config.py"
_spec = importlib.util.spec_from_file_location("manage_anthropic_config_group", _MOD_PATH)
mgr = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(mgr)


def _group(gid: int, name: str, *, claude_code_only: bool = False) -> dict:
    return {
        "id": gid,
        "name": name,
        "platform": "anthropic",
        "status": "active",
        "claude_code_only": claude_code_only,
        "fallback_group_id": None,
    }


def _snapshot(*, edge_groups: list | None = None, prod_groups: list | None = None) -> dict:
    return {
        "version": mgr.SNAPSHOT_VERSION,
        "captured_at": "2026-05-26T00:00:00Z",
        "edges": {
            "uk1": {
                "deployable": True,
                "instance_id": "i-uk1",
                "region": "eu-west-2",
                "anthropic_groups": edge_groups if edge_groups is not None else [],
                "oauth_accounts": [],
            },
        },
        "prod": {
            "instance_id": "i-prod",
            "region": "us-east-1",
            "anthropic_groups": prod_groups if prod_groups is not None else [],
            "anthropic_stubs": [],
        },
    }


class PolicyLoadTest(unittest.TestCase):
    def test_policy_requires_claude_code_only_true(self) -> None:
        p = mgr._load_group_claude_code_policy()
        self.assertEqual(p["platform"], "anthropic")
        self.assertIs(p["claude_code_only"], True)


class PlanGroupClaudeCodeOnlyTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = pathlib.Path(self._tmp.name)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def test_noop_when_all_groups_already_restricted(self) -> None:
        snap_path = self.tmp / "snap.json"
        snap_path.write_text(json.dumps(_snapshot(
            edge_groups=[_group(1, "default", claude_code_only=True)],
            prod_groups=[_group(2, "cc-edges", claude_code_only=True)],
        )))
        out = self.tmp / "plan.json"
        rc = mgr.cmd_plan_group_claude_code_only(argparse.Namespace(
            snapshot=str(snap_path), out=str(out), force_template_rewrite=False,
        ))
        self.assertEqual(rc, 0)
        plan = json.loads(out.read_text())
        self.assertTrue(plan["noop"])
        self.assertEqual(plan["actions"], [])

    def test_emits_edge_and_prod_when_any_group_is_open(self) -> None:
        snap_path = self.tmp / "snap.json"
        snap_path.write_text(json.dumps(_snapshot(
            edge_groups=[_group(1, "default", claude_code_only=False)],
            prod_groups=[
                _group(1, "default", claude_code_only=False),
                _group(15, "cc-edges", claude_code_only=True),
            ],
        )))
        out = self.tmp / "plan.json"
        rc = mgr.cmd_plan_group_claude_code_only(argparse.Namespace(
            snapshot=str(snap_path), out=str(out), force_template_rewrite=False,
        ))
        self.assertEqual(rc, 0)
        plan = json.loads(out.read_text())
        self.assertFalse(plan["noop"])
        self.assertEqual(len(plan["actions"]), 2)
        prod_action = [a for a in plan["actions"] if a["target"]["env"] == "prod"][0]
        self.assertEqual(len(prod_action["variables"]["groups_to_fix"]), 1)
        self.assertEqual(prod_action["variables"]["groups_to_fix"][0]["name"], "default")


class RenderSqlTest(unittest.TestCase):
    def test_sql_sets_true_and_scopes_anthropic(self) -> None:
        sql = mgr.render_anthropic_group_claude_code_sql(True)
        self.assertIn("claude_code_only = true", sql)
        self.assertIn("platform = 'anthropic'", sql)


if __name__ == "__main__":
    unittest.main()
