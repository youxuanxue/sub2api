#!/usr/bin/env python3
from __future__ import annotations

import copy
import json
import pathlib
import unittest

import yaml


ROOT = pathlib.Path(__file__).resolve().parents[3]
RAW_TEMPLATE = pathlib.Path(__file__).with_name("stage0-qa-raw-archive.yaml")
BACKUPS_TEMPLATE = pathlib.Path(__file__).with_name("stage0-backups.yaml")
ADDON_TEMPLATE = pathlib.Path(__file__).with_name("cicd-oidc-lightsail-addon.yaml")
EDGE_TARGETS = ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json"
PROVISION_SCRIPT = ROOT / "deploy/aws/lightsail/provision-edge.sh"
EDGE_WORKFLOW = ROOT / ".github/workflows/deploy-edge-lightsail-stage0.yml"

EDGE_ROLE_NAME = "tokenkey-lightsail-ssm-hybrid"
EDGE_ROLE_ARN = (
    "arn:${AWS::Partition}:iam::${AWS::AccountId}:role/" + EDGE_ROLE_NAME
)
EDGE_ROLE_PREFIX = EDGE_ROLE_NAME + "-"
EDGE_DENY_SID = "DenyLightsailEdgeRole"


class CloudFormationLoader(yaml.SafeLoader):
    pass


def _cloudformation_tag(loader: CloudFormationLoader, _suffix: str, node: yaml.Node):
    if isinstance(node, yaml.ScalarNode):
        return loader.construct_scalar(node)
    if isinstance(node, yaml.SequenceNode):
        return loader.construct_sequence(node)
    return loader.construct_mapping(node)


CloudFormationLoader.add_multi_constructor("!", _cloudformation_tag)


def _load_template(path: pathlib.Path) -> dict:
    return yaml.load(path.read_text(encoding="utf-8"), Loader=CloudFormationLoader)


def _statements(template: dict, policy: str) -> list[dict]:
    return template["Resources"][policy]["Properties"]["PolicyDocument"]["Statement"]


