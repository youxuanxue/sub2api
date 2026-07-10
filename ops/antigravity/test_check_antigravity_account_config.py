#!/usr/bin/env python3
"""Offline unit tests for the antigravity post-rollout config check's violation
classification logic — the load-bearing predicates that decide whether an account
/ group is flagged. The real-Postgres SQL self-check (ops/anthropic/
test_ops_sql_execute.py) proves the SQL *executes*; this proves the verdicts are
correct, so a careless edit to _account_violation / _group_violation is caught.

stdlib-only. The module filename is hyphenated, so it is loaded via importlib.
"""
from __future__ import annotations

import importlib.util
import json
import pathlib
import subprocess
import unittest
from unittest import mock

_HERE = pathlib.Path(__file__).resolve().parent
_MOD_PATH = _HERE / "check-antigravity-account-config.py"


def _load_module():
    spec = importlib.util.spec_from_file_location("check_antigravity_account_config", _MOD_PATH)
    assert spec is not None and spec.loader is not None
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)  # type: ignore[union-attr]
    return mod


CHK = _load_module()


class GoPolicyProjectionTest(unittest.TestCase):
    def test_checker_policy_matches_complete_go_owner_projection(self):
        proc = subprocess.run(
            CHK.GO_HELPER,
            cwd=CHK.REPO_ROOT / "backend",
            input="",
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr or proc.stdout)
        doc = json.loads(proc.stdout)
        mapping = doc["platforms"]["antigravity"]
        policy = CHK._antigravity_policy()

        self.assertEqual(policy["mapping"], mapping)
        self.assertEqual(policy["scopes"], set(doc["antigravity_group_scopes"]))
        self.assertEqual(
            policy["forbidden_keys"],
            set(doc.get("forbidden_model_mapping_keys", {}).get("antigravity", [])),
        )
        self.assertEqual(
            policy["forbidden_prefixes"],
            tuple(doc.get("forbidden_model_mapping_prefixes", {}).get("antigravity", [])),
        )


class AccountViolationTest(unittest.TestCase):
    def _policy(self):
        return CHK._antigravity_policy()

    def _live_mm(self):
        mm = dict(self._policy()["mapping"])
        # Fixed boundary: an unknown non-Claude, non-forbidden extra proves the
        # checker does not fork the Go floor into an exact local list.
        mm["test-extra-nonclaude-boundary"] = "test-extra-nonclaude-boundary"
        return mm

    def test_live_antigravity_account_clean(self):
        self.assertIsNone(CHK._account_violation({"model_mapping": self._live_mm()}))

    def test_empty_mapping_is_violation(self):
        self.assertIsNotNone(CHK._account_violation({"model_mapping": None}))
        self.assertIsNotNone(CHK._account_violation({"model_mapping": {}}))
        self.assertIsNotNone(CHK._account_violation({}))

    def test_edge_passthrough_empty_mapping_is_not_drift(self):
        self.assertIsNone(CHK._account_violation({"model_mapping": None}, allow_empty_mapping=True))
        self.assertIsNotNone(CHK._account_violation({
            "model_mapping": None,
            "status": "active", "schedulable": True, "bound": False,
        }, allow_empty_mapping=True))
        self.assertIsNotNone(CHK._account_violation({
            "model_mapping": {"test-edge-nonempty-boundary": "test-edge-nonempty-boundary"},
        }, allow_empty_mapping=True))
        self.assertIsNotNone(CHK._account_violation({"model_mapping": "malformed"}, allow_empty_mapping=True))

    def test_each_go_forbidden_prefix_is_violation(self):
        for prefix in self._policy()["forbidden_prefixes"]:
            with self.subTest(prefix=prefix):
                mm = self._live_mm()
                mm[prefix + "boundary"] = "x"
                self.assertIsNotNone(CHK._account_violation({"model_mapping": mm}))

    def test_each_missing_go_floor_key_is_violation(self):
        for missing in sorted(self._policy()["mapping"]):
            with self.subTest(missing=missing):
                mm = self._live_mm()
                del mm[missing]
                r = CHK._account_violation({"model_mapping": mm})
                self.assertIsNotNone(r)
                self.assertIn("missing Go SSOT floor", r)
                self.assertIn(missing, r)

    def test_each_bad_go_floor_target_is_violation(self):
        for model_id in sorted(self._policy()["mapping"]):
            with self.subTest(model_id=model_id):
                mm = self._live_mm()
                mm[model_id] = "test-wrong-target-boundary"
                r = CHK._account_violation({"model_mapping": mm})
                self.assertIsNotNone(r)
                self.assertIn("bad Go SSOT floor", r)
                self.assertIn(model_id, r)

    def test_each_go_forbidden_key_is_violation(self):
        for key in self._policy()["forbidden_keys"]:
            with self.subTest(key=key):
                mm = self._live_mm()
                mm[key] = "x"
                r = CHK._account_violation({"model_mapping": mm})
                self.assertIsNotNone(r)
                self.assertIn(key, r)

    def test_active_schedulable_unbound_is_violation(self):
        # the us4 gap: healthy account but no account_groups binding.
        r = CHK._account_violation({
            "model_mapping": self._live_mm(),
            "status": "active", "schedulable": True, "bound": False,
        })
        self.assertIsNotNone(r)
        self.assertIn("not bound", r.lower())

    def test_active_schedulable_bound_is_clean(self):
        self.assertIsNone(CHK._account_violation({
            "model_mapping": self._live_mm(),
            "status": "active", "schedulable": True, "bound": True,
        }))

    def test_parked_account_unbound_is_not_flagged(self):
        # schedulable=false (intentionally parked) → binding not required, no false flag.
        self.assertIsNone(CHK._account_violation({
            "model_mapping": self._live_mm(),
            "status": "active", "schedulable": False, "bound": False,
        }))

    def test_inactive_unbound_is_not_flagged(self):
        self.assertIsNone(CHK._account_violation({
            "model_mapping": self._live_mm(),
            "status": "inactive", "schedulable": True, "bound": False,
        }))

    def test_bad_mapping_and_unbound_reports_both(self):
        model_id = sorted(self._policy()["mapping"])[0]
        mm = self._live_mm()
        mm[model_id] = "test-wrong-target-boundary"
        r = CHK._account_violation({
            "model_mapping": mm,
            "status": "active", "schedulable": True, "bound": False,
        })
        self.assertIsNotNone(r)
        self.assertIn(model_id, r)
        self.assertIn("not bound", r.lower())


