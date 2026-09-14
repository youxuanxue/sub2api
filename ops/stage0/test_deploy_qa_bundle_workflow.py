#!/usr/bin/env python3
"""Security and behavior contract tests for standalone QA Bundle workflow modes."""
from __future__ import annotations

import pathlib
import os
import subprocess
import tempfile
import re
import unittest
import yaml


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOW = REPO_ROOT / ".github" / "workflows" / "deploy-qa-bundle.yml"


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




class DeployQABundleWorkflowTest(unittest.TestCase):
    def test_workflow_inputs_and_concurrency(self) -> None:
        data = yaml.safe_load(workflow_text())
        inputs = (data.get("on") or data[True])["workflow_dispatch"]["inputs"]
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

    def step(self, name, job="deploy-qa"):
        return next(step for step in yaml.safe_load(workflow_text())["jobs"][job]["steps"]
                    if step.get("name") == name)

    def test_requested_tag_artifacts_and_manifest_precede_aws_mutation(self):
        steps = yaml.safe_load(workflow_text())["jobs"]["deploy-qa"]["steps"]
        names = [step.get("name") for step in steps]
        checkout = self.step("Checkout requested QA release artifacts")
        self.assertEqual(checkout["with"]["ref"], "v${{ inputs.tag }}")
        self.assertEqual(checkout["with"]["path"], "qa-target-release")
        self.assertLess(names.index("Verify requested QA release before mutation"),
                        names.index("Configure AWS credentials via OIDC"))
        self.assertEqual(self.step("Deploy QA Bundle infrastructure")["run"],
                         "bash qa-target-release/ops/qa/deploy_qa_raw_archive_cfn.sh")
        script = self.step("Verify requested QA release before mutation")["run"]
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root / "ops/stage0").mkdir(parents=True)
            (root / "ops/stage0/verify_ghcr_manifest.sh").write_text("exit 42\n")
            result = subprocess.run(["bash", "-c", script], cwd=root, text=True,
                                    capture_output=True, env={**os.environ, "INPUT_TAG": "1.8.228"})
        self.assertEqual(result.returncode, 42, result.stderr)
        self.assertNotIn("resolve_qa_bundle_worker_image", result.stderr)

    def test_instance_resolver_executes_complete_aws_command_and_propagates_failure(self):
        script = self.step("Resolve target instance and network context")["run"]
        aws = r'''aws() {
  if [ "${AWS_FAIL:-}" = yes ]; then return 42; fi
  case "$1 $2" in
    'cloudformation describe-stacks')
      if [ "$4" = prod ]; then
        [[ "$*" == *--query* ]] || return 43
        printf '%s\n' '[{"OutputKey":"InstanceId","OutputValue":"i-test"},{"OutputKey":"ApiUrl","OutputValue":"https://api.example.com"}]'
      else
        echo arn:aws:iam::123456789012:role/qa-cfn
      fi ;;
    'cloudformation describe-stack-resources')
      printf '%s\n' '{"StackResources":[{"LogicalResourceId":"VPC","PhysicalResourceId":"vpc-ab"},{"LogicalResourceId":"PublicSubnet","PhysicalResourceId":"subnet-ab"},{"LogicalResourceId":"PublicRouteTable","PhysicalResourceId":"rtb-ab"},{"LogicalResourceId":"InstanceRole","PhysicalResourceId":"app"}]}' ;;
    'sts get-caller-identity') echo 123456789012 ;;
    *) return 44 ;;
  esac
}
'''
        for failure in ("no", "yes"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                output = pathlib.Path(directory) / "output"
                result = subprocess.run(["bash", "-c", aws + script], text=True, capture_output=True,
                                        env={**os.environ, "AWS_FAIL": failure, "STACK_NAME": "prod",
                                             "CICD_OIDC_STACK_NAME": "oidc", "GITHUB_OUTPUT": str(output)})
                if failure == "yes":
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(output.exists())
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn("id=i-test\n", output.read_text())
                    self.assertIn("browser_origin=https://example.com\n", output.read_text())

    def test_existing_recovery_principal_is_preserved_and_errors_never_bootstrap(self):
        step = self.step("Preserve QA recovery principal")
        self.assertEqual(step["if"], "inputs.operation == 'deploy'")
        self.assertEqual(self.step("Deploy QA Bundle infrastructure")["env"]["OPS_RECOVERY_PRINCIPAL_ARN"],
                         "${{ steps.qa_recovery.outputs.principal }}")
        aws = r'''aws() {
  case "$CASE" in
    existing) echo '{"Stacks":[{"Parameters":[{"ParameterKey":"OpsRecoveryPrincipalArn","ParameterValue":"arn:aws:iam::123456789012:role/existing"}]}]}' ;;
    missing) echo '(ValidationError) Stack does not exist' >&2; return 42 ;;
    denied) echo '(AccessDenied) denied' >&2; return 42 ;;
    broken) echo '{"Stacks":[{"Parameters":[]}]}' ;;
    *) return 43 ;;
  esac
}
'''
        for case in ("existing", "missing", "denied", "broken"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as directory:
                output = pathlib.Path(directory) / "output"
                configured = "arn:aws:iam::123456789012:user/bootstrap"
                result = subprocess.run(["bash", "-c", aws + step["run"]], text=True, capture_output=True,
                                        env={**os.environ, "CASE": case, "QA_RAW_ARCHIVE_STACK": "qa",
                                             "CONFIGURED_OPS_RECOVERY_PRINCIPAL_ARN": configured,
                                             "GITHUB_OUTPUT": str(output)})
                if case in ("denied", "broken"):
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(output.exists())
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    expected = configured if case == "missing" else "arn:aws:iam::123456789012:role/existing"
                    self.assertEqual(output.read_text(), "principal=" + expected + "\n")

    def test_summaries_render_literal_values_and_actual_status(self):
        for job in ("deploy-qa", "qa-infra-check"):
            step = self.step("Job summary", job)
            for status in ("success", "failure", "cancelled"):
                with self.subTest(job=job, status=status), tempfile.TemporaryDirectory() as directory:
                    output = pathlib.Path(directory) / "summary"
                    values = {key: "literal-$(exit 42)-`exit 43`" for key in step["env"]}
                    values.update(JOB_STATUS=status, QA_STACK_NAME="qa-stack", GITHUB_STEP_SUMMARY=str(output))
                    result = subprocess.run(["bash", "-euc", step["run"]], text=True,
                                            capture_output=True, env={**os.environ, **values})
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stderr, "")
                    self.assertIn("literal-$(exit 42)-`exit 43`", output.read_text())
                    if job == "deploy-qa":
                        self.assertEqual(step["env"]["JOB_STATUS"], "${{ job.status }}")
                        self.assertIn("- status: " + status, output.read_text())


if __name__ == "__main__":
    unittest.main()
