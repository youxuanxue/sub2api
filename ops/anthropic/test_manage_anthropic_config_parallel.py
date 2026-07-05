#!/usr/bin/env python3
"""Unit tests for remediate P0 parallel + batch guard paths (stdlib-only)."""
from __future__ import annotations

import importlib.util
import json
import os
import pathlib
import re
import shutil
import subprocess
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


# --------------------------------------------------------------------------
# Generated-SQL structural validity — deterministic, DB-less guard.
#
# Substring assertions (assertIn 'jsonb_agg') cannot tell valid SQL from a
# Postgres syntax error. These three checks map 1:1 to the syntax bugs that
# shipped past the mocked tests and only surfaced against a real Postgres:
#   * unbalanced parens          -> an unclosed COALESCE(... aggregate
#   * a ';' inside the statement -> a reused SELECT … ; embedded in a subquery
#   * a (subquery) value missing -> _sql_*_body stripping the leading SELECT,
#     a leading SELECT/WITH         yielding `(COALESCE(...) FROM ...)`
# All generated capture/guard SQL must pass them.
# --------------------------------------------------------------------------
def _assert_sql_structural(test: unittest.TestCase, sql: str) -> None:
    test.assertEqual(
        sql.count("("), sql.count(")"),
        f"unbalanced parentheses in generated SQL:\n{sql}",
    )
    body = sql.rstrip()
    if body.endswith(";"):
        body = body[:-1]
    test.assertNotIn(
        ";", body,
        f"interior ';' (statement terminator inside a subquery) in:\n{sql}",
    )
    # Every `'key', (` scalar-subquery value must open with SELECT or WITH.
    for m in re.finditer(r"'\w+',\s*\(\s*([A-Za-z]+)", sql):
        test.assertIn(
            m.group(1).upper(), {"SELECT", "WITH"},
            f"subquery value does not begin with SELECT/WITH (token "
            f"{m.group(1)!r}) in:\n{sql}",
        )


def _generated_sqls() -> dict[str, str]:
    return {
        "EDGE_CAPTURE_BUNDLE_SQL": mgr.EDGE_CAPTURE_BUNDLE_SQL,
        "PROD_CAPTURE_BUNDLE_SQL": mgr.PROD_CAPTURE_BUNDLE_SQL,
        "guard_batch": guard.build_all_oauth_guard_live_batch_query(),
    }


class SqlBundleTest(unittest.TestCase):
    def test_sql_as_subquery_keeps_select_strips_terminator(self) -> None:
        # Must keep SELECT (so `(...)` is a valid subquery) and drop the
        # trailing ';' (a ';' inside `(...)` is a syntax error).
        self.assertEqual(mgr._sql_as_subquery("SELECT 1 AS x;"), "SELECT 1 AS x")
        self.assertEqual(
            mgr._sql_as_subquery("\nSELECT a FROM t WHERE b;\n"),
            "SELECT a FROM t WHERE b",
        )

    def test_generated_capture_and_guard_sql_are_structurally_valid(self) -> None:
        for name, sql in _generated_sqls().items():
            with self.subTest(sql=name):
                _assert_sql_structural(self, sql)

    def test_edge_capture_bundle_aggregates_snapshot_fragments(self) -> None:
        sql = mgr.EDGE_CAPTURE_BUNDLE_SQL
        self.assertIn("oauth_accounts", sql)
        self.assertIn("tiers", sql)
        self.assertIn("operator_balance", sql)


@unittest.skipUnless(
    os.environ.get("TK_SQL_EXEC_PSQL") and shutil.which("psql"),
    "set TK_SQL_EXEC_PSQL=<conninfo> with a reachable empty Postgres to "
    "execute generated SQL against a real parser",
)
class GeneratedSqlExecutesOnPostgresTest(unittest.TestCase):
    """Authoritative artifact check: parse+execute every generated query on a
    real Postgres (the structural test above is the always-on DB-less floor).

    The DSN points at any empty Postgres; this test creates the minimal schema
    the queries touch, runs them under ON_ERROR_STOP, and fails on any syntax
    or column-resolution error."""

    _SCHEMA = """
CREATE TABLE accounts(id bigint, name text, platform text, type text, status text,
  schedulable boolean, concurrency int, load_factor numeric, priority int, channel_type int,
  rate_multiplier numeric, auto_pause_on_expired boolean, proxy_id bigint, error_message text,
  last_used_at timestamptz, credentials jsonb DEFAULT '{}', extra jsonb DEFAULT '{}',
  deleted_at timestamptz);
CREATE TABLE groups(id bigint, name text, platform text, status text, claude_code_only boolean,
  fallback_group_id bigint, deleted_at timestamptz);
CREATE TABLE account_groups(account_id bigint, group_id bigint);
CREATE TABLE users(id bigint, balance numeric, concurrency int, deleted_at timestamptz);
CREATE TABLE tiers(name text, concurrency int, priority int, rate_multiplier numeric, base_rpm int,
  rpm_sticky_buffer int, max_sessions int, session_idle_timeout_minutes int,
  cache_ttl_override_enabled boolean, cache_ttl_override_target text,
  tls_profile_name text);
CREATE TABLE tls_fingerprint_profiles(id bigint, name text, description text, enable_grease boolean,
  cipher_suites jsonb, curves jsonb, point_formats jsonb, signature_algorithms jsonb,
  alpn_protocols jsonb, supported_versions jsonb, key_share_groups jsonb, psk_modes jsonb,
  extensions jsonb);
"""

    def _psql(self, sql: str) -> None:
        conninfo = os.environ["TK_SQL_EXEC_PSQL"]
        proc = subprocess.run(
            ["psql", conninfo, "-v", "ON_ERROR_STOP=1", "-q", "-t", "-A", "-f", "-"],
            input=sql, text=True, capture_output=True,
        )
        if proc.returncode != 0:
            self.fail(f"psql rejected generated SQL:\n{proc.stderr}\n---\n{sql}")

    def test_all_generated_sql_executes(self) -> None:
        self._psql(self._SCHEMA)
        for name, sql in _generated_sqls().items():
            with self.subTest(sql=name):
                self._psql(sql if sql.rstrip().endswith(";") else sql + ";")


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
