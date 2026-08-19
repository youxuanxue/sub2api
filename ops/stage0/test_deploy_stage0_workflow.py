#!/usr/bin/env python3
"""Security and behavior contract tests for deploy-stage0 workflow modes."""
from __future__ import annotations

import pathlib
import re
import unittest


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOW = REPO_ROOT / ".github" / "workflows" / "deploy-stage0.yml"
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
        self.assertRegex(body, r"(?ms)options:\s*\n\s*- deploy\s*\n\s*- smoke-only\s*\n\s*- qa-infra-check\s*$")

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
        self.assertIn("QA_MAINTENANCE_TIMER_STATE: enabled", deploy)
        self.assertIn("sync-qa-maintenance-timer-via-ssm.sh", deploy)
        self.assertIn("QA_BOUNDARY_TIMER_STATE: auto", deploy)
        self.assertIn("sync-qa-boundary-timer-via-ssm.sh", deploy)
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
        self.assertLess(smoke, notification)
        block = deploy[baseline:image_mutation]
        self.assertIn("resolve-prod-running-tag-via-ssm.sh", block)
        self.assertIn('INSTANCE_ID: ${{ steps.instance.outputs.id }}', block)
        self.assertIn('--instance-id "$INSTANCE_ID"', block)
        self.assertIn("id: previous_runtime", block)
        notice = deploy[notification:]
        self.assertIn("steps.previous_runtime.outputs.tag", notice)
        self.assertIn("--previous-tag", notice)
        self.assertIn("v$PREVIOUS_TAG..v$INPUT_TAG", notice)
        self.assertNotIn("git tag -l --format", notice)

    def test_target_release_contract_is_bound_before_prod_mutation(self) -> None:
        deploy = job_block("deploy")
        target_checkout = deploy.index("name: Checkout target-tag QA contract and host artifacts")
        target_verify = deploy.index("name: Verify target-tag QA release tree")
        resolve = deploy.index("name: Resolve QA infrastructure deploy inputs")
        infra_mutation = deploy.index("name: Deploy QA Bundle infrastructure")
        image_mutation = deploy.index("name: Deploy via SSM Run-Command")

        self.assertLess(target_checkout, target_verify)
        self.assertLess(target_verify, resolve)
        self.assertLess(resolve, infra_mutation)
        self.assertLess(infra_mutation, image_mutation)
        self.assertIn("qa-target-release/ops/qa/deploy_rollout.yaml", deploy)
        self.assertIn("marker: legacy_release", deploy)
        self.assertIn("rollout=${ROLLOUT}", deploy)
        self.assertIn("TARGET_ROLLOUT: ${{ steps.qa_target.outputs.rollout }}", deploy)
        self.assertIn('--target-rollout "$TARGET_ROLLOUT"', deploy)
        self.assertIn("ref: v${{ inputs.tag }}", deploy)
        self.assertEqual(deploy.count("QA_HOST_ARTIFACT_ROOT: qa-target-release"), 2)

    def test_bundle_coordinates_come_from_verified_stack_outputs(self) -> None:
        deploy = job_block("deploy")
        fixed_desired = {
            "QA_BUNDLE_ENABLED": "true",
            "QA_BUNDLE_STORAGE_DRIVER": "s3",
            "QA_BUNDLE_STORAGE_PREFIX": "user-qa",
        }
        for key, value in fixed_desired.items():
            with self.subTest(key=key):
                self.assertEqual(deploy.count(f"{key}: \"{value}\""), 1)

        verify_step = deploy[
            deploy.index("- name: Verify QA Bundle infrastructure"):
            deploy.index("- name: Restore Stage0 deployment credentials via OIDC")
        ]
        self.assertIn("id: qa_bundle", verify_step)
        self.assertIn("verify_qa_bundle_infra.sh", verify_step)

        deploy_step = deploy[
            deploy.index("- name: Deploy via SSM Run-Command"):
            deploy.index("- name: External health check")
        ]
        desired = {
            "QA_BUNDLE_ENABLED": "${{ env.QA_BUNDLE_ENABLED }}",
            "QA_BUNDLE_QUEUE_URL": "${{ steps.qa_bundle.outputs.queue_url }}",
            "QA_BUNDLE_STORAGE_DRIVER": "${{ env.QA_BUNDLE_STORAGE_DRIVER }}",
            "QA_BUNDLE_STORAGE_REGION": "${{ env.AWS_REGION }}",
            "QA_BUNDLE_STORAGE_BUCKET": "${{ steps.qa_bundle.outputs.bucket }}",
            "QA_BUNDLE_STORAGE_PREFIX": "${{ env.QA_BUNDLE_STORAGE_PREFIX }}",
        }
        for key, value in desired.items():
            with self.subTest(deploy_key=key):
                self.assertIn(f"{key}: {value}", deploy_step)

        assertion = deploy[deploy.index("- name: Assert live-host state (drift check)"):]
        expect_env = ",".join(f"{key}={value}" for key, value in desired.items())
        self.assertIn(f"EXPECT_ENV: {expect_env}", assertion)
        self.assertIn("assert-live-host-state.sh", assertion)

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

    def test_existing_qa_stack_query_fails_closed_except_for_not_found(self) -> None:
        deploy = job_block("deploy")
        qa_credentials = deploy.index("name: Configure QA infrastructure credentials via OIDC")
        resolve = deploy.index("name: Resolve QA infrastructure deploy inputs")
        infra = deploy.index("name: Deploy QA Bundle infrastructure")
        self.assertLess(qa_credentials, resolve)
        self.assertLess(resolve, infra)

        block = deploy[resolve:infra]
        self.assertIn("aws cloudformation describe-stacks", block)
        self.assertIn("ValidationError", block)
        self.assertIn("does not exist", block)
        self.assertIn("exit 1", block)
        self.assertNotIn("2>/dev/null || true", block)
        self.assertNotRegex(block, r"describe-stacks[^\n]*\|\|\s*true")

    def test_legacy_worker_discovery_precedes_resolution_and_all_mutation(self) -> None:
        deploy = job_block("deploy")
        discovery = deploy.index("name: Discover existing QA Bundle Worker (read-only)")
        resolve = deploy.index("name: Resolve QA infrastructure deploy inputs")
        host_safety = deploy.index("name: Converge current QA maintenance runner before legacy app rollback")
        infra_mutation = deploy.index("name: Deploy QA Bundle infrastructure")
        image_mutation = deploy.index("name: Deploy via SSM Run-Command")
        self.assertLess(discovery, resolve)
        self.assertLess(resolve, host_safety)
        self.assertLess(host_safety, infra_mutation)
        self.assertLess(infra_mutation, image_mutation)
        pre_mutation = deploy[:infra_mutation]
        self.assertIn("QA_BUNDLE_VERIFY_MODE: discovery", pre_mutation)
        self.assertIn("steps.qa_worker_discovery.outputs.worker_image", pre_mutation)
        self.assertIn(
            '--verified-existing-image "$VERIFIED_EXISTING_WORKER_IMAGE"',
            pre_mutation,
        )
        self.assertIn("ops/qa/qa_bundle_release_surface.py", pre_mutation)
        self.assertIn("--surface-json", pre_mutation)
        self.assertIn("fail closed to target Worker + canary", pre_mutation)

    def test_bundle_infrastructure_is_ready_before_app_image_swap(self) -> None:
        deploy = job_block("deploy")
        infra = deploy.index("name: Deploy QA Bundle infrastructure")
        verify = deploy.index("name: Verify QA Bundle infrastructure")
        image_mutation = deploy.index("name: Deploy via SSM Run-Command")
        self.assertLess(infra, verify)
        self.assertLess(verify, image_mutation)
        pre_mutation = deploy[:image_mutation]
        resolved_image = "${{ steps.qa_infra.outputs.resolved_worker_image }}"
        self.assertEqual(pre_mutation.count(f"QA_BUNDLE_WORKER_IMAGE: {resolved_image}"), 2)
        self.assertNotIn("QA_BUNDLE_WORKER_IMAGE: ghcr.io/youxuanxue/sub2api:${{ env.INPUT_TAG }}", pre_mutation)
        self.assertIn("QA_BUNDLE_WORKER_DESIRED_COUNT: \"1\"", pre_mutation)
        self.assertIn('browser_origin="https://${API_HOST#api.}"', pre_mutation)
        self.assertIn('echo "browser_origin=$browser_origin" >> "$GITHUB_OUTPUT"', pre_mutation)
        self.assertIn("QA_BUNDLE_BROWSER_ALLOWED_ORIGIN: ${{ steps.instance.outputs.browser_origin }}", pre_mutation)
        self.assertIn("deploy_qa_raw_archive_cfn.sh", pre_mutation)
        self.assertIn("verify_qa_bundle_infra.sh", pre_mutation)

    def test_real_maintenance_unit_health_gate_blocks_before_bundle_canary(self) -> None:
        deploy = job_block("deploy")
        maintenance_sync = deploy.index("name: Sync QA maintenance host runner")
        boundary_sync = deploy.index(
            "name: Sync QA boundary host runner and restore durable owner"
        )
        health_gate = deploy.index("name: Verify QA maintenance systemd execution")
        bundle_canary = deploy.index("name: Post-deploy QA Bundle canary")

        self.assertLess(maintenance_sync, boundary_sync)
        self.assertLess(boundary_sync, health_gate)
        self.assertLess(health_gate, bundle_canary)
        gate = deploy[health_gate:bundle_canary]
        self.assertIn("if: steps.qa_infra.outputs.mode == 'phase3'", gate)
        self.assertIn("run-qa-maintenance-health-gate-via-ssm.sh", gate)

    def test_legacy_rollback_converges_safe_control_plane_before_app_mutation(self) -> None:
        deploy = job_block("deploy")
        maintenance = deploy.index("name: Converge current QA maintenance runner before legacy app rollback")
        boundary = deploy.index("name: Disable QA boundary before legacy app rollback")
        app_mutation = deploy.index("name: Deploy via SSM Run-Command")
        self.assertLess(maintenance, boundary)
        self.assertLess(boundary, app_mutation)
        safety = deploy[maintenance:app_mutation]
        self.assertIn("QA_MAINTENANCE_TIMER_STATE: enabled", safety)
        self.assertIn("QA_BOUNDARY_TIMER_STATE: disabled", safety)
        self.assertNotIn("QA_HOST_ARTIFACT_ROOT: qa-target-release", safety)

        canary = deploy[deploy.index("- name: Post-deploy QA Bundle canary"):]
        self.assertIn("if: steps.qa_infra.outputs.run_canary == 'true'", canary)
        warning = deploy[deploy.index("- name: Report legacy QA rollback degradation"):]
        self.assertIn("QA Phase 3 degraded", warning)
        self.assertIn("boundary was forced disabled", warning)
        self.assertIn("DROP is paused", warning)

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
