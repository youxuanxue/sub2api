#!/usr/bin/env python3
"""Security and behavior contract tests for deploy-stage0 workflow modes."""
from __future__ import annotations

import pathlib
import os
import re
import subprocess
import tempfile
import unittest
import yaml


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOW = REPO_ROOT / ".github" / "workflows" / "deploy-stage0.yml"
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


class DeployStage0WorkflowTest(unittest.TestCase):
    def test_failed_legacy_summary_reports_actual_safety_step_outcomes(self) -> None:
        steps = yaml.safe_load(workflow_text())["jobs"]["deploy"]["steps"]
        step = next(step for step in steps if step.get("name") == "Job summary")
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / "summary"
            values = {key: "fixture" for key in step["env"]}
            values.update(RELEASE_MODE="legacy_rollback", LEGACY_WORKER_OUTCOME="failure",
                          LEGACY_PAUSE_OUTCOME="skipped", LEGACY_BOUNDARY_OUTCOME="skipped",
                          GITHUB_STEP_SUMMARY=str(output))
            result = subprocess.run(["bash", "-euc", step["run"]],
                                    env={**os.environ, **values}, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("Worker verification=failure; DROP pause=skipped; Boundary disable=skipped", output.read_text())

    def test_legacy_safety_is_fail_closed_and_excluded_from_normal_gateway_path(self) -> None:
        workflow = yaml.safe_load(workflow_text())
        job = workflow["jobs"]["deploy"]
        self.assertEqual(job["needs"], "release-contract")
        self.assertNotIn("QA_INFRA_OIDC_ROLE_ARN", job["env"])
        steps = job["steps"]
        by_name = {step.get("name"): step for step in steps}
        ordered = ["Read legacy rollback maintenance pin", "Validate legacy QA credentials",
                   "Configure QA credentials for legacy Worker verification", "Verify preserved legacy Worker",
                   "Plan legacy rollback without changing QA pins", "Restore Stage0 credentials for legacy host safety",
                   "Pause DROP before legacy gateway mutation", "Disable Boundary before legacy gateway mutation"]
        indexes = [steps.index(by_name[name]) for name in ordered]
        self.assertEqual(indexes, sorted(indexes))
        self.assertLess(indexes[-1], steps.index(by_name["Deploy via SSM Run-Command"]))
        for name in ordered:
            self.assertEqual(by_name[name]["if"], "needs.release-contract.outputs.mode == 'legacy_rollback'")
            self.assertNotIn("continue-on-error", by_name[name])
        self.assertEqual(by_name[ordered[-1]]["env"]["QA_BOUNDARY_TIMER_STATE"], "disabled")
        self.assertEqual(by_name[ordered[3]]["env"]["QA_BUNDLE_VERIFY_MODE"], "discovery")
        qa = yaml.safe_load((WORKFLOW.parent / "deploy-qa-bundle.yml").read_text())["jobs"]["deploy-qa"]
        self.assertEqual(qa["concurrency"]["group"], "prod-qa-lifecycle")
        self.assertEqual(job["concurrency"]["group"],
                         "${{ needs.release-contract.outputs.mode == 'legacy_rollback' && 'prod-qa-lifecycle' || 'prod-gateway-compatible' }}")
        self.assertFalse(qa["concurrency"]["cancel-in-progress"])
        self.assertFalse(job["concurrency"]["cancel-in-progress"])

    def test_gateway_passes_live_stack_coordinates_before_bluegreen_mutation(self) -> None:
        steps = yaml.safe_load(workflow_text())["jobs"]["deploy"]["steps"]
        by_name = {step.get("name"): step for step in steps}
        resolve = by_name["Resolve QA producer coordinates (read-only)"]
        deploy = by_name["Deploy via SSM Run-Command"]
        self.assertLess(steps.index(resolve), steps.index(deploy))
        self.assertEqual(resolve["id"], "qa_coordinates")
        self.assertIn("ops/qa/resolve_qa_bundle_coordinates.py", resolve["run"])
        self.assertEqual(deploy["env"]["QA_BUNDLE_ENABLED"], "true")
        self.assertEqual(deploy["env"]["QA_BUNDLE_QUEUE_URL"], "${{ steps.qa_coordinates.outputs.queue_url }}")
        self.assertEqual(deploy["env"]["QA_BUNDLE_STORAGE_BUCKET"], "${{ steps.qa_coordinates.outputs.bucket }}")
        self.assertNotIn("continue-on-error", resolve)

    def test_us051_selected_components_and_drain_join_preserve_gateway_acceptance_order(self) -> None:
        steps = yaml.safe_load(workflow_text())["jobs"]["deploy"]["steps"]
        names = [step.get("name", "") for step in steps]
        by_name = {step.get("name"): step for step in steps}
        ordered = ["Deploy via SSM Run-Command", "Post-deploy gateway smoke (API + Claude paths)",
                   "Wait until 5 minutes after cutover", "Join gateway deployment and old request drain"]
        self.assertEqual([names.index(name) for name in ordered], sorted(names.index(name) for name in ordered))
        self.assertEqual(by_name[ordered[0]]["env"]["STAGE0_BLUEGREEN_WAIT_PHASE"], "cutover")
        self.assertEqual(by_name[ordered[3]]["if"], "always() && steps.ssm.outputs.command_id != ''")
        self.assertNotIn("continue-on-error", by_name[ordered[3]])

    def test_prod_and_edge_share_bluegreen_deploy_owner(self) -> None:
        prod = WORKFLOW.read_text(encoding="utf-8")
        edge = EDGE_WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("ops/stage0/deploy_via_ssm_bluegreen.sh", prod)
        self.assertIn("ops/stage0/deploy_via_ssm_bluegreen.sh", edge)
        self.assertNotIn("ops/stage0/deploy_via_ssm.sh", prod)
        self.assertNotIn("ops/stage0/deploy_via_ssm.sh", edge)

    def test_prod_and_edge_share_one_migration_safety_entry(self) -> None:
        prod = WORKFLOW.read_text(encoding="utf-8")
        edge = EDGE_WORKFLOW.read_text(encoding="utf-8")
        entry = 'python3 scripts/checks/bluegreen-migration-safety.py --release-tag'
        self.assertIn(entry, prod)
        self.assertIn(entry, edge)
        self.assertNotIn("git ls-remote --tags origin", prod)
        self.assertNotIn("git ls-remote --tags origin", edge)
        self.assertEqual(prod.count(entry), 2)
        self.assertEqual(edge.count(entry), 1)

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
        self.assertRegex(body, r"(?ms)options:\s*\n\s*- deploy\s*\n\s*- replay\s*\n\s*- smoke-only\s*$")

    def test_focused_ssot_input_is_optional_and_defaults_empty(self) -> None:
        text = workflow_text()
        focused = re.search(
            r"(?ms)^      ssot_models:\n(?P<body>.*?)(?=^      [A-Za-z0-9_-]+:\n|^# Default)",
            text,
        )
        self.assertIsNotNone(focused)
        body = focused.group("body")
        self.assertIn("required: false", body)
        self.assertIn("type: string", body)
        self.assertIn('default: ""', body)

    def test_deploy_job_retains_mutating_capabilities_and_canonical_gates(self) -> None:
        deploy = job_block("deploy")
        self.assertIn("if: inputs.operation == 'deploy'", deploy)
        self.assertIn("environment: prod", deploy)
        self.assertIn("id-token: write", deploy)
        self.assertIn("packages: read", deploy)
        self.assertIn("deploy_via_ssm_bluegreen.sh", deploy)
        self.assertIn("steps.ssm.outputs.cutover_at", deploy)
        self.assertIn("bash ops/stage0/post_deploy_smoke.sh", deploy)
        self.assertIn(
            "bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --gate --deploy-canary --deploy-closeout",
            deploy,
        )

    def test_feishu_rollout_uses_pre_mutation_runtime_baseline(self) -> None:
        deploy = job_block("deploy")
        baseline = deploy.index("name: Resolve previous prod runtime tag")
        image_mutation = deploy.index("name: Deploy via SSM Run-Command")
        notification = deploy.index("name: Notify Feishu (release rollout)")
        smoke = deploy.index("name: Post-deploy gateway smoke (API + Claude paths)")

        self.assertLess(baseline, image_mutation)
        post_release = deploy.index("name: Plan checks from live→new PRs")
        post_release_immediate = deploy.index("name: Check PR hooks immediately")
        post_release_delayed = deploy.index("name: Check traffic and 5xx after 5 minutes")
        post_release_gate = deploy.index("name: Enforce post-release verdicts")
        self.assertLess(smoke, post_release)
        self.assertLess(post_release, post_release_immediate)
        self.assertLess(post_release_immediate, post_release_delayed)
        self.assertLess(post_release_delayed, post_release_gate)
        self.assertLess(post_release_gate, notification)
        self.assertIn("release_post_check.py gate", deploy[post_release_gate:notification])
        daily = (REPO_ROOT / ".github/workflows/ops-daily-diagnostics.yml").read_text()
        self.assertIn("prod-config-audit.sh", daily)
        self.assertNotIn("check-supplier-projection.sh", deploy)
        self.assertNotIn("check-account-group-bindings.sh", deploy)
        block = deploy[baseline:image_mutation]
        self.assertIn("resolve-prod-running-tag-via-ssm.sh", block)
        self.assertIn('INSTANCE_ID: ${{ steps.instance.outputs.id }}', block)
        self.assertIn('--instance-id "$INSTANCE_ID"', block)
        self.assertIn("id: previous_runtime", block)
        notice = deploy[notification:]
        self.assertIn("steps.previous_runtime.outputs.tag", notice)
        self.assertIn("--previous-tag", notice)
        self.assertIn("collect-feishu-release-notes.sh", notice)
        self.assertNotIn("git tag -l --format", notice)

    def test_target_release_contract_is_bound_before_prod_mutation(self) -> None:
        deploy = job_block("deploy")
        target_checkout = deploy.index("name: Checkout target-tag host artifacts")
        image_mutation = deploy.index("name: Deploy via SSM Run-Command")
        self.assertLess(target_checkout, image_mutation)
        self.assertIn("ref: v${{ inputs.tag }}", deploy)

    def test_bundle_coordinates_are_not_hardcoded_in_deploy_owners(self) -> None:
        forbidden = (
            r"https://sqs\.[a-z0-9-]+\.amazonaws\.com/[0-9]{12}/[A-Za-z0-9_.-]+",
            r"\btokenkey-[A-Za-z0-9-]*qa-bundles-[0-9]{12}\b",
        )
        for path in (WORKFLOW, BLUEGREEN_DEPLOY):
            body = path.read_text(encoding="utf-8")
            for pattern in forbidden:
                with self.subTest(path=path.name, pattern=pattern):
                    self.assertNotRegex(body, pattern)

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
        self.assertIn(
            'python3 scripts/checks/ssot-delta-gate.py focused --models "$INPUT_SSOT_MODELS"',
            smoke,
        )
        self.assertIn("INPUT_SSOT_MODELS: ${{ inputs.ssot_models }}", smoke)
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
