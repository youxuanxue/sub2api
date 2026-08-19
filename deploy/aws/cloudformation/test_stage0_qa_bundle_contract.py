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
OIDC_TEMPLATE = pathlib.Path(__file__).with_name("cicd-oidc.yaml")
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


def _load_oidc_template() -> dict:
    return yaml.load(OIDC_TEMPLATE.read_text(encoding="utf-8"), Loader=CloudFormationLoader)


class Stage0QABundleContractTest(unittest.TestCase):
    def setUp(self) -> None:
        self.template = _load_template()

    def test_bundle_bucket_browser_surface_and_app_role_are_scoped(self) -> None:
        self.assert_bundle_bucket_contract(self.template)

    def test_bundle_persistent_resources_clean_up_failed_first_create(self) -> None:
        resources = self.template["Resources"]
        for logical_id in ("QaBundleBucket", "QaBundleWorkerLogGroup"):
            with self.subTest(logical_id=logical_id):
                resource = resources[logical_id]
                self.assertEqual(resource["DeletionPolicy"], "RetainExceptOnCreate")
                self.assertEqual(resource["UpdateReplacePolicy"], "Retain")

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
            "Condition": {"ArnLike": {"aws:PrincipalArn": [
                EDGE_ROLE_ARN,
                "arn:${AWS::Partition}:iam::${AWS::AccountId}:role/tokenkey-lightsail-ssm-hybrid-*",
            ]}},
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

    def test_worker_uses_public_immutable_ghcr_image_and_minimal_surfaces(self) -> None:
        self.assert_worker_contract(self.template)

    def test_qa_cloudformation_service_role_covers_managed_resource_lifecycles(self) -> None:
        oidc = _load_oidc_template()
        policies = oidc["Resources"]["QAInfraCloudFormationServiceRole"]["Properties"]["Policies"]
        statements = {
            statement["Sid"]: set(statement["Action"])
            for policy in policies
            for statement in policy["PolicyDocument"]["Statement"]
        }
        expected = {
            "ManageQaBucketsAndObjects": {
                "s3:GetBucketCORS",
                "s3:GetBucketPublicAccessBlock",
                "s3:GetBucketVersioning",
                "s3:GetEncryptionConfiguration",
                "s3:GetLifecycleConfiguration",
                "s3:PutEncryptionConfiguration",
            },
            "ManageQaKms": {"kms:CreateAlias", "kms:DeleteAlias", "kms:UntagResource"},
            "ManageQaRoles": {
                "iam:ListAttachedRolePolicies",
                "iam:ListRolePolicies",
                "iam:UpdateRole",
                "iam:UpdateRoleDescription",
            },
            "ManageQaEcs": {"ecs:DeleteCluster", "ecs:ListTagsForResource", "ecs:UntagResource"},
            "ManageQaQueueLogsTrailAndNetwork": {
                "sqs:GetQueueUrl",
                "sqs:ListQueueTags",
                "sqs:UntagQueue",
                "logs:DescribeIndexPolicies",
                "logs:UntagResource",
                "cloudtrail:DeleteTrail",
                "cloudtrail:GetEventSelectors",
                "cloudtrail:GetTrail",
                "cloudtrail:ListTags",
                "cloudtrail:PutEventSelectors",
                "cloudtrail:RemoveTags",
                "cloudtrail:StopLogging",
            },
        }
        for sid, actions in expected.items():
            with self.subTest(sid=sid):
                self.assertTrue(actions <= statements[sid], actions - statements[sid])

        manage_roles = next(
            statement
            for policy in policies
            for statement in policy["PolicyDocument"]["Statement"]
            if statement["Sid"] == "ManageQaRoles"
        )
        self.assertEqual(
            manage_roles["Resource"],
            "arn:${AWS::Partition}:iam::${AWS::AccountId}:role/tokenkey-prod-qa-raw-arch*",
        )

        service_linked_role = next(
            statement
            for policy in policies
            for statement in policy["PolicyDocument"]["Statement"]
            if statement["Sid"] == "CreateEcsServiceLinkedRole"
        )
        self.assertEqual(service_linked_role["Action"], "iam:CreateServiceLinkedRole")
        self.assertEqual(service_linked_role["Resource"], "*")
        self.assertEqual(
            service_linked_role["Condition"],
            {"StringEquals": {"iam:AWSServiceName": "ecs.amazonaws.com"}},
        )

    def test_qa_bundle_verifier_roles_have_scoped_bucket_readback(self) -> None:
        oidc = _load_oidc_template()
        resources = oidc["Resources"]
        expected_actions = {
            "s3:GetBucketCORS",
            "s3:GetEncryptionConfiguration",
            "s3:GetLifecycleConfiguration",
            "s3:ListBucket",
        }
        expected_resource = "arn:${AWS::Partition}:s3:::tokenkey-prod-qa-bundles-${AWS::AccountId}"
        for role_name in ("ClusteringRole", "QAInfraDeploymentRole"):
            with self.subTest(role_name=role_name):
                policies = resources[role_name]["Properties"]["Policies"]
                statement = next(
                    statement
                    for policy in policies
                    for statement in policy["PolicyDocument"]["Statement"]
                    if isinstance(statement, dict) and statement.get("Sid") == "ReadQaBundleBucketState"
                )
                self.assertEqual(set(statement["Action"]), expected_actions)
                self.assertEqual(statement["Resource"], expected_resource)

    def test_qa_verifier_roles_can_read_running_worker_tasks(self) -> None:
        oidc = _load_oidc_template()
        expected = {
            "ecs:DescribeServices",
            "ecs:DescribeTasks",
            "ecs:ListTasks",
            "sqs:GetQueueAttributes",
        }
        for role_name, sid in (
            ("ClusteringRole", "ReadQaBundleRuntimeForDiagnostics"),
            ("QAInfraDeploymentRole", "VerifyQaBundleRuntime"),
        ):
            with self.subTest(role_name=role_name):
                policies = oidc["Resources"][role_name]["Properties"]["Policies"]
                statement = next(
                    statement
                    for policy in policies
                    for statement in policy["PolicyDocument"]["Statement"]
                    if isinstance(statement, dict) and statement.get("Sid") == sid
                )
                self.assertEqual(set(statement["Action"]), expected)

    def assert_worker_contract(self, template: dict) -> None:
        parameters = template["Parameters"]
        self.assertEqual(parameters["BundleWorkerDesiredCount"]["Default"], 1)
        self.assertNotIn("BundleWorkerRepositoryCredentialsSecretArn", parameters)
        self.assertNotIn("Default", parameters["BundleWorkerImage"])
        resources = template["Resources"]
        task = resources["QaBundleWorkerTaskDefinition"]["Properties"]
        container = task["ContainerDefinitions"][0]
        self.assertNotIn("RepositoryCredentials", container)
        self.assertEqual(container["Command"], ["--qa-bundle-worker"])
        environment = {item["Name"]: item["Value"] for item in container["Environment"]}
        self.assertEqual(environment, {
            "TZ": "UTC",
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
        self.assertNotIn("Policies", execution)

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

    def test_active_runtime_has_no_legacy_export_alias(self) -> None:
        for surface in ACTIVE_RUNTIME_SURFACES:
            with self.subTest(surface=surface):
                self.assertNotIn("QA_CAPTURE_EXPORT_STORAGE", surface.read_text(encoding="utf-8"))

    def test_backups_template_is_ascii_for_cloudformation_round_trip(self) -> None:
        backups_text = (
            ROOT / "deploy/aws/cloudformation/stage0-backups.yaml"
        ).read_text(encoding="utf-8")
        self.assertTrue(backups_text.isascii())

    def test_backups_template_has_no_legacy_export_owner(self) -> None:
        backups = yaml.load(
            (ROOT / "deploy/aws/cloudformation/stage0-backups.yaml").read_text(encoding="utf-8"),
            Loader=CloudFormationLoader,
        )
        self.assertNotIn("QaExportsRetentionDays", backups["Parameters"])
        self.assertNotIn("QaExportsBucket", backups["Resources"])
        self.assertNotIn("QaExportsBucketPolicy", backups["Resources"])
        self.assertNotIn("QaExportsBucketName", backups["Outputs"])
        self.assertNotIn("QaExportsS3Uri", backups["Outputs"])

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
                    "QA_BUNDLE_WORKER_IMAGE": "ghcr.io/youxuanxue/sub2api:1.8.99",
                    "QA_BUNDLE_BROWSER_ALLOWED_ORIGIN": "https://api.tokenkey.dev",
                }, capture_output=True, text=True, check=False,
            )
            self.assertEqual(proc.returncode, 91, msg=proc.stderr)
            calls = call_log.read_text(encoding="utf-8")
            self.assertIn('"ParameterKey":"BundleWorkerPublicSubnetIds","ParameterValue":"subnet-1111aaaa,subnet-2222bbbb"', calls, msg=proc.stdout + proc.stderr + calls)
            self.assertNotIn("BundleWorkerRepositoryCredentialsSecretArn", calls)
            self.assertIn('"ParameterKey":"BundleWorkerImage","ParameterValue":"ghcr.io/youxuanxue/sub2api:1.8.99"', calls, msg=proc.stdout + proc.stderr + calls)
            self.assertIn('"ParameterKey":"BundleWorkerDesiredCount","ParameterValue":"1"', calls, msg=proc.stdout + proc.stderr + calls)
            self.assertIn('"ParameterKey":"BundleBrowserAllowedOrigin","ParameterValue":"https://api.tokenkey.dev"', calls, msg=proc.stdout + proc.stderr + calls)
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
            "QA_BUNDLE_BROWSER_ALLOWED_ORIGIN": "https://api.tokenkey.dev",
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
