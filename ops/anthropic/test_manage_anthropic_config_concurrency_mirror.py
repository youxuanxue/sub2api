#!/usr/bin/env python3
"""Unit tests for the concurrency-mirror stage (surface C) of the anthropic
config orchestrator — the four-hop schedulable-concurrency cascade:

  - _normalize_base_url / _build_domain_to_edge: the prod-stub↔edge link is
    resolved ONLY from edge-targets.json ``domain`` (no slug inference)
  - render_edge_operator_concurrency_sql: operator sync filters schedulable=true
  - render_prod_concurrency_mirror_sql: per-stub safe WHERE, int/concurrency
    validation, name quoting, ordering (stubs before operator), operator
    subquery filters schedulable=true, empty stub_updates → operator-only
  - plan-concurrency-mirror: edge operator drift, prod stub mirror, prod
    operator delta math, noop idempotency, Σ-schedulable=0 safety rail,
    unmatched stubs excluded, --force

stdlib-only (unittest); CI runs `python3 -m unittest discover -s ops`.
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import pathlib
import tempfile
import unittest

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "manage-anthropic-config.py"
_spec = importlib.util.spec_from_file_location("manage_anthropic_config_cmirror", _MOD_PATH)
mgr = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(mgr)


def _edge(sched_sum: int, operator: int, *, deployable: bool = True) -> dict:
    return {
        "deployable": deployable,
        "instance_id": "i-test",
        "region": "us-west-2",
        "stack": "tokenkey-edge-test-stage0",
        "domain": "api-test.tokenkey.dev",
        "oauth_accounts": [],
        "operator_user_concurrency": operator,
        "schedulable_concurrency_sum": sched_sum,
    }


def _stub(stub_id: int, name: str, base_url: str | None, concurrency: int,
          *, schedulable: bool = True) -> dict:
    return {
        "id": stub_id, "name": name, "type": "apikey", "platform": "anthropic",
        "status": "active", "schedulable": schedulable, "concurrency": concurrency,
        "priority": 1, "cred_base_url": base_url,
        "cred_pool_mode": True, "cred_pool_mode_retry_count": 1,
    }


def _snapshot(*, edges: dict, stubs: list[dict],
              prod_sum: int, prod_operator: int) -> dict:
    return {
        "version": mgr.SNAPSHOT_VERSION,
        "captured_at": "2026-05-23T00:00:00Z",
        "edges": edges,
        "prod": {
            "instance_id": "i-prod", "region": "us-east-1",
            "stack": "tokenkey-prod-stage0", "domain": "api.tokenkey.dev",
            "anthropic_stubs": stubs,
            "operator_user_concurrency": prod_operator,
            "schedulable_concurrency_sum": prod_sum,
        },
    }


class NormalizeAndDomainMapTest(unittest.TestCase):
    def test_normalize_strips_scheme_and_trailing_slash(self) -> None:
        self.assertEqual(mgr._normalize_base_url("https://api-us1.tokenkey.dev/"),
                         "api-us1.tokenkey.dev")
        self.assertEqual(mgr._normalize_base_url("http://api-uk1.tokenkey.dev"),
                         "api-uk1.tokenkey.dev")
        self.assertEqual(mgr._normalize_base_url("  api-us1.tokenkey.dev  "),
                         "api-us1.tokenkey.dev")
        self.assertEqual(mgr._normalize_base_url(None), "")
        self.assertEqual(mgr._normalize_base_url(""), "")

    def test_domain_map_keys_are_normalized_domains(self) -> None:
        matrix = {"targets": {
            "us1": {"domain": "api-us1.tokenkey.dev"},
            "uk1": {"domain": "https://api-uk1.tokenkey.dev/"},
            "blank": {},
        }}
        m = mgr._build_domain_to_edge(matrix)
        self.assertEqual(m.get("api-us1.tokenkey.dev"), "us1")
        self.assertEqual(m.get("api-uk1.tokenkey.dev"), "uk1")
        # an edge with no domain contributes no key (cannot be matched).
        self.assertNotIn("", m)

    def test_real_edge_targets_domains_map(self) -> None:
        # The shipped edge-targets.json is the single source of truth; us1/uk1
        # must resolve from their domain so a prod stub can find its edge.
        matrix = mgr.load_json_file(mgr.EDGE_MATRIX, "edge matrix")
        m = mgr._build_domain_to_edge(matrix)
        self.assertEqual(m.get("api-us1.tokenkey.dev"), "us1")
        self.assertEqual(m.get("api-uk1.tokenkey.dev"), "uk1")


class RenderEdgeOperatorConcurrencyTest(unittest.TestCase):
    def test_sql_filters_schedulable_and_sums_concurrency(self) -> None:
        sql = mgr.render_edge_operator_concurrency_sql("us1")
        self.assertIn("BEGIN;", sql)
        self.assertIn("\nCOMMIT;", sql)
        self.assertIn("UPDATE users u SET concurrency", sql)
        self.assertIn("SUM(a.concurrency)", sql)
        self.assertIn("a.platform = 'anthropic'", sql)
        self.assertIn("a.schedulable = true", sql)
        self.assertIn(f"u.id = {mgr.ADMIN_OPERATOR_USER_CONCURRENCY_SYNC_ID}", sql)


class RenderProdConcurrencyMirrorTest(unittest.TestCase):
    def test_per_stub_safe_where_and_value(self) -> None:
        sql = mgr.render_prod_concurrency_mirror_sql(
            [{"id": 42, "name": "cc-us1", "concurrency": 18}])
        self.assertIn("BEGIN;", sql)
        self.assertIn("\nCOMMIT;", sql)
        self.assertIn("UPDATE accounts SET", sql)
        self.assertIn("concurrency = 18", sql)
        self.assertIn("WHERE id = 42", sql)
        self.assertIn("AND name = 'cc-us1'", sql)
        self.assertIn("AND platform = 'anthropic'", sql)
        self.assertIn("AND type = 'apikey'", sql)
        self.assertIn("AND deleted_at IS NULL", sql)

    def test_operator_sync_runs_after_stub_updates(self) -> None:
        sql = mgr.render_prod_concurrency_mirror_sql(
            [{"id": 42, "name": "cc-us1", "concurrency": 18}])
        # The operator sync subquery (Σ schedulable) must appear AFTER the stub
        # UPDATE so it reads the just-written concurrency.
        stub_pos = sql.index("WHERE id = 42")
        op_pos = sql.index("UPDATE users u SET concurrency")
        self.assertLess(stub_pos, op_pos, "stub update must precede operator sync")
        self.assertIn("a.schedulable = true", sql[op_pos:])

    def test_quotes_apostrophes_in_name(self) -> None:
        sql = mgr.render_prod_concurrency_mirror_sql(
            [{"id": 7, "name": "weird'name", "concurrency": 5}])
        self.assertIn("AND name = 'weird''name'", sql)

    def test_rejects_non_int_id(self) -> None:
        with self.assertRaises(SystemExit):
            mgr.render_prod_concurrency_mirror_sql(
                [{"id": "42", "name": "cc-us1", "concurrency": 18}])

    def test_refuses_zero_or_negative_concurrency(self) -> None:
        for bad in (0, -1):
            with self.assertRaises(SystemExit):
                mgr.render_prod_concurrency_mirror_sql(
                    [{"id": 42, "name": "cc-us1", "concurrency": bad}])

    def test_empty_stub_updates_renders_operator_only(self) -> None:
        sql = mgr.render_prod_concurrency_mirror_sql([])
        self.assertIn("BEGIN;", sql)
        self.assertIn("\nCOMMIT;", sql)
        self.assertNotIn("UPDATE accounts SET", sql)
        self.assertIn("UPDATE users u SET concurrency", sql)


class PlanConcurrencyMirrorTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = pathlib.Path(self._tmp.name)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _plan(self, snap: dict, *, force: bool = False) -> dict:
        snap_path = self.tmp / "snap.json"
        snap_path.write_text(json.dumps(snap))
        out = self.tmp / "plan.json"
        ns = argparse.Namespace(snapshot=str(snap_path), out=str(out),
                                force_template_rewrite=force)
        self.assertEqual(mgr.cmd_plan_concurrency_mirror(ns), 0)
        return json.loads(out.read_text())

    def test_pool_raise_cascades_edge_stub_and_prod_operator(self) -> None:
        # us1 oauth pool raised to 18 but edge operator stuck at 20, cc-us1 stub
        # stuck at 16, prod operator stuck at 319. uk1 already aligned.
        snap = _snapshot(
            edges={
                "us1": _edge(sched_sum=18, operator=20),
                "uk1": _edge(sched_sum=3, operator=3),
            },
            stubs=[
                _stub(42, "cc-us1", "https://api-us1.tokenkey.dev", 16),
                _stub(40, "cc-uk1", "https://api-uk1.tokenkey.dev", 3, schedulable=False),
                _stub(43, "tokensea-0.4", "https://agent.tokensea.ai", 100),
            ],
            prod_sum=316, prod_operator=319,
        )
        plan = self._plan(snap)
        self.assertFalse(plan["noop"])
        kinds = [a["kind"] for a in plan["actions"]]
        self.assertEqual(kinds, ["edge_operator_concurrency", "prod_concurrency_mirror"])

        edge_act = plan["actions"][0]
        self.assertEqual(edge_act["target"]["edge_id"], "us1")
        self.assertEqual(edge_act["expected_after"]["operator_user_concurrency"], 18)

        prod_act = plan["actions"][1]
        self.assertEqual(prod_act["target"]["env"], "prod")
        # only cc-us1 is updated (matched + drifted); cc-uk1 aligned, tokensea unmatched.
        self.assertEqual(prod_act["expected_after"]["stub_concurrency"], {"42": 18})
        # delta from cc-us1 (schedulable) 16→18 = +2 → prod operator 316+2 = 318.
        self.assertEqual(prod_act["expected_after"]["operator_user_concurrency"], 318)
        # the variables carry the matched edge for audit.
        self.assertEqual(prod_act["variables"]["stub_updates"][0]["matched_edge"], "us1")
        self.assertEqual(plan["summary"]["skipped_unmatched_stubs"], 1)

    def test_fully_aligned_is_noop(self) -> None:
        snap = _snapshot(
            edges={"us1": _edge(sched_sum=18, operator=18)},
            stubs=[_stub(42, "cc-us1", "https://api-us1.tokenkey.dev", 18)],
            prod_sum=318, prod_operator=318,
        )
        plan = self._plan(snap)
        self.assertTrue(plan["noop"])
        self.assertEqual(plan["actions"], [])

    def test_zero_schedulable_edge_skips_stub_no_write_zero(self) -> None:
        # us1 Σ schedulable = 0 (all accounts parked). The stub must NOT be driven
        # to 0, and the edge operator must NOT be driven to 0.
        snap = _snapshot(
            edges={"us1": _edge(sched_sum=0, operator=18)},
            stubs=[_stub(42, "cc-us1", "https://api-us1.tokenkey.dev", 16)],
            prod_sum=300, prod_operator=300,
        )
        plan = self._plan(snap)
        self.assertTrue(plan["noop"])
        self.assertEqual(plan["actions"], [])
        self.assertEqual(plan["summary"]["skipped_edges"], 1)
        self.assertEqual(plan["summary"]["skipped_zero_edges"], 1)

    def test_unmatched_stub_excluded(self) -> None:
        snap = _snapshot(
            edges={"us1": _edge(sched_sum=18, operator=18)},
            stubs=[
                _stub(43, "tokensea-0.4", "https://agent.tokensea.ai", 100),
                _stub(45, "ds-fallback", "https://api.deepseek.com/anthropic", 100),
            ],
            prod_sum=200, prod_operator=200,
        )
        plan = self._plan(snap)
        self.assertTrue(plan["noop"])
        self.assertEqual(plan["summary"]["skipped_unmatched_stubs"], 2)

    def test_operator_only_drift_emits_prod_action_with_empty_stub_updates(self) -> None:
        # No stub drift (cc-us1 already 18) but prod operator drifted 319 vs 318.
        snap = _snapshot(
            edges={"us1": _edge(sched_sum=18, operator=18)},
            stubs=[_stub(42, "cc-us1", "https://api-us1.tokenkey.dev", 18)],
            prod_sum=318, prod_operator=319,
        )
        plan = self._plan(snap)
        self.assertFalse(plan["noop"])
        self.assertEqual([a["kind"] for a in plan["actions"]], ["prod_concurrency_mirror"])
        act = plan["actions"][0]
        self.assertEqual(act["variables"]["stub_updates"], [])
        self.assertEqual(act["expected_after"]["stub_concurrency"], {})
        self.assertEqual(act["expected_after"]["operator_user_concurrency"], 318)

    def test_force_includes_aligned_edges_and_stubs(self) -> None:
        snap = _snapshot(
            edges={
                "us1": _edge(sched_sum=18, operator=18),
                "uk1": _edge(sched_sum=3, operator=3),
            },
            stubs=[
                _stub(42, "cc-us1", "https://api-us1.tokenkey.dev", 18),
                _stub(40, "cc-uk1", "https://api-uk1.tokenkey.dev", 3),
            ],
            prod_sum=318, prod_operator=318,
        )
        plan = self._plan(snap, force=True)
        self.assertFalse(plan["noop"])
        edge_acts = [a for a in plan["actions"] if a["kind"] == "edge_operator_concurrency"]
        prod_acts = [a for a in plan["actions"] if a["kind"] == "prod_concurrency_mirror"]
        self.assertEqual(sorted(a["target"]["edge_id"] for a in edge_acts), ["uk1", "us1"])
        self.assertEqual(len(prod_acts), 1)
        self.assertEqual(sorted(prod_acts[0]["expected_after"]["stub_concurrency"].keys()),
                         ["40", "42"])


class ApplyDispatchTest(unittest.TestCase):
    """Verify cmd_apply routes the two new kinds to the right targets and renders
    the expected SQL, without touching SSM (helper monkey-patched)."""

    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = pathlib.Path(self._tmp.name)
        self._orig_ssm = mgr.ssm_run_sql_b64
        self._orig_resolve = mgr.resolve_instance_id
        self._orig_resolve_edge = mgr._resolve_edge_target
        self.captured: list[dict] = []

        def fake_ssm(region, instance_id, sql_b64, comment):
            import base64 as _b
            self.captured.append({
                "region": region, "instance_id": instance_id,
                "sql": _b.b64decode(sql_b64).decode("utf-8"), "comment": comment,
            })
            return ("UPDATE 1", "cid-fake", True, "")

        mgr.ssm_run_sql_b64 = fake_ssm
        mgr.resolve_instance_id = lambda region, stack: f"i-fake-{stack}"
        mgr._resolve_edge_target = lambda edge_id: (
            "us-west-2", f"i-fake-{edge_id}", f"edge:{edge_id}",
        )

    def tearDown(self) -> None:
        mgr.ssm_run_sql_b64 = self._orig_ssm
        mgr.resolve_instance_id = self._orig_resolve
        mgr._resolve_edge_target = self._orig_resolve_edge
        self._tmp.cleanup()

    def _apply(self, actions: list[dict]) -> None:
        plan = {
            "version": mgr.PLAN_VERSION, "kind": "concurrency_mirror",
            "confirm_code": mgr.CONFIRM_CODE, "intent": {}, "actions": actions,
        }
        plan_path = self.tmp / "plan.json"
        plan_path.write_text(json.dumps(plan))
        ns = argparse.Namespace(plan=str(plan_path), confirm=mgr.CONFIRM_CODE,
                                job_dir=str(self.tmp / "job"), json=False)
        self.assertEqual(mgr.cmd_apply(ns), 0)

    def test_edge_operator_dispatches_to_edge_target(self) -> None:
        self._apply([{
            "step": 1, "kind": "edge_operator_concurrency",
            "target": {"env": "edge", "edge_id": "us1"},
            "variables": {"edge_id": "us1", "schedulable_concurrency_sum": 18},
            "expected_after": {"operator_user_concurrency": 18},
        }])
        self.assertEqual(len(self.captured), 1)
        self.assertEqual(self.captured[0]["region"], "us-west-2")  # us1 region
        self.assertIn("a.schedulable = true", self.captured[0]["sql"])

    def test_prod_mirror_dispatches_to_prod_target(self) -> None:
        self._apply([{
            "step": 1, "kind": "prod_concurrency_mirror",
            "target": {"env": "prod"},
            "variables": {"stub_updates": [{"id": 42, "name": "cc-us1",
                                            "concurrency": 18, "matched_edge": "us1"}]},
            "expected_after": {"stub_concurrency": {"42": 18},
                               "operator_user_concurrency": 318},
        }])
        self.assertEqual(len(self.captured), 1)
        self.assertEqual(self.captured[0]["region"], mgr.PROD_TARGET["region"])
        self.assertIn("WHERE id = 42", self.captured[0]["sql"])
        self.assertIn("concurrency = 18", self.captured[0]["sql"])


if __name__ == "__main__":
    unittest.main()
