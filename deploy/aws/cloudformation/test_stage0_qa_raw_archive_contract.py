#!/usr/bin/env python3
from __future__ import annotations

import copy
import pathlib
import subprocess
import tempfile
import unittest

import yaml


ROOT = pathlib.Path(__file__).resolve().parents[3]
TEMPLATE = pathlib.Path(__file__).with_name("stage0-qa-raw-archive.yaml")


class CloudFormationLoader(yaml.SafeLoader):
    pass


def _cloudformation_tag(loader: CloudFormationLoader, _suffix: str, node: yaml.Node):
    if isinstance(node, yaml.ScalarNode):
        return loader.construct_scalar(node)
    if isinstance(node, yaml.SequenceNode):
        return loader.construct_sequence(node)
    return loader.construct_mapping(node)


CloudFormationLoader.add_multi_constructor("!", _cloudformation_tag)


class Stage0QARawArchiveContractTest(unittest.TestCase):
    def setUp(self) -> None:
        self.template = yaml.load(TEMPLATE.read_text(encoding="utf-8"), Loader=CloudFormationLoader)

    def test_us045_app_role_has_suffix_scoped_access_without_list_or_partial_reads(self) -> None:
        self.assert_app_policy_contract(self.template)

    def assert_app_policy_contract(self, template: dict[str, object]) -> None:
        statements = template["Resources"]["QaRawArchiveBucketPolicy"]["Properties"]["PolicyDocument"]["Statement"]
        app = {statement["Sid"]: statement for statement in statements if "AppInstanceRole" in statement["Sid"]}
        self.assertEqual(set(app), {"AllowAppInstanceRoleWriteRaw", "AllowAppInstanceRoleVerifyRaw"})
        self.assertEqual(
            app["AllowAppInstanceRoleWriteRaw"]["Action"],
            ["s3:PutObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"],
        )
        self.assertEqual(app["AllowAppInstanceRoleVerifyRaw"]["Action"], "s3:GetObject")
        expected_suffixes = [
            "commit.json", "manifest.json", "records.parquet", "evidence.pack",
            "evidence-index.jsonl.zst", "orphan-evidence-index.jsonl.zst",
        ]
        for statement in app.values():
            resources = statement["Resource"]
            self.assertEqual([resource.rsplit("/", 1)[-1] for resource in resources], expected_suffixes)
            self.assertTrue(all("raw/v1/date=*/hour=*" in resource for resource in resources))
            self.assertFalse(any("raw/partial/" in resource for resource in resources))

    def test_us045_recovery_role_is_nonempty_read_only_and_audited(self) -> None:
        self.assert_recovery_policy_contract(self.template)

    def assert_recovery_policy_contract(self, template: dict[str, object]) -> None:
        role = template["Resources"]["QaRawArchiveRecoveryRole"]["Properties"]
        self.assertNotIn("RoleName", role, "naming the existing role would force replacement")
        trust = role["AssumeRolePolicyDocument"]["Statement"][0]
        self.assertEqual(trust["Principal"]["AWS"], "OpsRecoveryPrincipalArn")
        policy = {statement["Sid"]: statement for statement in role["Policies"][0]["PolicyDocument"]["Statement"]}
        self.assertEqual(set(policy), {"ListRawPrefix", "ReadRawObjects"})
        self.assertEqual(policy["ListRawPrefix"]["Action"], "s3:ListBucket")
        self.assertEqual(policy["ReadRawObjects"]["Action"], "s3:GetObject")
        key_statements = {
            statement["Sid"]: statement
            for statement in template["Resources"]["QaRawArchiveKey"]["Properties"]["KeyPolicy"]["Statement"]
        }
        key_read = key_statements["AllowOpsRecoveryRoleReadViaS3"]
        self.assertEqual(key_read["Principal"], {"AWS": "QaRawArchiveRecoveryRole.Arn"})
        self.assertEqual(key_read["Action"], ["kms:Decrypt", "kms:DescribeKey"])
        self.assertEqual(key_read["Resource"], "*")
        self.assertEqual(
            key_read["Condition"]["StringEquals"],
            {"kms:ViaService": "s3.${AWS::Region}.amazonaws.com"},
        )
        self.assertEqual(
            key_read["Condition"]["StringLike"]["kms:EncryptionContext:aws:s3:arn"],
            [
                "arn:${AWS::Partition}:s3:::${ProjectName}-${Environment}-qa-raw-archive-${AWS::AccountId}",
                "arn:${AWS::Partition}:s3:::${ProjectName}-${Environment}-qa-raw-archive-${AWS::AccountId}/raw/*",
            ],
        )
        bucket_arn = "arn:${AWS::Partition}:s3:::${ProjectName}-${Environment}-qa-raw-archive-${AWS::AccountId}"
        self.assertEqual(policy["ListRawPrefix"]["Resource"], bucket_arn)
        self.assertEqual(policy["ListRawPrefix"]["Condition"], {"StringLike": {"s3:prefix": ["raw", "raw/*"]}})
        self.assertEqual(policy["ReadRawObjects"]["Resource"], bucket_arn + "/raw/*")
        resources = template["Resources"]
        bucket_policy = {
            statement["Sid"]: statement
            for statement in resources["QaRawArchiveBucketPolicy"]["Properties"]["PolicyDocument"]["Statement"]
            if "OpsRecoveryRole" in statement["Sid"]
        }
        self.assertEqual(set(bucket_policy), {"AllowOpsRecoveryRoleReadRaw", "AllowOpsRecoveryRoleListRawPrefix"})
        self.assertEqual(bucket_policy["AllowOpsRecoveryRoleReadRaw"]["Action"], "s3:GetObject")
        self.assertEqual(bucket_policy["AllowOpsRecoveryRoleReadRaw"]["Resource"], "${QaRawArchiveBucket.Arn}/raw/*")
        self.assertEqual(bucket_policy["AllowOpsRecoveryRoleListRawPrefix"]["Action"], "s3:ListBucket")
        self.assertEqual(bucket_policy["AllowOpsRecoveryRoleListRawPrefix"]["Resource"], "QaRawArchiveBucket.Arn")
        self.assertEqual(
            bucket_policy["AllowOpsRecoveryRoleListRawPrefix"]["Condition"],
            {"StringLike": {"s3:prefix": ["raw", "raw/*"]}},
        )
        self.assertEqual(resources["QaRawArchiveS3Endpoint"]["Properties"]["VpcEndpointType"], "Gateway")
        trail = resources["QaRawArchiveDataTrail"]["Properties"]
        self.assertTrue(trail["IsLogging"])
        self.assertEqual(trail["EventSelectors"][0]["DataResources"][0]["Type"], "AWS::S3::Object")

    def test_us045_structured_contract_rejects_broadened_actions_resources_and_removed_conditions(self) -> None:
        broadened_app = copy.deepcopy(self.template)
        app_statements = broadened_app["Resources"]["QaRawArchiveBucketPolicy"]["Properties"]["PolicyDocument"]["Statement"]
        next(item for item in app_statements if item["Sid"] == "AllowAppInstanceRoleVerifyRaw")["Action"] = "s3:*"
        with self.assertRaises(AssertionError):
            self.assert_app_policy_contract(broadened_app)

        broadened_recovery = copy.deepcopy(self.template)
        recovery_statements = broadened_recovery["Resources"]["QaRawArchiveKey"]["Properties"]["KeyPolicy"]["Statement"]
        decrypt = next(item for item in recovery_statements if item["Sid"] == "AllowOpsRecoveryRoleReadViaS3")
        decrypt["Action"] = "kms:*"
        decrypt.pop("Condition")
        with self.assertRaises(AssertionError):
            self.assert_recovery_policy_contract(broadened_recovery)

    def test_us045_deploy_renders_exact_security_binding_and_shared_role_boundary(self) -> None:
        script = ROOT / "ops/qa/deploy_qa_raw_archive_cfn.sh"
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_bin = pathlib.Path(temp_dir) / "bin"
            fake_bin.mkdir()
            fake_aws = fake_bin / "aws"
            fake_aws.write_text(
                """#!/usr/bin/env bash
if [[ "$*" == *"sts get-caller-identity"* ]]; then echo 123456789012; exit 0; fi
if [[ "$*" == *"cloudformation describe-stacks"* && "$*" == *"QaRawArchiveRecoveryRoleArn"* ]]; then
  echo arn:aws:iam::123456789012:role/generated-existing-role; exit 0
fi
if [[ "$*" == *"cloudformation describe-stacks"* ]]; then exit 0; fi
if [[ "$*" == *"cloudformation create-change-set"* && "$*" == *"CAPABILITY_NAMED_IAM"* ]]; then exit 92; fi
if [[ "$*" == *"cloudformation create-change-set"* && "$*" == *"--capabilities CAPABILITY_IAM"* ]]; then exit 91; fi
if [[ "$*" == *"cloudformation create-change-set"* ]]; then exit 93; fi
exit 91
""",
                encoding="utf-8",
            )
            fake_aws.chmod(0o755)
            proc = subprocess.run(
                ["bash", str(script)],
                env={
                    "PATH": f"{fake_bin}:/usr/bin:/bin",
                    "APP_INSTANCE_ROLE_ARN": "arn:aws:iam::123456789012:role/stage0-shared",
                    "OPS_RECOVERY_PRINCIPAL_ARN": "arn:aws:iam::123456789012:user/qa-operator",
                    "QA_RAW_ARCHIVE_VPC_ID": "vpc-1234abcd",
                    "QA_RAW_ARCHIVE_ROUTE_TABLE_IDS": "rtb-1111aaaa,rtb-2222bbbb",
                    "QA_BUNDLE_WORKER_PUBLIC_SUBNET_IDS": "subnet-1111aaaa,subnet-2222bbbb",
                    "QA_BUNDLE_WORKER_REPOSITORY_CREDENTIALS_SECRET_ARN": "arn:aws:secretsmanager:us-east-1:123456789012:secret:ghcr-pull",
                    "QA_BUNDLE_WORKER_IMAGE": "ghcr.io/youxuanxue/sub2api:1.8.99",
                    "QA_BUNDLE_BROWSER_ALLOWED_ORIGIN": "https://api.tokenkey.dev",
                },
                capture_output=True,
                text=True,
                check=False,
            )
        self.assertEqual(proc.returncode, 91)
        output = proc.stdout
        for value in (
            "app_role=arn:aws:iam::123456789012:role/stage0-shared",
            "recovery_role=arn:aws:iam::123456789012:role/generated-existing-role",
            "vpc=vpc-1234abcd route_tables=rtb-1111aaaa,rtb-2222bbbb",
            "raw_bucket=tokenkey-prod-qa-raw-archive-123456789012",
            "kms_alias=alias/tokenkey-prod-qa-raw-archive",
            "audit_bucket=tokenkey-prod-qa-raw-audit-123456789012",
            "trail=tokenkey-prod-qa-raw-data-events",
            "iam_boundary=shared_ec2_instance_role_no_process_isolation",
        ):
            self.assertIn(value, output)


if __name__ == "__main__":
    unittest.main()
