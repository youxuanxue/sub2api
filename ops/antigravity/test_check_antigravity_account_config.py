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
import pathlib
import unittest

_HERE = pathlib.Path(__file__).resolve().parent
_MOD_PATH = _HERE / "check-antigravity-account-config.py"


def _load_module():
    spec = importlib.util.spec_from_file_location("check_antigravity_account_config", _MOD_PATH)
    assert spec is not None and spec.loader is not None
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)  # type: ignore[union-attr]
    return mod


CHK = _load_module()


class AccountViolationTest(unittest.TestCase):
    def test_gemini_only_account_clean(self):
        self.assertIsNone(CHK._account_violation({"model_mapping": {"gemini-3-flash": "gemini-3-flash"}}))

    def test_empty_mapping_is_violation(self):
        self.assertIsNotNone(CHK._account_violation({"model_mapping": None}))
        self.assertIsNotNone(CHK._account_violation({"model_mapping": {}}))
        self.assertIsNotNone(CHK._account_violation({}))

    def test_claude_or_gptoss_key_is_violation(self):
        r = CHK._account_violation({"model_mapping": {"claude-opus-4-8": "x", "gemini-3-flash": "y"}})
        self.assertIsNotNone(r)
        self.assertIn("claude-opus-4-8", r)
        self.assertIsNotNone(CHK._account_violation({"model_mapping": {"gpt-oss-120b-medium": "x"}}))

    def _gemini_only_mm(self):
        return {"gemini-3-flash": "gemini-3-flash"}

    def test_active_schedulable_unbound_is_violation(self):
        # the us4 gap: healthy gemini-only account but no account_groups binding.
        r = CHK._account_violation({
            "model_mapping": self._gemini_only_mm(),
            "status": "active", "schedulable": True, "bound": False,
        })
        self.assertIsNotNone(r)
        self.assertIn("not bound", r.lower())

    def test_active_schedulable_bound_is_clean(self):
        self.assertIsNone(CHK._account_violation({
            "model_mapping": self._gemini_only_mm(),
            "status": "active", "schedulable": True, "bound": True,
        }))

    def test_parked_account_unbound_is_not_flagged(self):
        # schedulable=false (intentionally parked) → binding not required, no false flag.
        self.assertIsNone(CHK._account_violation({
            "model_mapping": self._gemini_only_mm(),
            "status": "active", "schedulable": False, "bound": False,
        }))

    def test_inactive_unbound_is_not_flagged(self):
        self.assertIsNone(CHK._account_violation({
            "model_mapping": self._gemini_only_mm(),
            "status": "inactive", "schedulable": True, "bound": False,
        }))

    def test_bad_mapping_and_unbound_reports_both(self):
        r = CHK._account_violation({
            "model_mapping": {"claude-opus-4-8": "x"},
            "status": "active", "schedulable": True, "bound": False,
        })
        self.assertIsNotNone(r)
        self.assertIn("claude-opus-4-8", r)
        self.assertIn("not bound", r.lower())


class GroupViolationTest(unittest.TestCase):
    def test_canonical_gemini_only_clean(self):
        self.assertIsNone(CHK._group_violation({"scopes": ["gemini_text", "gemini_image"]}))

    def test_order_independent(self):
        self.assertIsNone(CHK._group_violation({"scopes": ["gemini_image", "gemini_text"]}))

    def test_empty_or_missing_is_violation(self):
        self.assertIsNotNone(CHK._group_violation({"scopes": None}))
        self.assertIsNotNone(CHK._group_violation({"scopes": []}))
        self.assertIsNotNone(CHK._group_violation({}))

    def test_claude_present_is_violation(self):
        r = CHK._group_violation({"scopes": ["claude", "gemini_text", "gemini_image"]})
        self.assertIsNotNone(r)
        self.assertIn("unexpected: claude", r)

    def test_missing_image_is_violation(self):
        r = CHK._group_violation({"scopes": ["gemini_text"]})
        self.assertIsNotNone(r)
        self.assertIn("missing: gemini_image", r)

    def test_canonical_set_matches_constant(self):
        # The check's gemini-only set must equal the canonical scopes (mirrors the
        # Go domain.GeminiOnlyAntigravityModelScopes + reconciler predicate).
        self.assertEqual(CHK.GEMINI_ONLY_SCOPES, {"gemini_text", "gemini_image"})


class SelfCheckSqlEnumerationTest(unittest.TestCase):
    def test_both_sql_enumerated(self):
        labels = [label for label, _ in CHK.iter_self_check_sql()]
        self.assertEqual(labels, ["ANTIGRAVITY_ACCOUNTS_SQL", "ANTIGRAVITY_GROUPS_SQL"])


if __name__ == "__main__":
    unittest.main()
