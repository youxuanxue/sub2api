#!/usr/bin/env python3
"""Security and behavior contract tests for deploy-stage0 workflow modes."""
from __future__ import annotations

import pathlib
import re
import unittest
import yaml


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOW = REPO_ROOT / ".github" / "workflows" / "deploy-qa-bundle.yml"
EDGE_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "deploy-edge-lightsail-stage0.yml"
BLUEGREEN_DEPLOY = REPO_ROOT / "ops" / "stage0" / "deploy_via_ssm_bluegreen.sh"


def workflow_text() -> str:
    return WORKFLOW.read_text(encoding="utf-8")


def job_block(name: str) -> str:
    text = workflow_text()
    match = re.search(
        rf"(?ms)^  {re.escape(name)}:\n(?P<body>.*?)(?=^  [A-Za-z0-9_-]+:\n|\Z)",
        text,
    )
    if match is None:
        raise AssertionError(f"job not found: {name}")
    return match.group(0)


def step_run(name: str) -> str:
    lines = workflow_text().splitlines()
    marker = f"      - name: {name}"
    start = lines.index(marker)
    end = next(
        (index for index in range(start + 1, len(lines)) if lines[index].startswith("      - ")),
        len(lines),
    )
    run_start = lines.index("        run: |", start, end) + 1
    return "\n".join(
        line[10:] if line.startswith("          ") else ""
        for line in lines[run_start:end]
    )


class DeployQABundleWorkflowTest(unittest.TestCase):
    def test_workflow_inputs_and_concurrency(self) -> None:
        data = yaml.safe_load(workflow_text())
        inputs = data.get("on") or data[True]["workflow_dispatch"]["inputs"]
        self.assertIn("operation", inputs)
        self.assertEqual(inputs["operation"]["type"], "choice")
        self.assertEqual(inputs["operation"]["default"], "deploy")
        self.assertEqual(inputs["operation"]["options"], ["deploy", "canary-only", "qa-infra-check"])
        self.assertIn("tag", inputs)
        self.assertEqual(data["concurrency"]["group"], "deploy-qa-bundle-prod")
        self.assertFalse(data["concurrency"]["cancel-in-progress"])

    def test_qa_infra_check_is_read_only_and_verifies_oidc_binding(self) -> None:
        job = job_block("qa-infra-check")
        self.assertIn("if: inputs.operation == 'qa-infra-check'", job)
        self.assertIn("environment: prod", job)
        self.assertIn("contents: read", job)
        self.assertIn("id-token: write", job)
        self.assertIn("QAInfraDeploymentRoleArn", job)
        self.assertIn("QAInfraCloudFormationServiceRoleArn", job)
        self.assertIn("QA_INFRA_OIDC_ROLE_ARN", job)
        self.assertIn("aws sts get-caller-identity", job)
        self.assertIn(".Stacks[0].RoleARN", job)
        self.assertIn("QaRawArchiveBucketName", job)
        self.assertIn("QaRawArchiveRecoveryRoleArn", job)
        self.assertIn("recognized raw-archive contract", job)
        self.assertIn("legacy_bootstrap_ready", job)
        self.assertIn("Bundle-era QA stack is not bound", job)
        for forbidden in (
            "create-change-set", "execute-change-set", "aws ssm",
            "deploy_via_ssm", "sync-qa-", "run-qa-bundle-canary",
        ):
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, job.lower())

    def test_deploy_qa_job_provisions_infra_and_runs_canary(self) -> None:
        job = job_block("deploy-qa")
        self.assertIn("if: inputs.operation == 'deploy' || inputs.operation == 'canary-only'", job)
        self.assertIn("environment: prod", job)
        self.assertIn("id-token: write", job)
        self.assertIn("packages: read", job)
        self.assertIn("ops/qa/deploy_qa_raw_archive_cfn.sh", job)
        self.assertIn("ops/qa/verify_qa_bundle_infra.sh", job)
        self.assertIn("ops/stage0/run-qa-bundle-canary-via-ssm.sh", job)


if __name__ == "__main__":
    unittest.main()
