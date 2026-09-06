#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path


HERE = Path(__file__).resolve().parent
MODULE_PATH = HERE / "account_group_binding_check.py"
SPEC = importlib.util.spec_from_file_location("account_group_binding_check", MODULE_PATH)
assert SPEC and SPEC.loader
MOD = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = MOD
SPEC.loader.exec_module(MOD)


class AccountGroupBindingCheckTest(unittest.TestCase):
    def group(self, group_id: int, name: str, status: str = "active") -> dict:
        return {"id": group_id, "name": name, "status": status}

    def account(
        self,
        account_id: int,
        models: list[str],
        groups: list[dict],
        *,
        platform: str = "newapi",
    ) -> dict:
        return {
            "id": account_id,
            "name": f"account-{account_id}",
            "platform": platform,
            "model_ids": models,
            "groups": groups,
        }

    def evaluate(self, accounts: list[dict], groups: list[dict] | None = None) -> dict:
        return MOD.evaluate_snapshot(
            {"accounts": accounts, "groups": groups or [self.group(1, "claude"), self.group(2, "other")]}
        )

    def test_shared_model_and_group_is_aligned(self) -> None:
        report = self.evaluate(
            [
                self.account(1, ["claude-opus"], [self.group(1, "claude")]),
                self.account(2, ["claude-opus"], [self.group(1, "claude")]),
            ]
        )

        self.assertEqual(report["verdict"], "aligned")
        self.assertEqual(report["findings"], [])
        self.assertEqual(report["summary"]["explicit_mapping_account_count"], 2)

    def test_ungrouped_account_reports_ranked_peer_candidates(self) -> None:
        report = self.evaluate(
            [
                self.account(10, ["claude-opus", "claude-sonnet"], []),
                self.account(11, ["claude-opus", "claude-sonnet"], [self.group(1, "claude")]),
                self.account(12, ["claude-opus"], [self.group(2, "other")]),
            ]
        )

        self.assertEqual(report["verdict"], "review")
        finding = next(item for item in report["findings"] if item["account_id"] == 10)
        self.assertEqual(finding["code"], "no_active_group")
        self.assertEqual(finding["candidate_groups"][0]["group_id"], 1)
        self.assertEqual(
            finding["candidate_groups"][0]["matching_models"],
            ["claude-opus", "claude-sonnet"],
        )

    def test_disjoint_binding_reports_ranked_peer_evidence(self) -> None:
        report = self.evaluate(
            [
                self.account(20, ["claude-opus"], [self.group(2, "other")]),
                self.account(21, ["claude-opus"], [self.group(1, "claude")]),
            ]
        )

        finding = next(item for item in report["findings"] if item["account_id"] == 20)
        self.assertEqual(finding["code"], "model_group_peer_mismatch")
        self.assertEqual(finding["candidate_groups"][0]["group_id"], 1)

    def test_one_peer_group_overlap_allows_intentional_model_subset(self) -> None:
        report = self.evaluate(
            [
                self.account(25, ["claude-opus", "gpt-model"], [self.group(1, "claude")]),
                self.account(26, ["claude-opus"], [self.group(1, "claude")]),
                self.account(27, ["gpt-model"], [self.group(2, "other")]),
                self.account(28, ["gpt-model"], [self.group(2, "other")]),
            ]
        )

        self.assertEqual(report["verdict"], "aligned")
        self.assertEqual(report["findings"], [])

    def test_account_never_self_certifies_its_group(self) -> None:
        report = self.evaluate(
            [self.account(30, ["brand-new-model"], [self.group(2, "other")])]
        )

        self.assertEqual(report["verdict"], "aligned")
        self.assertEqual(report["summary"]["inconclusive_model_count"], 1)
        self.assertEqual(report["findings"], [])

    def test_empty_mapping_is_accounted_for_but_not_compared(self) -> None:
        report = self.evaluate(
            [
                self.account(40, [], []),
                self.account(41, ["mapped"], [self.group(1, "claude")]),
            ]
        )

        self.assertEqual(report["coverage"]["evaluated_account_ids"], [41])
        self.assertEqual(report["coverage"]["skipped_no_explicit_mapping_account_ids"], [40])
        self.assertEqual(report["summary"]["skipped_no_explicit_mapping_count"], 1)

    def test_inactive_binding_counts_as_no_active_group(self) -> None:
        inactive = self.group(9, "retired", "inactive")
        report = self.evaluate(
            [self.account(50, ["mapped"], [inactive])],
            groups=[self.group(1, "claude"), inactive],
        )

        self.assertEqual(report["verdict"], "review")
        self.assertEqual(report["findings"][0]["code"], "no_active_group")
        self.assertEqual(report["findings"][0]["inactive_groups"][0]["id"], 9)


if __name__ == "__main__":
    unittest.main()
