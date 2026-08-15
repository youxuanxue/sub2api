#!/usr/bin/env python3
from __future__ import annotations

import copy
import json
import pathlib
import re
import subprocess
import tempfile
import unittest

import yaml


ROOT = pathlib.Path(__file__).resolve().parents[3]
TEMPLATE = pathlib.Path(__file__).with_name("stage0-qa-raw-archive.yaml")
DEPLOY = ROOT / "ops/qa/deploy_qa_raw_archive_cfn.sh"
DEPLOY_SURFACES = (
    ROOT / "ops/stage0/deploy_via_ssm.sh",
    ROOT / "ops/stage0/deploy_via_ssm_bluegreen.sh",
)
ACTIVE_RUNTIME_SURFACES = DEPLOY_SURFACES + (
    ROOT / "ops/stage0/assert-live-host-state.sh",
    ROOT / "ops/stage0/live_host_state_verdict.py",
    ROOT / "ops/stage0/test_assert_live_host_state.py",
    ROOT / "ops/stage0/test_deploy_via_ssm_qa_export.py",
    ROOT / "ops/stage0/test_deploy_via_ssm_bluegreen.py",
    ROOT / "ops/stage0/test_deploy_via_ssm_edge_qa_capture.py",
    ROOT / "ops/qa/edge_phase1_baseline.py",
    ROOT / "ops/qa/prod_phase2_baseline.py",
    ROOT / "ops/qa/verify_raw_archive_iam_contract.py",
    ROOT / "ops/qa/test_qa_phase_ops.py",
    ROOT / "deploy/aws/cloudformation/stage0-backups.yaml",
)
EDGE_ROLE_ARN = "arn:${AWS::Partition}:iam::${AWS::AccountId}:role/tokenkey-lightsail-ssm-hybrid"


class CloudFormationLoader(yaml.SafeLoader):
    pass


def _cloudformation_tag(loader: CloudFormationLoader, _suffix: str, node: yaml.Node):
    if isinstance(node, yaml.ScalarNode):
        return loader.construct_scalar(node)
    if isinstance(node, yaml.SequenceNode):
        return loader.construct_sequence(node)
    return loader.construct_mapping(node)


CloudFormationLoader.add_multi_constructor("!", _cloudformation_tag)


def _load_template() -> dict:
    return yaml.load(TEMPLATE.read_text(encoding="utf-8"), Loader=CloudFormationLoader)


