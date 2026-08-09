#!/usr/bin/env python3
from __future__ import annotations

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
        statements = self.template["Resources"]["QaRawArchiveBucketPolicy"]["Properties"]["PolicyDocument"]["Statement"]
        app = [statement for statement in statements if "AppInstanceRole" in statement["Sid"]]
        actions = {action for statement in app for action in ([statement["Action"]] if isinstance(statement["Action"], str) else statement["Action"])}
        resources = [resource for statement in app for resource in ([statement["Resource"]] if isinstance(statement["Resource"], str) else statement["Resource"])]

        self.assertNotIn("s3:ListBucket", actions)
        self.assertNotIn("s3:DeleteObject", actions)
        self.assertFalse(any("raw/partial/" in resource for resource in resources))
        get_resources = [
            resource
            for statement in app
            if "s3:GetObject" in ([statement["Action"]] if isinstance(statement["Action"], str) else statement["Action"])
            for resource in ([statement["Resource"]] if isinstance(statement["Resource"], str) else statement["Resource"])
        ]
        expected_suffixes = {
            "commit.json", "manifest.json", "records.parquet", "evidence.pack",
            "evidence-index.jsonl.zst", "orphan-evidence-index.jsonl.zst",
        }
        self.assertEqual({resource.rsplit("/", 1)[-1] for resource in get_resources}, expected_suffixes)
        self.assertTrue(all("raw/v1/date=*/hour=*" in resource for resource in get_resources))

    def test_us045_recovery_role_is_nonempty_read_only_and_audited(self) -> None:
        role = self.template["Resources"]["QaRawArchiveRecoveryRole"]["Properties"]
        self.assertEqual(role["RoleName"], "${ProjectName}-${Environment}-qa-raw-recovery")
        trust = role["AssumeRolePolicyDocument"]["Statement"][0]
        self.assertEqual(trust["Principal"]["AWS"], "OpsRecoveryPrincipalArn")
        policy = role["Policies"][0]["PolicyDocument"]["Statement"]
        actions = {action for statement in policy for action in ([statement["Action"]] if isinstance(statement["Action"], str) else statement["Action"])}
        self.assertIn("s3:ListBucket", actions)
        self.assertIn("s3:GetObject", actions)
        self.assertIn("kms:Decrypt", actions)
        self.assertNotIn("s3:PutObject", actions)
        self.assertNotIn("s3:DeleteObject", actions)
        resources = self.template["Resources"]
        self.assertEqual(resources["QaRawArchiveS3Endpoint"]["Properties"]["VpcEndpointType"], "Gateway")
        trail = resources["QaRawArchiveDataTrail"]["Properties"]
        self.assertTrue(trail["IsLogging"])
        self.assertEqual(trail["EventSelectors"][0]["DataResources"][0]["Type"], "AWS::S3::Object")

    def test_us045_deploy_renders_exact_security_binding_and_shared_role_boundary(self) -> None:
        script = ROOT / "ops/qa/deploy_qa_raw_archive_cfn.sh"
        self.assertIn("CAPABILITY_NAMED_IAM", script.read_text(encoding="utf-8"))
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_bin = pathlib.Path(temp_dir) / "bin"
            fake_bin.mkdir()
            fake_aws = fake_bin / "aws"
            fake_aws.write_text(
                """#!/usr/bin/env bash
if [[ "$*" == *"sts get-caller-identity"* ]]; then echo 123456789012; exit 0; fi
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
                },
                capture_output=True,
                text=True,
                check=False,
            )
        self.assertEqual(proc.returncode, 91)
        output = proc.stdout
        for value in (
            "app_role=arn:aws:iam::123456789012:role/stage0-shared",
            "recovery_role=arn:aws:iam::123456789012:role/tokenkey-prod-qa-raw-recovery",
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
