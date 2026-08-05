#!/usr/bin/env python3
"""Security and behavior contract tests for deploy-stage0 workflow modes."""
from __future__ import annotations

import pathlib
import re
import unittest


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOW = REPO_ROOT / ".github" / "workflows" / "deploy-stage0.yml"


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


class DeployStage0WorkflowTest(unittest.TestCase):
    def test_operation_choice_preserves_deploy_default(self) -> None:
        text = workflow_text()
        operation = re.search(
            r"(?ms)^      operation:\n(?P<body>.*?)(?=^      [A-Za-z0-9_-]+:\n)",
            text,
        )
        self.assertIsNotNone(operation)
        body = operation.group("body")
        self.assertIn("type: choice", body)
        self.assertIn("required: true", body)
        self.assertIn("default: deploy", body)
        self.assertRegex(body, r"(?ms)options:\s*\n\s*- deploy\s*\n\s*- smoke-only\s*$")

    def test_deploy_job_retains_mutating_capabilities_and_canonical_gates(self) -> None:
        deploy = job_block("deploy")
        self.assertIn("if: inputs.operation == 'deploy'", deploy)
        self.assertIn("environment: prod", deploy)
        self.assertIn("id-token: write", deploy)
        self.assertIn("packages: read", deploy)
        self.assertIn("deploy_via_ssm_bluegreen.sh", deploy)
        self.assertIn("bash ops/stage0/post_deploy_smoke.sh", deploy)
        self.assertIn(
            "bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --gate --deploy-canary --deploy-closeout",
            deploy,
        )

    def test_smoke_only_job_is_read_only_and_uses_prod_environment(self) -> None:
        smoke = job_block("smoke-only")
        self.assertIn("if: inputs.operation == 'smoke-only'", smoke)
        self.assertIn("environment: prod", smoke)
        self.assertRegex(smoke, r"(?ms)^    permissions:\n      contents: read\s*$")
        self.assertNotIn("id-token:", smoke)
        self.assertNotIn("packages:", smoke)

    def test_smoke_only_runs_canonical_full_smoke_and_ssot_gate(self) -> None:
        smoke = job_block("smoke-only")
        self.assertIn("GATEWAY_SMOKE_SUITE: full", smoke)
        self.assertIn("id: gateway_smoke", smoke)
        self.assertIn("bash ops/stage0/post_deploy_smoke.sh", smoke)
        self.assertIn("id: ssot_gate", smoke)
        self.assertIn(
            "bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --gate --deploy-canary --deploy-closeout",
            smoke,
        )
        self.assertIn("TK_SMOKE_API_KEY: ${{ secrets.TK_SMOKE_API_KEY }}", smoke)
        self.assertIn("TK_FULLTEST_KEY: ${{ secrets.TK_FULLTEST_KEY }}", smoke)

    def test_smoke_only_uses_canonical_prod_url_without_mutation_commands(self) -> None:
        smoke = job_block("smoke-only")
        self.assertIn("${{ vars.PROD_API_URL || 'https://api.tokenkey.dev' }}", smoke)
        self.assertIn("TK_FULLTEST_BASE_URL: ${{ vars.PROD_API_URL || 'https://api.tokenkey.dev' }}", smoke)
        forbidden = (
            "aws-actions/configure-aws-credentials",
            "aws ssm",
            "deploy_via_ssm",
            "docker ",
            "docker-compose",
            "caddy reload",
            "active-color",
            "sync-runtime",
            "apply-accounts",
            "notify-feishu-release",
        )
        for marker in forbidden:
            with self.subTest(marker=marker):
                self.assertNotIn(marker, smoke.lower())

    def test_smoke_only_summary_never_claims_deployment(self) -> None:
        smoke = job_block("smoke-only")
        summary_start = smoke.index("- name: Job summary")
        summary = smoke[summary_start:]
        self.assertIn("operation: `smoke-only`", summary)
        self.assertIn("gateway smoke: ${GATEWAY_SMOKE_OUTCOME:-not-run}", summary)
        self.assertIn("SSOT display gate: ${SSOT_GATE_OUTCOME:-not-run}", summary)
        self.assertIn("no image or host state was changed", summary)
        self.assertNotRegex(summary.lower(), r"\bdeployed\b|rollback:")


if __name__ == "__main__":
    unittest.main()
