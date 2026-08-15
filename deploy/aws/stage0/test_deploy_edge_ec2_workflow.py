#!/usr/bin/env python3
from __future__ import annotations

import pathlib
import re
import subprocess
import unittest

import yaml


REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
WORKFLOW = REPO_ROOT / ".github/workflows/deploy-edge-stage0.yml"


class CfnLoader(yaml.SafeLoader):
    pass


def construct_cfn_tag(loader: CfnLoader, _suffix: str, node: yaml.Node) -> object:
    if isinstance(node, yaml.ScalarNode):
        return loader.construct_scalar(node)
    if isinstance(node, yaml.SequenceNode):
        return loader.construct_sequence(node)
    return loader.construct_mapping(node)


CfnLoader.add_multi_constructor("!", construct_cfn_tag)


class DeployEdgeEc2WorkflowTest(unittest.TestCase):
    def setUp(self) -> None:
        self.text = WORKFLOW.read_text(encoding="utf-8")
        self.doc = yaml.safe_load(self.text)
        on = self.doc.get("on", self.doc.get(True))
        self.inputs = on["workflow_dispatch"]["inputs"]
        self.steps = self.doc["jobs"]["edge"]["steps"]

    def step(self, name: str) -> dict:
        return next(step for step in self.steps if step.get("name") == name)

    def test_lifecycle_operations_are_candidate_safe(self) -> None:
        self.assertEqual(
            ["provision", "upgrade", "rollback", "smoke"],
            self.inputs["operation"]["options"],
        )
        for forbidden in (
            "rotate_egress_ip",
            "decommission",
            "release-address",
            "delete-stack",
            "monthly_budget",
            "max_monthly_budget",
        ):
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, self.text)

    def test_candidate_and_exact_stack_require_explicit_confirmation(self) -> None:
        self.assertIn("confirm_stack", self.inputs)
        self.assertIn("allow_migration_candidate", self.inputs)
        self.assertIn("--confirm-stack", self.text)
        self.assertIn("--allow-migration-candidate", self.text)

    def test_workflow_uses_oidc_execution_role_and_generated_template(self) -> None:
        self.assertIn("id-token: write", self.text)
        self.assertIn("tokenkey-cfn-ec2-edge-stage0", self.text)
        self.assertIn("stage0-edge-ec2.yaml", self.text)
        self.assertIn("CAPABILITY_NAMED_IAM", self.text)
        self.assertIn("build-cfn.sh --check", self.text)

    def test_provision_passes_generated_bootstrap_values_without_s3(self) -> None:
        self.assertLessEqual(
            (REPO_ROOT / "deploy/aws/cloudformation/stage0-edge-ec2.yaml").stat().st_size,
            51_200,
        )
        provision = self.step("Provision candidate infrastructure")["run"]
        generate = "build-cfn.sh --edge-parameter-values"
        self.assertIn(generate, provision)
        self.assertIn('PARAMETER_VALUES="$(bash', provision)
        self.assertIn('"${PARAMETERS[@]}"', provision)
        self.assertLess(provision.index(generate), provision.index("allocate-address"))
        self.assertNotIn("--s3-bucket", provision)
        self.assertNotIn("EDGE_CFN_ARTIFACT_BUCKET", provision)

    def test_provision_covers_every_required_cloudformation_parameter(self) -> None:
        template = yaml.load(
            (REPO_ROOT / "deploy/aws/cloudformation/stage0-edge-ec2.yaml").read_text(
                encoding="utf-8"
            ),
            Loader=CfnLoader,
        )
        required = {
            name for name, spec in template["Parameters"].items()
            if "Default" not in spec
        }
        provision = self.step("Provision candidate infrastructure")["run"]
        explicit = set(re.findall(r"\b([A-Za-z][A-Za-z0-9]+)=", provision))
        generated = subprocess.run(
            ["bash", "deploy/aws/stage0/build-cfn.sh", "--edge-parameter-values"],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            check=True,
        )
        generated_names = {line.split("\t", 1)[0] for line in generated.stdout.splitlines()}

        self.assertEqual(set(), required - explicit - generated_names)

    def test_candidate_provision_does_not_probe_the_lightsail_owned_dns_name(self) -> None:
        provision = self.text.split("- name: Provision candidate infrastructure", 1)[1]
        provision = provision.split("- name: Upgrade or rollback", 1)[0]
        self.assertNotIn("external_health.sh", provision)
        self.assertIn("describe-instance-information", provision)
        self.assertIn('[ "$STATUS" = Online ]', provision)

    def test_provision_reuses_the_only_tagged_candidate_eip(self) -> None:
        provision = self.text.split("- name: Provision candidate infrastructure", 1)[1]
        provision = provision.split("- name: Resolve EC2 instance", 1)[0]
        self.assertIn("describe-addresses", provision)
        self.assertIn("migration-candidate", provision)
        self.assertIn("Key=Environment,Value=edge", provision)
        self.assertIn("jq 'length'", provision)
        self.assertIn("multiple candidate EIPs", provision)
        self.assertIn("--allocation-ids \"$ALLOCATION_ID\"", provision)
        self.assertIn("--query 'Addresses[0].PublicIp'", provision)
        self.assertIn('PublicIpv4="$PUBLIC_IPV4"', provision)

    def test_readiness_accepts_candidate_app_containers_not_yet_created(self) -> None:
        readiness = self.text.split("- name: Verify candidate host readiness", 1)[1]
        readiness = readiness.split("- name: Update candidate image", 1)[0]
        self.assertIn("tokenkey-caddy 2>/dev/null || echo false", readiness)
        self.assertIn("tokenkey 2>/dev/null || echo false", readiness)

    def test_candidate_lifecycle_never_starts_or_probes_the_public_app(self) -> None:
        update = self.step("Update candidate image without starting the app")
        self.assertIn("migration_candidate", update["if"])
        self.assertIn("update_ec2_edge_candidate_via_ssm.sh", update["run"])
        for name in ("Deploy active EC2 Edge", "Sync active Edge Feishu config", "Smoke active EC2 Edge"):
            self.assertIn("deployable", self.step(name)["if"])

    def test_active_ec2_owner_uses_normal_deploy_and_smoke(self) -> None:
        self.assertIn("smoke_phase", self.inputs)
        deploy = self.step("Deploy active EC2 Edge")
        self.assertIn("deployable", deploy["if"])
        self.assertIn("deploy_via_ssm.sh", deploy["run"])
        smoke = self.step("Smoke active EC2 Edge")
        self.assertIn("deployable", smoke["if"])
        self.assertIn("edge_post_deploy_smoke.sh", smoke["run"])

    def test_active_ec2_owner_cannot_silently_run_candidate_provision(self) -> None:
        validate = self.step("Validate operation and target mode")["run"]
        self.assertIn("active EC2 Edge is already provisioned", validate)


if __name__ == "__main__":
    unittest.main()
