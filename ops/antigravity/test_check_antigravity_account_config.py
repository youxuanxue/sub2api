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
    def _live_mm(self):
        return {
            "gemini-3-flash": "gemini-3-flash",
            "claude-sonnet-4-6": "claude-sonnet-4-6",
            "claude-opus-4-6": "claude-opus-4-6-thinking",
            "claude-opus-4-6-thinking": "claude-opus-4-6-thinking",
        }

    def test_live_antigravity_account_clean(self):
        self.assertIsNone(CHK._account_violation({"model_mapping": self._live_mm()}))

    def test_empty_mapping_is_violation(self):
        self.assertIsNotNone(CHK._account_violation({"model_mapping": None}))
        self.assertIsNotNone(CHK._account_violation({"model_mapping": {}}))
        self.assertIsNotNone(CHK._account_violation({}))

    def test_non_live_claude_or_gptoss_key_is_violation(self):
        mm = self._live_mm()
        mm["claude-opus-4-8"] = "x"
        r = CHK._account_violation({"model_mapping": mm})
        self.assertIsNotNone(r)
        self.assertIn("claude-opus-4-8", r)
        mm = self._live_mm()
        mm["gpt-oss-120b-medium"] = "x"
        self.assertIsNotNone(CHK._account_violation({"model_mapping": mm}))

    def test_missing_live_claude_key_is_violation(self):
        mm = self._live_mm()
        del mm["claude-opus-4-6"]
        r = CHK._account_violation({"model_mapping": mm})
        self.assertIsNotNone(r)
        self.assertIn("missing live Claude", r)

    def test_bad_live_claude_remap_is_violation(self):
        mm = self._live_mm()
        mm["claude-opus-4-6"] = "claude-opus-4-6"
        r = CHK._account_violation({"model_mapping": mm})
        self.assertIsNotNone(r)
        self.assertIn("bad live Claude", r)

    def test_structural_dead_alias_is_violation(self):
        for key in CHK.ANTIGRAVITY_STRUCTURAL_DEAD_MODEL_MAPPING_KEYS:
            with self.subTest(key=key):
                mm = self._live_mm()
                mm[key] = "x"
                r = CHK._account_violation({"model_mapping": mm})
                self.assertIsNotNone(r)
                self.assertIn(key, r)

    def test_unpriced_model_key_is_violation(self):
        r = CHK._account_violation({
            "model_mapping": {**self._live_mm(), "tab_flash_lite_preview": "tab_flash_lite_preview"},
        })
        self.assertIsNotNone(r)
        self.assertIn("unpriced", r)
        self.assertIn("tab_flash_lite_preview", r)

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
        r = CHK._account_violation({
            "model_mapping": {**self._live_mm(), "claude-opus-4-8": "x"},
            "status": "active", "schedulable": True, "bound": False,
        })
        self.assertIsNotNone(r)
        self.assertIn("claude-opus-4-8", r)
        self.assertIn("not bound", r.lower())


class GroupViolationTest(unittest.TestCase):
    def test_canonical_scopes_clean(self):
        self.assertIsNone(CHK._group_violation({"scopes": ["claude", "gemini_text", "gemini_image"]}))

    def test_order_independent(self):
        self.assertIsNone(CHK._group_violation({"scopes": ["gemini_image", "claude", "gemini_text"]}))

    def test_empty_or_missing_is_violation(self):
        self.assertIsNotNone(CHK._group_violation({"scopes": None}))
        self.assertIsNotNone(CHK._group_violation({"scopes": []}))
        self.assertIsNotNone(CHK._group_violation({}))

    def test_missing_claude_is_violation(self):
        r = CHK._group_violation({"scopes": ["gemini_text", "gemini_image"]})
        self.assertIsNotNone(r)
        self.assertIn("missing: claude", r)

    def test_missing_image_is_violation(self):
        r = CHK._group_violation({"scopes": ["gemini_text"]})
        self.assertIsNotNone(r)
        self.assertIn("missing: claude", r)
        self.assertIn("gemini_image", r)

    def test_canonical_set_matches_constant(self):
        # The check's set must equal the canonical scopes (mirrors the Go reconciler).
        self.assertEqual(CHK.ANTIGRAVITY_CANONICAL_SCOPES, {"claude", "gemini_text", "gemini_image"})


class SelfCheckSqlEnumerationTest(unittest.TestCase):
    def test_both_sql_enumerated(self):
        labels = [label for label, _ in CHK.iter_self_check_sql()]
        self.assertEqual(labels, ["ANTIGRAVITY_ACCOUNTS_SQL", "ANTIGRAVITY_GROUPS_SQL"])


if __name__ == "__main__":
    unittest.main()