class Stage0QABundleContractTest(unittest.TestCase):
    def setUp(self) -> None:
        self.template = _load_template()

    def test_bundle_bucket_browser_surface_and_app_role_are_scoped(self) -> None:
        self.assert_bundle_bucket_contract(self.template)

    def assert_bundle_bucket_contract(self, template: dict) -> None:
        resources = template["Resources"]
        bucket = resources["QaBundleBucket"]["Properties"]
        self.assertEqual(bucket["PublicAccessBlockConfiguration"], {
            "BlockPublicAcls": True, "BlockPublicPolicy": True,
            "IgnorePublicAcls": True, "RestrictPublicBuckets": True,
        })
        self.assertEqual(bucket["CorsConfiguration"]["CorsRules"], [{
            "AllowedHeaders": ["*"],
            "AllowedMethods": ["GET", "HEAD"],
            "AllowedOrigins": ["BundleBrowserAllowedOrigin"],
            "ExposedHeaders": ["ETag", "Content-Encoding", "Content-Length"],
            "MaxAge": 300,
        }])
        rule = bucket["LifecycleConfiguration"]["Rules"][0]
        self.assertEqual(rule["Prefix"], "user-qa/qa-bundles/v1/jobs/")
        self.assertEqual(rule["ExpirationInDays"], "BundleRetentionDays")

        statements = resources["QaBundleBucketPolicy"]["Properties"]["PolicyDocument"]["Statement"]
        policy = {statement["Sid"]: statement for statement in statements}
        self.assertEqual(policy["DenyLightsailEdgeRole"], {
            "Sid": "DenyLightsailEdgeRole", "Effect": "Deny", "Principal": "*", "Action": "s3:*",
            "Resource": ["QaBundleBucket.Arn", "${QaBundleBucket.Arn}/*"],
            "Condition": {"ArnEquals": {"aws:PrincipalArn": EDGE_ROLE_ARN}},
        })
        create = policy["AllowAppInstanceRoleCreateScopedBundleJobs"]
        self.assertEqual(create["Action"], "s3:PutObject")
        self.assertEqual(create["Resource"], "${QaBundleBucket.Arn}/user-qa/qa-bundles/v1/jobs/*/spec.json")
        read = policy["AllowAppInstanceRoleReadScopedBundleArtifacts"]
        self.assertEqual(read["Action"], "s3:GetObject")
        self.assertEqual(read["Resource"], [
            "${QaBundleBucket.Arn}/user-qa/qa-bundles/v1/jobs/*/spec.json",
            "${QaBundleBucket.Arn}/user-qa/qa-bundles/v1/jobs/*/receipt.json",
            "${QaBundleBucket.Arn}/user-qa/qa-bundles/v1/jobs/*/failure.json",
            "${QaBundleBucket.Arn}/user-qa/qa-bundles/v1/jobs/*/generation/manifest.json",
            "${QaBundleBucket.Arn}/user-qa/qa-bundles/v1/jobs/*/generation/pages/*.json.gz",
            "${QaBundleBucket.Arn}/user-qa/qa-bundles/v1/jobs/*/export.zip",
        ])
        app_list = policy["AllowAppInstanceRoleListScopedBundleJobs"]
        self.assertEqual(app_list["Action"], "s3:ListBucket")
        self.assertEqual(app_list["Resource"], "QaBundleBucket.Arn")
        self.assertEqual(app_list["Condition"], {"StringLike": {"s3:prefix": ["user-qa/qa-bundles/v1/jobs/*"]}})
        origin = template["Parameters"]["BundleBrowserAllowedOrigin"]
        self.assertEqual(origin["Type"], "String")
        self.assertIsNotNone(re.fullmatch(origin["AllowedPattern"], "https://app.tokenkey.example"))
        self.assertIsNone(re.fullmatch(origin["AllowedPattern"], "*"))
        self.assertIsNone(re.fullmatch(origin["AllowedPattern"], "http://app.tokenkey.example"))

    def test_worker_uses_explicit_private_ghcr_credentials_and_minimal_surfaces(self) -> None:
        self.assert_worker_contract(self.template)

    def assert_worker_contract(self, template: dict) -> None:
        parameters = template["Parameters"]
        self.assertEqual(parameters["BundleWorkerDesiredCount"]["Default"], 0)
        self.assertEqual(parameters["BundleWorkerRepositoryCredentialsSecretArn"]["NoEcho"], True)
        self.assertNotIn("Default", parameters["BundleWorkerImage"])
        resources = template["Resources"]
        task = resources["QaBundleWorkerTaskDefinition"]["Properties"]
        container = task["ContainerDefinitions"][0]
        self.assertEqual(container["RepositoryCredentials"], {"CredentialsParameter": "BundleWorkerRepositoryCredentialsSecretArn"})
        self.assertEqual(container["Command"], ["--qa-bundle-worker"])
        environment = {item["Name"]: item["Value"] for item in container["Environment"]}
        self.assertEqual(environment, {
            "QA_BUNDLE_ENABLED": "true",
            "QA_BUNDLE_QUEUE_URL": "QaBundleQueue",
            "QA_BUNDLE_STORAGE_DRIVER": "s3",
            "QA_BUNDLE_STORAGE_REGION": "AWS::Region",
            "QA_BUNDLE_STORAGE_BUCKET": "QaBundleBucket",
            "QA_BUNDLE_STORAGE_PREFIX": "user-qa",
            "QA_ARCHIVE_ENABLED": "true",
            "QA_ARCHIVE_STORAGE_DRIVER": "s3",
            "QA_ARCHIVE_STORAGE_REGION": "AWS::Region",
            "QA_ARCHIVE_STORAGE_BUCKET": "QaRawArchiveBucket",
            "QA_ARCHIVE_STORAGE_PREFIX": "raw/v1",
        })
        self.assertEqual(resources["QaBundleWorkerService"]["Properties"]["DesiredCount"], "BundleWorkerDesiredCount")

        execution = resources["QaBundleWorkerExecutionRole"]["Properties"]
        secret = execution["Policies"][0]["PolicyDocument"]["Statement"][0]
        self.assertEqual(secret["Action"], "secretsmanager:GetSecretValue")
        self.assertEqual(secret["Resource"], "BundleWorkerRepositoryCredentialsSecretArn")

        role = resources["QaBundleWorkerTaskRole"]["Properties"]
        statements = {statement["Sid"]: statement for policy in role["Policies"] for statement in policy["PolicyDocument"]["Statement"]}
        self.assertEqual(statements["ReadRawArchive"]["Action"], "s3:GetObject")
        self.assertEqual(statements["ReadRawArchive"]["Resource"], "${QaRawArchiveBucket.Arn}/raw/v1/*")
        self.assertEqual(statements["WriteBundleJobSurface"]["Action"], ["s3:GetObject", "s3:PutObject"])
        self.assertEqual(statements["WriteBundleJobSurface"]["Resource"], "${QaBundleBucket.Arn}/user-qa/qa-bundles/v1/jobs/*")
        self.assertEqual(statements["ListBundleJobSurface"], {
            "Sid": "ListBundleJobSurface", "Effect": "Allow", "Action": "s3:ListBucket",
            "Resource": "QaBundleBucket.Arn",
            "Condition": {"StringLike": {"s3:prefix": ["user-qa/qa-bundles/v1/jobs/*"]}},
        })
        self.assertEqual(statements["ConsumeBundleQueue"]["Action"], ["sqs:DeleteMessage", "sqs:GetQueueAttributes", "sqs:ReceiveMessage"])
        self.assertEqual(statements["ConsumeBundleQueue"]["Resource"], "QaBundleQueue.Arn")
        self.assertEqual(resources["QaBundleQueue"]["Properties"]["RedrivePolicy"]["deadLetterTargetArn"], "QaBundleDeadLetterQueue.Arn")
        security_group = resources["QaBundleWorkerSecurityGroup"]["Properties"]
        self.assertEqual(security_group["VpcId"], "VpcId")
        self.assertNotIn("SecurityGroupIngress", security_group)
        self.assertEqual(security_group["SecurityGroupEgress"], [{"IpProtocol": "-1", "CidrIp": "0.0.0.0/0"}])
        network = resources["QaBundleWorkerService"]["Properties"]["NetworkConfiguration"]["AwsvpcConfiguration"]
        self.assertEqual(network, {
            "AssignPublicIp": "ENABLED", "Subnets": "BundleWorkerPublicSubnetIds",
            "SecurityGroups": ["QaBundleWorkerSecurityGroup.GroupId"],
        })

    def test_contract_rejects_broad_bucket_or_worker_iam(self) -> None:
        broadened_bucket = copy.deepcopy(self.template)
        statements = broadened_bucket["Resources"]["QaBundleBucketPolicy"]["Properties"]["PolicyDocument"]["Statement"]
        next(item for item in statements if item["Sid"] == "AllowAppInstanceRoleCreateScopedBundleJobs")["Resource"] = "${QaBundleBucket.Arn}/*"
        with self.assertRaises(AssertionError):
            self.assert_bundle_bucket_contract(broadened_bucket)

        broadened_worker = copy.deepcopy(self.template)
        policies = broadened_worker["Resources"]["QaBundleWorkerTaskRole"]["Properties"]["Policies"]
        raw = next(statement for policy in policies for statement in policy["PolicyDocument"]["Statement"] if statement["Sid"] == "ReadRawArchive")
        raw["Action"] = "s3:*"
        raw["Resource"] = "${QaRawArchiveBucket.Arn}/*"
        with self.assertRaises(AssertionError):
            self.assert_worker_contract(broadened_worker)

    def test_active_runtime_has_no_legacy_export_alias_or_app_role_grant(self) -> None:
        for surface in ACTIVE_RUNTIME_SURFACES:
            with self.subTest(surface=surface):
                self.assertNotIn("QA_CAPTURE_EXPORT_STORAGE", surface.read_text(encoding="utf-8"))

        backups = yaml.load(
            (ROOT / "deploy/aws/cloudformation/stage0-backups.yaml").read_text(encoding="utf-8"),
            Loader=CloudFormationLoader,
        )
        statements = backups["Resources"]["QaExportsBucketPolicy"]["Properties"]["PolicyDocument"]["Statement"]
        self.assertFalse(any(statement.get("Principal") == {"AWS": "AppInstanceRoleArn"} for statement in statements))

    def test_guarded_deploy_renders_bundle_parameters_and_runtime_surfaces_drop_export_aliases(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            temp = pathlib.Path(temp_dir)
            fake_bin = temp / "bin"
            fake_bin.mkdir()
            call_log = temp / "aws-calls.log"
            fake_aws = fake_bin / "aws"
            fake_aws.write_text(
                """#!/usr/bin/env bash
printf '%s\\n' "$*" >> "${AWS_CALL_LOG}"
if [[ "$*" == *"sts get-caller-identity"* ]]; then echo 123456789012; exit 0; fi
if [[ "$*" == *"cloudformation describe-stacks"* ]]; then echo arn:aws:iam::123456789012:role/generated-existing-role; exit 0; fi
if [[ "$*" == *"cloudformation create-change-set"* ]]; then exit 91; fi
exit 91
""", encoding="utf-8")
            fake_aws.chmod(0o755)
            proc = subprocess.run(
                ["bash", str(DEPLOY)],
                env={
                    "PATH": f"{fake_bin}:/usr/bin:/bin", "AWS_CALL_LOG": str(call_log),
                    "APP_INSTANCE_ROLE_ARN": "arn:aws:iam::123456789012:role/stage0-shared",
                    "OPS_RECOVERY_PRINCIPAL_ARN": "arn:aws:iam::123456789012:user/qa-operator",
                    "QA_RAW_ARCHIVE_VPC_ID": "vpc-1234abcd",
                    "QA_RAW_ARCHIVE_ROUTE_TABLE_IDS": "rtb-1111aaaa,rtb-2222bbbb",
                    "QA_BUNDLE_WORKER_PUBLIC_SUBNET_IDS": "subnet-1111aaaa,subnet-2222bbbb",
                    "QA_BUNDLE_WORKER_REPOSITORY_CREDENTIALS_SECRET_ARN": "arn:aws:secretsmanager:us-east-1:123456789012:secret:ghcr-pull",
                    "QA_BUNDLE_WORKER_IMAGE": "ghcr.io/youxuanxue/sub2api:1.8.99",
                }, capture_output=True, text=True, check=False,
            )
            self.assertEqual(proc.returncode, 91, msg=proc.stderr)
            calls = call_log.read_text(encoding="utf-8")
            self.assertIn('"ParameterKey":"BundleWorkerPublicSubnetIds","ParameterValue":"subnet-1111aaaa,subnet-2222bbbb"', calls, msg=proc.stdout + proc.stderr + calls)
            self.assertIn('"ParameterKey":"BundleWorkerRepositoryCredentialsSecretArn","ParameterValue":"arn:aws:secretsmanager:us-east-1:123456789012:secret:ghcr-pull"', calls, msg=proc.stdout + proc.stderr + calls)
            self.assertIn('"ParameterKey":"BundleWorkerImage","ParameterValue":"ghcr.io/youxuanxue/sub2api:1.8.99"', calls, msg=proc.stdout + proc.stderr + calls)
        for surface in DEPLOY_SURFACES:
            text = surface.read_text(encoding="utf-8")
            self.assertNotIn("QA_CAPTURE_EXPORT_STORAGE", text)
            self.assertIn("QA_BUNDLE_STORAGE_BUCKET", text)
            self.assertIn("QA_BUNDLE_QUEUE_URL", text)

    def test_guarded_deploy_rejects_mutable_image_and_wildcard_origin(self) -> None:
        common_env = {
            "APP_INSTANCE_ROLE_ARN": "arn:aws:iam::123456789012:role/stage0-shared",
            "OPS_RECOVERY_PRINCIPAL_ARN": "arn:aws:iam::123456789012:user/qa-operator",
            "QA_RAW_ARCHIVE_VPC_ID": "vpc-1234abcd",
            "QA_RAW_ARCHIVE_ROUTE_TABLE_IDS": "rtb-1111aaaa",
            "QA_BUNDLE_WORKER_PUBLIC_SUBNET_IDS": "subnet-1111aaaa",
            "QA_BUNDLE_WORKER_REPOSITORY_CREDENTIALS_SECRET_ARN": "arn:aws:secretsmanager:us-east-1:123456789012:secret:ghcr-pull",
        }
        mutable = subprocess.run(
            ["bash", str(DEPLOY)], env={"PATH": "/usr/bin:/bin", **common_env, "QA_BUNDLE_WORKER_IMAGE": "ghcr.io/acme/worker:latest"},
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(mutable.returncode, 1)
        self.assertIn("immutable release tag or sha256 digest", mutable.stderr)
        wildcard = subprocess.run(
            ["bash", str(DEPLOY)], env={"PATH": "/usr/bin:/bin", **common_env, "QA_BUNDLE_WORKER_IMAGE": "ghcr.io/acme/worker:1.0.0", "QA_BUNDLE_BROWSER_ALLOWED_ORIGIN": "*"},
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(wildcard.returncode, 1)
        self.assertIn("one exact HTTPS origin", wildcard.stderr)


if __name__ == "__main__":
    unittest.main()
