#!/usr/bin/env python3
from __future__ import annotations

import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / ".github" / "workflows" / "ops-repair-draft.yml"


class OpsRepairDraftWorkflowTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.text = WORKFLOW.read_text(encoding="utf-8")

    def test_repository_write_workflow_has_no_aws_oidc_or_prod_commands(self) -> None:
        self.assertIn("contents: write", self.text)
        self.assertIn("pull-requests: write", self.text)
        self.assertIn("actions: read", self.text)
        self.assertNotIn("id-token: write", self.text)
        self.assertNotIn("configure-aws-credentials", self.text)
        self.assertNotIn("aws ssm", self.text)
        self.assertNotIn("deploy-stage0", self.text)

    def test_report_must_come_from_successful_main_daily_run(self) -> None:
        self.assertIn('= "Ops Daily Diagnostics"', self.text)
        self.assertIn('= "main"', self.text)
        self.assertIn('= "success"', self.text)
        self.assertIn("older than 48 hours", self.text)
        self.assertIn("daily_error_report.py select", self.text)

    def test_agent_cannot_push_and_workflow_opens_draft_only_after_gates(self) -> None:
        agent_start = self.text.index("- name: Run repository-only repair agent")
        guard_start = self.text.index("- name: Guard repair contract", agent_start)
        commit_start = self.text.index("- name: Commit Draft repair locally", guard_start)
        preflight_start = self.text.index("- name: Run preflight", commit_start)
        pr_start = self.text.index("- name: Push and open Draft PR", preflight_start)
        self.assertLess(agent_start, guard_start)
        self.assertLess(guard_start, commit_start)
        self.assertLess(commit_start, preflight_start)
        self.assertLess(preflight_start, pr_start)
        self.assertIn("persist-credentials: false", self.text)
        self.assertIn("ops_repair_contract.py", self.text)
        self.assertIn("ops_repair_contract.py run-reproduction", self.text)
        self.assertNotIn("bash -lc", self.text)
        self.assertIn('BRANCH="fix/ops-repair-', self.text)
        self.assertIn("./scripts/preflight.sh", self.text)
        self.assertIn("gh pr create --draft", self.text)
        self.assertNotIn("gh pr merge", self.text)

    def test_non_reproducible_candidate_creates_no_branch(self) -> None:
        self.assertIn('"status":"no_change"', self.text)
        self.assertIn("candidate was not reproducible; no Draft PR created", self.text)
        self.assertIn("steps.outcome.outputs.skip == 'false'", self.text)


if __name__ == "__main__":
    unittest.main()
