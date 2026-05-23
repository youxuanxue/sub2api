#!/usr/bin/env python3
"""Unit tests for the prod-stub-pool stage of the anthropic config orchestrator:

  - plan-stub-pool: regex matching, noop idempotency, force flag
  - render_prod_stub_pool_sql: SQL contains the values from the JSON policy,
    quoting is correct, the WHERE clause anchors on (id, name, platform, type)
  - apply / verify wiring: action.kind = prod_stub_pool dispatches through
    the prod target resolver and reads back from snap.prod.anthropic_stubs

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
_spec = importlib.util.spec_from_file_location("manage_anthropic_config_stub_pool", _MOD_PATH)
mgr = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(mgr)


def _policy() -> dict:
    """Read the canonical stub-pool policy from the baseline JSON so these tests
    stay green if the policy values are retuned later — they assert behavior
    (matching, noop, SQL shape), not magic numbers."""
    return mgr._load_stub_pool_policy()


def _stub(stub_id: int, name: str, base_url: str | None, *,
          pool_mode=None, retry=None) -> dict:
    return {
        "id": stub_id, "name": name, "type": "apikey", "platform": "anthropic",
        "status": "active", "schedulable": True, "concurrency": 16, "priority": 1,
        "cred_base_url": base_url,
        "cred_pool_mode": pool_mode,
        "cred_pool_mode_retry_count": retry,
    }


def _snapshot(stubs: list[dict], *, edges: dict | None = None,
              prod_extra: dict | None = None) -> dict:
    prod_view = {"instance_id": "i-test", "region": "us-east-1",
                 "stack": "tokenkey-prod-stage0", "anthropic_stubs": stubs}
    if prod_extra is not None:
        prod_view.update(prod_extra)
    return {
        "version": mgr.SNAPSHOT_VERSION,
        "captured_at": "2026-05-23T00:00:00Z",
        "edges": edges or {},
        "prod": prod_view,
    }


class PolicyLoadTest(unittest.TestCase):
    def test_policy_has_required_fields_and_compiles_regex(self) -> None:
        p = _policy()
        for k in ("base_url_pattern", "platform", "account_type",
                  "pool_mode_enabled", "pool_mode_retry_count"):
            self.assertIn(k, p, f"policy missing required field {k}")
        self.assertEqual(p["platform"], "anthropic")
        self.assertEqual(p["account_type"], "apikey")
        self.assertIsNotNone(p.get("_compiled_pattern"))


class PlanStubPoolTest(unittest.TestCase):
    """plan-stub-pool enumerates every prod anthropic stub matching the base_url
    policy, emits one action per stub not already at the declared values."""

    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = pathlib.Path(self._tmp.name)
        p = _policy()
        self.want_pool = bool(p["pool_mode_enabled"])
        self.want_retry = int(p["pool_mode_retry_count"])

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _write(self, stubs: list[dict]) -> pathlib.Path:
        path = self.tmp / "snap.json"
        path.write_text(json.dumps(_snapshot(stubs)))
        return path

    def _plan(self, snap_path: pathlib.Path, *, force: bool = False) -> dict:
        out = self.tmp / "plan.json"
        ns = argparse.Namespace(snapshot=str(snap_path), out=str(out),
                                force_template_rewrite=force)
        self.assertEqual(mgr.cmd_plan_stub_pool(ns), 0)
        return json.loads(out.read_text())

    def test_matches_edge_url_pattern_and_actions_unset_stubs(self) -> None:
        path = self._write([
            _stub(42, "cc-us1", "https://api-us1.tokenkey.dev"),
            _stub(40, "cc-uk1", "https://api-uk1.tokenkey.dev"),
        ])
        plan = self._plan(path)
        names = sorted(a["target"]["account_name"] for a in plan["actions"])
        self.assertEqual(names, ["cc-uk1", "cc-us1"])
        self.assertFalse(plan["noop"])
        for a in plan["actions"]:
            self.assertEqual(a["kind"], "prod_stub_pool")
            self.assertEqual(a["target"]["env"], "prod")
            self.assertEqual(a["expected_after"]["cred_pool_mode"], self.want_pool)
            self.assertEqual(a["expected_after"]["cred_pool_mode_retry_count"], self.want_retry)

    def test_non_matching_base_url_is_skipped_not_actioned(self) -> None:
        # Third-party stubs (tokensea, deepseek) must not be touched even though
        # they sit on the same platform=anthropic / type=apikey row shape.
        path = self._write([
            _stub(43, "tokensea-0.4", "https://agent.tokensea.ai"),
            _stub(45, "ds-fallback",  "https://api.deepseek.com/anthropic"),
        ])
        plan = self._plan(path)
        self.assertEqual(plan["actions"], [])
        self.assertTrue(plan["noop"])
        self.assertEqual(plan["summary"]["skipped_unmatched"], 2)
        unmatched_names = sorted(s["name"] for s in plan["live_inputs"]["skipped_unmatched"])
        self.assertEqual(unmatched_names, ["ds-fallback", "tokensea-0.4"])

    def test_already_matching_stub_is_noop_unless_forced(self) -> None:
        # Replays a real cc-us1 after pool_mode has been applied manually
        # (the 2026-05-23 ratchet state) — second-pass plan should be noop.
        path = self._write([
            _stub(42, "cc-us1", "https://api-us1.tokenkey.dev",
                  pool_mode=self.want_pool, retry=self.want_retry),
        ])
        plan = self._plan(path, force=False)
        self.assertEqual(plan["actions"], [])
        self.assertTrue(plan["noop"])
        self.assertEqual(plan["summary"]["skipped_noop"], 1)
        # force flag must re-emit an action even when live already matches
        plan_forced = self._plan(path, force=True)
        self.assertEqual(len(plan_forced["actions"]), 1)
        self.assertFalse(plan_forced["noop"])

    def test_disabled_status_apikey_with_matching_url_still_actions(self) -> None:
        # cc-fra1-oauth on prod is status=disabled but base_url matches; we still
        # action it so a later admin re-enable lands with pool_mode already on.
        # (The accounts.deleted_at IS NULL filter is the only hard exclusion.)
        path = self._write([
            _stub(41, "cc-fra1-oauth", "https://api-fra1.tokenkey.dev/"),
        ])
        plan = self._plan(path)
        self.assertEqual(len(plan["actions"]), 1)
        self.assertEqual(plan["actions"][0]["target"]["account_name"], "cc-fra1-oauth")

    def test_missing_prod_view_fails_loud(self) -> None:
        snap = _snapshot([], prod_extra={"error": "prod resolver failed"})
        path = self.tmp / "snap.json"
        path.write_text(json.dumps(snap))
        with self.assertRaises(SystemExit):
            self._plan(path)


class RenderProdStubPoolSqlTest(unittest.TestCase):
    """SQL is rendered from policy + (account_id, account_name) — no static
    template, no operator-written SQL. Renderer must:
      - embed pool_mode + retry as JSONB merge
      - anchor WHERE on (id, name, platform='anthropic', type='apikey', deleted_at IS NULL)
      - SQL-quote the name (defence in depth)
    """

    def test_sql_contains_policy_values_and_safe_where(self) -> None:
        sql = mgr.render_prod_stub_pool_sql(42, "cc-us1", True, 1)
        self.assertIn("UPDATE accounts SET", sql)
        self.assertIn("credentials = credentials || jsonb_build_object", sql)
        self.assertIn("'pool_mode', true::boolean", sql)
        self.assertIn("'pool_mode_retry_count', 1::int", sql)
        self.assertIn("WHERE id = 42", sql)
        self.assertIn("AND name = 'cc-us1'", sql)
        self.assertIn("AND platform = 'anthropic'", sql)
        self.assertIn("AND type = 'apikey'", sql)
        self.assertIn("AND deleted_at IS NULL", sql)
        # RETURNING gives apply step a deterministic stdout to log.
        self.assertIn("RETURNING id, name", sql)
        self.assertIn("BEGIN;", sql)
        self.assertIn("\nCOMMIT;", sql)

    def test_sql_quotes_apostrophes_in_name(self) -> None:
        sql = mgr.render_prod_stub_pool_sql(99, "weird'name", True, 1)
        # ' → '' (SQL-standard quoting)
        self.assertIn("AND name = 'weird''name'", sql)
        # And the audit comment also carries the quoted form
        self.assertIn("name='weird''name'", sql)

    def test_renderer_rejects_non_int_id(self) -> None:
        with self.assertRaises(SystemExit):
            mgr.render_prod_stub_pool_sql("42", "cc-us1", True, 1)  # type: ignore[arg-type]


class ApplyDispatchTest(unittest.TestCase):
    """cmd_apply branches on action.kind. We do not exercise the SSM path here
    (that requires AWS creds); we only verify the renderer/resolver wiring by
    monkey-patching the SSM helper to capture its inputs."""

    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = pathlib.Path(self._tmp.name)
        self._orig_ssm = mgr.ssm_run_sql_b64
        self._orig_resolve = mgr.resolve_instance_id
        self.captured: list[dict] = []

        def fake_ssm(region, instance_id, sql_b64, comment):
            import base64 as _b
            self.captured.append({
                "region": region, "instance_id": instance_id,
                "sql": _b.b64decode(sql_b64).decode("utf-8"),
                "comment": comment,
            })
            return ("UPDATE 1\n42|cc-us1|true|1", "cid-fake", True, "")

        def fake_resolve(region, stack):
            return f"i-fake-{stack}"

        mgr.ssm_run_sql_b64 = fake_ssm
        mgr.resolve_instance_id = fake_resolve

    def tearDown(self) -> None:
        mgr.ssm_run_sql_b64 = self._orig_ssm
        mgr.resolve_instance_id = self._orig_resolve
        self._tmp.cleanup()

    def test_prod_stub_pool_action_dispatches_to_prod_target(self) -> None:
        p = _policy()
        plan = {
            "version": mgr.PLAN_VERSION,
            "kind": "prod_stub_pool_mode",
            "confirm_code": mgr.CONFIRM_CODE,
            "intent": {},
            "actions": [{
                "step": 1,
                "kind": "prod_stub_pool",
                "target": {"env": "prod", "account_id": 42, "account_name": "cc-us1"},
                "variables": {
                    "account_id": 42,
                    "pool_mode_enabled": bool(p["pool_mode_enabled"]),
                    "pool_mode_retry_count": int(p["pool_mode_retry_count"]),
                },
                "expected_after": mgr._stub_pool_expected_after(p),
            }],
        }
        plan_path = self.tmp / "plan.json"
        plan_path.write_text(json.dumps(plan))
        job_dir = self.tmp / "job"
        ns = argparse.Namespace(
            plan=str(plan_path),
            confirm=mgr.CONFIRM_CODE,
            job_dir=str(job_dir),
            json=False,
        )
        rc = mgr.cmd_apply(ns)
        self.assertEqual(rc, 0)
        self.assertEqual(len(self.captured), 1)
        cap = self.captured[0]
        self.assertEqual(cap["region"], mgr.PROD_TARGET["region"])
        self.assertEqual(cap["instance_id"], f"i-fake-{mgr.PROD_TARGET['stack']}")
        self.assertIn("WHERE id = 42", cap["sql"])
        self.assertIn("AND name = 'cc-us1'", cap["sql"])


if __name__ == "__main__":
    unittest.main()