class GroupViolationTest(unittest.TestCase):
    def _scopes(self):
        return set(CHK._antigravity_policy()["scopes"])

    def test_canonical_scopes_clean(self):
        self.assertIsNone(CHK._group_violation({"scopes": sorted(self._scopes())}))

    def test_order_independent(self):
        self.assertIsNone(CHK._group_violation({"scopes": list(reversed(sorted(self._scopes())))}))

    def test_empty_or_missing_is_violation(self):
        self.assertIsNotNone(CHK._group_violation({"scopes": None}))
        self.assertIsNotNone(CHK._group_violation({"scopes": []}))
        self.assertIsNotNone(CHK._group_violation({}))

    def test_each_missing_owner_scope_is_violation(self):
        canonical = self._scopes()
        self.assertTrue(canonical)
        for missing in sorted(canonical):
            with self.subTest(missing=missing):
                r = CHK._group_violation({"scopes": sorted(canonical - {missing})})
                self.assertIsNotNone(r)
                self.assertIn("missing: " + missing, r)

    def test_canonical_set_drives_group_verdict(self):
        scopes = sorted(self._scopes())
        self.assertIsNone(CHK._group_violation({"scopes": scopes}))
        r = CHK._group_violation({"scopes": scopes + ["unexpected_scope"]})
        self.assertIsNotNone(r)
        self.assertIn("unexpected: unexpected_scope", r)


class TargetScopeTest(unittest.TestCase):
    def test_edge_target_allows_only_empty_mapping_while_prod_enforces_floor(self):
        account_mapping = None

        def fake_sql(_region, _instance_id, _sql, comment):
            if "account-check" in comment:
                return json.dumps([{
                    "id": 1,
                    "name": "boundary",
                    "model_mapping": account_mapping,
                    "status": "inactive",
                    "schedulable": False,
                    "bound": False,
                }])
            return "[]"

        with mock.patch.object(CHK, "ssm_run_sql", side_effect=fake_sql):
            self.assertEqual(CHK._check_target("edge:test", "region", "instance"), [])
            account_mapping = {"test-edge-nonempty-boundary": "test-edge-nonempty-boundary"}
            edge_violations = CHK._check_target("edge:test", "region", "instance")
            account_mapping = None
            prod_violations = CHK._check_target(CHK.PROD_TARGET["label"], "region", "instance")
        self.assertEqual(len(edge_violations), 1)
        self.assertIn("edge model_mapping must remain empty passthrough", edge_violations[0]["reason"])
        self.assertEqual(len(prod_violations), 1)
        self.assertIn("empty/missing prod model_mapping", prod_violations[0]["reason"])


class SelfCheckSqlEnumerationTest(unittest.TestCase):
    def test_both_sql_enumerated(self):
        labels = [label for label, _ in CHK.iter_self_check_sql()]
        self.assertEqual(labels, ["ANTIGRAVITY_ACCOUNTS_SQL", "ANTIGRAVITY_GROUPS_SQL"])


if __name__ == "__main__":
    unittest.main()
