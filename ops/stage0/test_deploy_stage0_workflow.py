#!/usr/bin/env python3
"""Security and behavior contract tests for deploy-stage0 workflow modes."""
from __future__ import annotations

import os
import pathlib
import re
import subprocess
import tempfile
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
        self.assertRegex(body, r"(?ms)options:\s*\n\s*- deploy\s*\n\s*- smoke-only\s*$")

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

    def test_qa_host_artifacts_are_bound_to_target_tag_before_prod_mutation(self) -> None:
        deploy = job_block("deploy")
        target_checkout = deploy.index("name: Checkout target-tag QA host artifacts")
        resolve = deploy.index("name: Resolve QA infrastructure deploy inputs")
        infra_mutation = deploy.index("name: Deploy QA Bundle infrastructure")
        image_mutation = deploy.index("name: Deploy via SSM Run-Command")

        self.assertLess(resolve, target_checkout)
        self.assertLess(target_checkout, infra_mutation)
        self.assertLess(target_checkout, image_mutation)
        target_block = deploy[target_checkout:image_mutation]
        self.assertEqual(
            target_block.count("if: steps.qa_infra.outputs.mode == 'phase3'"), 2
        )
        self.assertIn("ref: v${{ inputs.tag }}", target_block)
        self.assertIn("path: qa-host-runtime", target_block)
        self.assertIn("id: qa_host_artifacts", target_block)
        self.assertIn("git -C qa-host-runtime rev-parse HEAD", target_block)
        self.assertEqual(deploy.count("QA_HOST_ARTIFACT_ROOT: qa-host-runtime"), 2)
        self.assertEqual(
            deploy.count(
                "QA_HOST_ARTIFACT_SHA: ${{ steps.qa_host_artifacts.outputs.sha }}"
            ),
            2,
        )

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

    def test_qa_stack_principal_resolution_only_bootstraps_when_stack_is_absent(self) -> None:
        script = step_run("Resolve QA infrastructure deploy inputs")
        with tempfile.TemporaryDirectory() as temp_dir:
            root = pathlib.Path(temp_dir)
            fake_bin = root / "bin"
            fake_bin.mkdir()
            fake_aws = fake_bin / "aws"
            fake_aws.write_text(
                """#!/usr/bin/env bash
set -euo pipefail
case "${AWS_SCENARIO}" in
  existing)
    printf '%s\n' '{"Stacks":[{"Parameters":[{"ParameterKey":"OpsRecoveryPrincipalArn","ParameterValue":"arn:existing"},{"ParameterKey":"BundleWorkerImage","ParameterValue":"ghcr.io/youxuanxue/sub2api:1.8.158"}]}]}'
    ;;
  missing)
    echo 'An error occurred (ValidationError) when calling the DescribeStacks operation: Stack with id tokenkey-prod-qa-raw-archive does not exist' >&2
    exit 255
    ;;
  denied)
    echo 'An error occurred (AccessDenied) when calling the DescribeStacks operation' >&2
    exit 254
    ;;
esac
""",
                encoding="utf-8",
            )
            fake_aws.chmod(0o755)
            base_env = {
                **os.environ,
                "PATH": f"{fake_bin}:/usr/bin:/bin",
                "QA_STACK_NAME": "tokenkey-prod-qa-raw-archive",
                "CONFIGURED_OPS_RECOVERY_PRINCIPAL_ARN": "arn:bootstrap",
                "INPUT_TAG": "1.8.157",
            }

            for (
                scenario,
                target,
                expected_principal,
                expected_mode,
                expected_image,
            ) in (
                (
                    "existing",
                    "1.8.157",
                    "arn:existing",
                    "phase3",
                    "ghcr.io/youxuanxue/sub2api:1.8.158",
                ),
                (
                    "existing",
                    "1.8.155",
                    "arn:existing",
                    "legacy_rollback",
                    "ghcr.io/youxuanxue/sub2api:1.8.158",
                ),
                (
                    "missing",
                    "1.8.157",
                    "arn:bootstrap",
                    "phase3",
                    "ghcr.io/youxuanxue/sub2api:1.8.157",
                ),
            ):
                output = root / f"{scenario}-{target}-output"
                proc = subprocess.run(
                    ["bash", "-c", script],
                    env={
                        **base_env,
                        "AWS_SCENARIO": scenario,
                        "INPUT_TAG": target,
                        "GITHUB_OUTPUT": str(output),
                    },
                    cwd=REPO_ROOT,
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(proc.returncode, 0, msg=proc.stderr)
                outputs = output.read_text()
                self.assertIn(
                    f"ops_recovery_principal_arn={expected_principal}\n", outputs
                )
                self.assertIn(f"resolved_worker_image={expected_image}\n", outputs)
                self.assertIn(f"mode={expected_mode}\n", outputs)

            denied_output = root / "denied-output"
            denied = subprocess.run(
                ["bash", "-c", script],
                env={
                    **base_env,
                    "AWS_SCENARIO": "denied",
                    "GITHUB_OUTPUT": str(denied_output),
                },
                cwd=REPO_ROOT,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(denied.returncode, 0)
            self.assertIn("refusing bootstrap fallback", denied.stderr)
            self.assertFalse(denied_output.exists())

    def test_legacy_bootstrap_without_compatible_worker_fails_before_mutation(self) -> None:
        script = step_run("Resolve QA infrastructure deploy inputs")
        with tempfile.TemporaryDirectory() as temp_dir:
            root = pathlib.Path(temp_dir)
            fake_bin = root / "bin"
            fake_bin.mkdir()
            fake_aws = fake_bin / "aws"
            fake_aws.write_text(
                """#!/usr/bin/env bash
echo 'An error occurred (ValidationError) when calling the DescribeStacks operation: Stack with id tokenkey-prod-qa-raw-archive does not exist' >&2
exit 255
""",
                encoding="utf-8",
            )
            fake_aws.chmod(0o755)
            output = root / "output"
            proc = subprocess.run(
                ["bash", "-c", script],
                env={
                    **os.environ,
                    "PATH": f"{fake_bin}:/usr/bin:/bin",
                    "QA_STACK_NAME": "tokenkey-prod-qa-raw-archive",
                    "CONFIGURED_OPS_RECOVERY_PRINCIPAL_ARN": "arn:bootstrap",
                    "INPUT_TAG": "1.8.155",
                    "GITHUB_OUTPUT": str(output),
                },
                cwd=REPO_ROOT,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(proc.returncode, 0)
            self.assertIn("requires an existing compatible QA Bundle Worker", proc.stderr)
            self.assertFalse(output.exists())

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

    def test_legacy_rollback_preserves_phase3_control_plane_and_reports_degraded(self) -> None:
        deploy = job_block("deploy")
        phase3_guard = "if: steps.qa_infra.outputs.mode == 'phase3'"
        for step_name in (
            "Sync QA maintenance host runner",
            "Sync QA boundary host runner and restore durable owner",
            "Post-deploy QA Bundle canary",
        ):
            start = deploy.index(f"- name: {step_name}")
            end = deploy.find("      - name:", start + 1)
            block = deploy[start : end if end != -1 else None]
            self.assertIn(phase3_guard, block)

        warning_start = deploy.index("- name: Report legacy QA rollback degradation")
        warning_end = deploy.find("      - name:", warning_start + 1)
        warning = deploy[warning_start : warning_end if warning_end != -1 else None]
        self.assertIn("if: steps.qa_infra.outputs.mode == 'legacy_rollback'", warning)
        self.assertIn("QA Phase 3 degraded", warning)
        self.assertIn("existing Worker and host runners were preserved", warning)

        summary = deploy[deploy.index("- name: Job summary") :]
        self.assertIn("QA_MODE: ${{ steps.qa_infra.outputs.mode }}", summary)
        self.assertIn(
            "QA_WORKER_IMAGE: ${{ steps.qa_infra.outputs.resolved_worker_image }}",
            summary,
        )
        self.assertIn(r"- QA mode: \`${QA_MODE:-not-resolved}\`", summary)
        self.assertIn(
            r"- QA Bundle Worker image: \`${QA_WORKER_IMAGE:-not-resolved}\`", summary
        )
        self.assertIn("QA Phase 3 degraded", summary)
        self.assertIn("legacy rollback changed only the app image", summary)

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