class Stage0EdgeQaS3BoundaryTest(unittest.TestCase):
    def setUp(self) -> None:
        self.raw = _load_template(RAW_TEMPLATE)
        self.backups = _load_template(BACKUPS_TEMPLATE)
        self.addon = _load_template(ADDON_TEMPLATE)

    def deployable_edges(self) -> dict[str, dict]:
        matrix = json.loads(EDGE_TARGETS.read_text(encoding="utf-8"))
        deployable = {
            edge_id: target
            for edge_id, target in matrix["targets"].items()
            if target["deployable"] is True
        }
        self.assertTrue(deployable)
        self.assertTrue(
            all(target["profile"] == matrix["default_profile"] for target in deployable.values())
        )
        return deployable

    def test_deployable_fleet_has_one_isolated_ssm_hybrid_role_per_edge(self) -> None:
        deployable = self.deployable_edges()
        roles = {
            resource["Properties"]["RoleName"]: resource["Properties"]
            for resource in self.addon["Resources"].values()
            if resource.get("Type") == "AWS::IAM::Role"
        }
        expected_names = {EDGE_ROLE_NAME} | {
            f"{EDGE_ROLE_PREFIX}{edge_id}" for edge_id in deployable
        }
        self.assertEqual(set(roles), expected_names)

        shared = roles[EDGE_ROLE_NAME]
        self.assertEqual(
            shared["ManagedPolicyArns"],
            ["arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"],
        )
        self.assertNotIn("Policies", shared)

        for edge_id in deployable:
            with self.subTest(edge_id=edge_id):
                role = roles[f"{EDGE_ROLE_PREFIX}{edge_id}"]
                self.assertEqual(
                    role["ManagedPolicyArns"],
                    ["arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"],
                )
                self.assertEqual(
                    role["Policies"],
                    [
                        {
                            "PolicyName": "EdgeRecoveryData",
                            "PolicyDocument": {
                                "Version": "2012-10-17",
                                "Statement": [
                                    {
                                        "Sid": "ReadWriteOwnEnvSecrets",
                                        "Effect": "Allow",
                                        "Action": ["ssm:GetParameter", "ssm:PutParameter"],
                                        "Resource": (
                                            "arn:${AWS::Partition}:ssm:*:${AWS::AccountId}:"
                                            f"parameter/tokenkey/edge/{edge_id}/stage0/env-secrets-backup"
                                        ),
                                    },
                                    {
                                        "Sid": "ReadWriteOwnPgdumpObjects",
                                        "Effect": "Allow",
                                        "Action": ["s3:GetObject", "s3:PutObject"],
                                        "Resource": (
                                            "arn:${AWS::Partition}:s3:::tokenkey-prod-pgdump-"
                                            f"${{AWS::AccountId}}/edge/{edge_id}/pgdump/*"
                                        ),
                                    },
                                    {
                                        "Sid": "ListOwnPgdumpPrefix",
                                        "Effect": "Allow",
                                        "Action": "s3:ListBucket",
                                        "Resource": (
                                            "arn:${AWS::Partition}:s3:::tokenkey-prod-pgdump-"
                                            "${AWS::AccountId}"
                                        ),
                                        "Condition": {
                                            "StringLike": {
                                                "s3:prefix": [
                                                    f"edge/{edge_id}/pgdump",
                                                    f"edge/{edge_id}/pgdump/*",
                                                ]
                                            }
                                        },
                                    },
                                ],
                            },
                        }
                    ],
                )

        addon_statements = self.addon["Resources"]["LightsailEdgeAddonPolicy"][
            "Properties"
        ]["PolicyDocument"]["Statement"]
        pass_role = next(
            item for item in addon_statements if item.get("Sid") == "PassSsmHybridRoleToActivation"
        )
        self.assertEqual(
            pass_role["Resource"],
            "arn:${AWS::Partition}:iam::${AWS::AccountId}:role/tokenkey-lightsail-ssm-hybrid-*",
        )
        managed = next(
            item for item in addon_statements if item.get("Sid") == "SsmManagedInstanceCommand"
        )
        self.assertIn("ssm:UpdateManagedInstanceRole", managed["Action"])

        provision = PROVISION_SCRIPT.read_text(encoding="utf-8")
        self.assertIn('--iam-role "$SSM_HYBRID_ROLE_NAME"', provision)

    def assert_qa_bucket_deny(
        self,
        template: dict,
        policy_name: str,
        bucket_arn: str,
        object_arn: str,
    ) -> None:
        statements = _statements(template, policy_name)
        deny = [item for item in statements if item.get("Sid") == EDGE_DENY_SID]
        self.assertEqual(len(deny), 1)
        self.assertEqual(
            deny[0],
            {
                "Sid": EDGE_DENY_SID,
                "Effect": "Deny",
                "Principal": "*",
                "Action": "s3:*",
                "Resource": [bucket_arn, object_arn],
                "Condition": {
                    "ArnLike": {
                        "aws:PrincipalArn": [EDGE_ROLE_ARN, EDGE_ROLE_ARN + "-*"]
                    }
                },
            },
        )
        for statement in statements:
            if statement.get("Effect") != "Allow":
                continue
            self.assertNotIn(EDGE_ROLE_NAME, json.dumps(statement, sort_keys=True))

    def test_both_qa_bucket_policies_deny_shared_and_per_edge_roles(self) -> None:
        cases = (
            (
                self.raw,
                "QaRawArchiveBucketPolicy",
                "QaRawArchiveBucket.Arn",
                "${QaRawArchiveBucket.Arn}/*",
            ),
            (
                self.raw,
                "QaBundleBucketPolicy",
                "QaBundleBucket.Arn",
                "${QaBundleBucket.Arn}/*",
            ),
        )
        for template, policy_name, bucket_arn, object_arn in cases:
            with self.subTest(policy=policy_name):
                self.assert_qa_bucket_deny(
                    template, policy_name, bucket_arn, object_arn
                )

    def test_edge_deny_does_not_expand_to_non_qa_bucket_or_kms_policies(self) -> None:
        for policy_name in ("PgdumpBackupBucketPolicy", "MediaBucketPolicy"):
            statements = _statements(self.backups, policy_name)
            self.assertFalse(any(item.get("Sid") == EDGE_DENY_SID for item in statements))
            self.assertNotIn(EDGE_ROLE_NAME, json.dumps(statements, sort_keys=True))

        key_policy = self.raw["Resources"]["QaRawArchiveKey"]["Properties"]["KeyPolicy"]
        audit_policy = _statements(self.raw, "QaRawArchiveAuditBucketPolicy")
        self.assertNotIn(EDGE_ROLE_NAME, json.dumps(key_policy, sort_keys=True))
        self.assertNotIn(EDGE_ROLE_NAME, json.dumps(audit_policy, sort_keys=True))

    def test_contract_rejects_condition_or_resource_broadening(self) -> None:
        broadened = copy.deepcopy(self.raw)
        statements = _statements(broadened, "QaRawArchiveBucketPolicy")
        deny = next(item for item in statements if item.get("Sid") == EDGE_DENY_SID)
        deny["Condition"] = {
            "ArnLike": {
                "aws:PrincipalArn": [EDGE_ROLE_ARN, EDGE_ROLE_ARN + "-*"]
            }
        }
        deny["Condition"]["ArnLike"]["aws:PrincipalArn"] = "arn:aws:iam::*:role/*"
        deny["Resource"].append("arn:aws:s3:::*")

        with self.assertRaises(AssertionError):
            self.assert_qa_bucket_deny(
                broadened,
                "QaRawArchiveBucketPolicy",
                "QaRawArchiveBucket.Arn",
                "${QaRawArchiveBucket.Arn}/*",
            )

    def test_deploy_workflow_backs_up_to_the_resolved_edge_parameter(self) -> None:
        workflow = yaml.safe_load(EDGE_WORKFLOW.read_text(encoding="utf-8"))
        steps = workflow["jobs"]["edge"]["steps"]
        step = next(item for item in steps if item.get("name") == "Backup Edge env secrets off-box")
        self.assertEqual(step["env"]["TK_ENV_SECRETS_SOURCE"], "/var/lib/tokenkey/.env.secret")
        self.assertEqual(
            step["env"]["TK_ENV_SECRETS_PARAM"],
            "/tokenkey/edge/${{ steps.edge.outputs.edge_id }}/stage0/env-secrets-backup",
        )
        self.assertIn("backup-env-secrets-via-ssm.sh", step["run"])


if __name__ == "__main__":
    unittest.main()
