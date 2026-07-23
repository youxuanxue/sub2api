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
        self.assertIn("ops_repair_contract.py run-command *", self.text)
        self.assertNotIn('allowed_tools: "Bash Read', self.text)
        self.assertNotIn("bash -lc", self.text)
        self.assertIn('BRANCH="fix/ops-repair-', self.text)
        self.assertIn("./scripts/preflight.sh", self.text)
        self.assertIn("gh pr create --draft", self.text)
        self.assertNotIn("gh pr merge", self.text)

    def test_agent_receives_only_allowlisted_candidate_brief(self) -> None:
        prompt_start = self.text.index("- name: Build repair prompt")
        agent_start = self.text.index("- name: Run repository-only repair agent", prompt_start)
        prompt = self.text[prompt_start:agent_start]
        self.assertIn("Allowlisted candidate brief", prompt)
        self.assertIn("cat /tmp/ops-repair/candidate.json", prompt)
        self.assertIn("prompt-candidate", self.text)
        candidate_step_start = self.text.index("- name: Select candidate and enforce cooldown")
        candidate_step_end = self.text.index("- uses: ./.github/actions/cache-and-checkout-new-api", candidate_step_start)
        candidate_step = self.text[candidate_step_start:candidate_step_end]
        self.assertIn("candidate-source.json", candidate_step)
        self.assertIn("rm -f /tmp/ops-repair/candidate-source.json", candidate_step)

    def test_final_diff_is_revalidated_after_reproduction(self) -> None:
        guard_start = self.text.index("- name: Guard repair contract")
        guard_end = self.text.index("- name: Commit Draft repair locally", guard_start)
        guard = self.text[guard_start:guard_end]
        reproduction = guard.index("ops_repair_contract.py run-reproduction")
        first_validate = guard.index("ops_repair_contract.py validate")
        second_validate = guard.index("ops_repair_contract.py validate", first_validate + 1)
        final_snapshot = guard.index("git diff --name-only", reproduction)
        self.assertLess(first_validate, reproduction)
        self.assertLess(reproduction, final_snapshot)
        self.assertLess(final_snapshot, second_validate)
        self.assertIn("repair diff grew after reproduction", guard)
        self.assertIn("git diff --check", guard)

    def test_non_reproducible_candidate_creates_no_branch(self) -> None:
        self.assertIn('"status":"no_change"', self.text)
        self.assertIn("candidate was not reproducible; no Draft PR created", self.text)
        self.assertIn("steps.outcome.outputs.skip == 'false'", self.text)


if __name__ == "__main__":
    unittest.main()
