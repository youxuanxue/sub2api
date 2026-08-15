from __future__ import annotations

import copy
import importlib.util
import pathlib
import unittest


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts/checks/ec2-edge-oidc-perm-coverage.py"
EDGE_CFN = REPO_ROOT / "deploy/aws/cloudformation/stage0-edge-ec2.yaml"


def load_module():
    spec = importlib.util.spec_from_file_location("ec2_edge_oidc", SCRIPT)
    if spec is None or spec.loader is None:
        raise AssertionError("cannot load EC2 Edge OIDC checker")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def remove_token(value: object, token: str) -> object:
    if isinstance(value, dict):
        return {key: remove_token(item, token) for key, item in value.items()}
    if isinstance(value, list):
        return [remove_token(item, token) for item in value if item != token]
    if isinstance(value, str) and token in value:
        return value.replace(token, "removed-by-test")
    return value


class Ec2EdgeOidcPermCoverageTest(unittest.TestCase):
    def setUp(self) -> None:
        self.assertTrue(SCRIPT.is_file(), "EC2 Edge OIDC checker is not implemented")
        self.mod = load_module()
        self.addon = self.mod.load_cfn(self.mod.ADDON_CFN)
        self.base = self.mod.load_cfn(self.mod.BASE_CFN)
        self.edge = self.mod.load_cfn(self.mod.EDGE_CFN)

    def assert_required(self, token: str) -> None:
        failures = self.mod.validate_contract(remove_token(copy.deepcopy(self.addon), token), self.base, self.edge)
        self.assertTrue(any(token in failure for failure in failures), failures)

    def test_required_provision_and_runtime_permissions_are_covered(self) -> None:
        for action in (
            "cloudformation:CreateChangeSet",
            "ec2:RunInstances",
            "ec2:ModifyInstanceCreditSpecification",
            "ec2:AllocateAddress",
            "iam:PassRole",
            "ssm:SendCommand",
        ):
            with self.subTest(action=action):
                self.assert_required(action)

    def test_both_edge_regions_and_stack_scope_are_required(self) -> None:
        for token in ("us-east-2", "us-west-2", "tokenkey-edge-*-stage0"):
            with self.subTest(token=token):
                self.assert_required(token)

    def test_unapproved_identity_or_fleet_action_is_rejected(self) -> None:
        for action in (
            "iam:CreateUser",
            "lightsail:DeleteInstance",
            "cloudformation:DeleteStack",
            "ec2:CreateSnapshot",
            "ec2:ReleaseAddress",
            "lightsail:GetInstanceMetricData",
            "servicequotas:GetServiceQuota",
        ):
            with self.subTest(action=action):
                mutated = copy.deepcopy(self.addon)
                policy = mutated["Resources"]["Ec2EdgeAddonPolicy"]["Properties"]["PolicyDocument"]
                existing = policy["Statement"][0]["Action"]
                policy["Statement"][0]["Action"] = self.mod._as_list(existing) + [action]
                failures = self.mod.validate_contract(mutated, self.base, self.edge)
                self.assertTrue(any(action in failure for failure in failures), failures)

    def test_real_caller_policy_has_no_retirement_or_cost_actions(self) -> None:
        policy = self.addon["Resources"]["Ec2EdgeAddonPolicy"]["Properties"]["PolicyDocument"]
        actual = self.mod._actions(policy)
        forbidden = {
            "cloudformation:DeleteStack",
            "ec2:CreateSnapshot",
            "ec2:ReleaseAddress",
            "lightsail:GetInstanceMetricData",
            "servicequotas:GetServiceQuota",
        }
        self.assertEqual(set(), forbidden & actual)

    def test_cloudformation_mutation_cannot_widen_to_all_stacks(self) -> None:
        mutated = copy.deepcopy(self.addon)
        statements = mutated["Resources"]["Ec2EdgeAddonPolicy"]["Properties"]["PolicyDocument"]["Statement"]
        target = next(item for item in statements if "cloudformation:CreateChangeSet" in self.mod._actions({"Statement": [item]}))
        target["Resource"] = "*"
        failures = self.mod.validate_contract(mutated, self.base, self.edge)
        self.assertTrue(any("stack scope" in failure for failure in failures), failures)

    def test_cloudformation_mutation_requires_the_fixed_execution_role(self) -> None:
        mutated = copy.deepcopy(self.addon)
        statements = mutated["Resources"]["Ec2EdgeAddonPolicy"]["Properties"]["PolicyDocument"]["Statement"]
        target = next(item for item in statements if "cloudformation:CreateChangeSet" in item.get("Action", []))
        target["Condition"]["StringEquals"]["cloudformation:RoleArn"] = "*"

        failures = self.mod.validate_contract(mutated, self.base, self.edge)

        self.assertTrue(any("fixed execution role" in failure for failure in failures), failures)

    def test_cloudformation_followup_actions_cannot_inherit_role_arn_condition(self) -> None:
        mutated = copy.deepcopy(self.addon)
        statements = mutated["Resources"]["Ec2EdgeAddonPolicy"]["Properties"]["PolicyDocument"]["Statement"]
        followup = next(item for item in statements if "cloudformation:ExecuteChangeSet" in item.get("Action", []))
        followup.setdefault("Condition", {}).setdefault("StringEquals", {})[
            "cloudformation:RoleArn"
        ] = "arn:unsupported-on-this-action"

        failures = self.mod.validate_contract(mutated, self.base, self.edge)

        self.assertTrue(any("unsupported RoleArn condition" in failure for failure in failures), failures)

    def test_run_instances_dependencies_include_implicit_network_interface(self) -> None:
        mutated = copy.deepcopy(self.addon)
        execution = mutated["Resources"]["Ec2EdgeCloudFormationExecutionRole"]
        statements = execution["Properties"]["Policies"][0]["PolicyDocument"]["Statement"]
        dependencies = next(item for item in statements if item.get("Sid") == "UseEc2EdgeLaunchDependencies")
        dependencies["Resource"] = [
            resource for resource in dependencies["Resource"]
            if ":network-interface/" not in str(resource)
        ]

        failures = self.mod.validate_contract(mutated, self.base, self.edge)

        self.assertTrue(any("network-interface" in failure for failure in failures), failures)

    def test_cloudwatch_mutations_are_limited_to_edge_alarm_names(self) -> None:
        mutated = copy.deepcopy(self.addon)
        execution = mutated["Resources"]["Ec2EdgeCloudFormationExecutionRole"]
        statements = execution["Properties"]["Policies"][0]["PolicyDocument"]["Statement"]
        alarm = next(item for item in statements if item.get("Sid") == "ManageEdgeAlarms")
        alarm["Resource"] = "*"

        failures = self.mod.validate_contract(mutated, self.base, self.edge)

        self.assertTrue(any("alarm scope" in failure for failure in failures), failures)

    def test_dlm_create_and_mutation_require_edge_tags(self) -> None:
        mutated = copy.deepcopy(self.addon)
        execution = mutated["Resources"]["Ec2EdgeCloudFormationExecutionRole"]
        statements = execution["Properties"]["Policies"][0]["PolicyDocument"]["Statement"]
        create = next(item for item in statements if item.get("Sid") == "CreateTaggedEdgeSnapshotPolicy")
        create.pop("Condition", None)
        manage = next(item for item in statements if item.get("Sid") == "ManageTaggedEdgeSnapshotPolicies")
        manage["Resource"] = "*"

        failures = self.mod.validate_contract(mutated, self.base, self.edge)

        self.assertTrue(any("DLM create tags" in failure for failure in failures), failures)
        self.assertTrue(any("DLM policy scope" in failure for failure in failures), failures)

    def test_ec2_edge_execution_role_requires_tag_scoped_resources(self) -> None:
        mutated = copy.deepcopy(self.addon)
        execution = mutated["Resources"]["Ec2EdgeCloudFormationExecutionRole"]
        statements = execution["Properties"]["Policies"][0]["PolicyDocument"]["Statement"]
        for statement in statements:
            if statement.get("Sid") in {"CreateTaggedEc2EdgeResources", "ManageTaggedEc2EdgeResources"}:
                statement.pop("Condition", None)

        failures = self.mod.validate_contract(mutated, self.base, self.edge)

        self.assertTrue(any("EC2 tag scope" in failure for failure in failures), failures)

    def test_edge_template_cannot_substitute_project_or_environment_tags(self) -> None:
        mutated = copy.deepcopy(self.edge)
        mutated["Parameters"]["ProjectName"]["AllowedValues"] = ["attacker-project"]
        mutated["Parameters"]["Environment"]["AllowedValues"] = ["attacker-environment"]

        failures = self.mod.validate_contract(self.addon, self.base, mutated)

        self.assertTrue(any("ProjectName" in failure for failure in failures), failures)
        self.assertTrue(any("Environment" in failure for failure in failures), failures)

    def test_every_ec2_resource_with_tags_carries_edge_scope(self) -> None:
        mutated = copy.deepcopy(self.edge)
        mutated["Resources"]["InternetGateway"]["Properties"]["Tags"] = [
            {"Key": "Name", "Value": "removed-by-test"}
        ]

        failures = self.mod.validate_contract(self.addon, self.base, mutated)

        self.assertTrue(any("InternetGateway" in failure and "Project" in failure for failure in failures), failures)

    def test_lightsail_source_commands_are_scoped_to_tagged_managed_instances(self) -> None:
        mutated = copy.deepcopy(self.addon)
        statements = mutated["Resources"]["Ec2EdgeAddonPolicy"]["Properties"]["PolicyDocument"]["Statement"]
        target = next(
            (item for item in statements if item.get("Sid") == "SendRunShellScriptToTokenkeyLightsailEdge"),
            None,
        )
        if target is not None:
            target["Resource"] = "*"

        failures = self.mod.validate_contract(mutated, self.base, self.edge)

        self.assertTrue(any("Lightsail managed-instance" in failure for failure in failures), failures)

    def test_ec2_commands_require_project_environment_and_edge_id_tags(self) -> None:
        for operator, key in (
            ("StringEquals", "ssm:resourceTag/Project"),
            ("StringEquals", "ssm:resourceTag/Environment"),
            ("StringLike", "ssm:resourceTag/EdgeId"),
        ):
            with self.subTest(key=key):
                mutated = copy.deepcopy(self.addon)
                statements = mutated["Resources"]["Ec2EdgeAddonPolicy"]["Properties"]["PolicyDocument"]["Statement"]
                target = next(item for item in statements if item.get("Sid") == "SendRunShellScriptToTokenkeyEdge")
                target["Condition"].get(operator, {}).pop(key, None)

                failures = self.mod.validate_contract(mutated, self.base, self.edge)

                self.assertTrue(any("EC2 instance SendCommand" in failure for failure in failures), failures)

    def test_candidate_eip_allocation_requires_edge_tags(self) -> None:
        mutated = copy.deepcopy(self.addon)
        statements = mutated["Resources"]["Ec2EdgeAddonPolicy"]["Properties"]["PolicyDocument"]["Statement"]
        target = next(item for item in statements if item.get("Sid") == "AllocateEdgeEip")
        target["Condition"] = {"StringEquals": {"aws:RequestedRegion": ["us-east-2", "us-west-2"]}}

        failures = self.mod.validate_contract(mutated, self.base, self.edge)

        self.assertTrue(any("EIP request tags" in failure for failure in failures), failures)

    def test_bootstrap_parameters_are_stack_owned_without_s3_caller_permissions(self) -> None:
        mutated = copy.deepcopy(self.addon)
        execution = mutated["Resources"]["Ec2EdgeCloudFormationExecutionRole"]
        statements = execution["Properties"]["Policies"][0]["PolicyDocument"]["Statement"]
        manager = next(item for item in statements if item.get("Sid") == "ManageEdgeBootstrapParameters")
        manager["Resource"] = "*"

        failures = self.mod.validate_contract(mutated, self.base, self.edge)

        self.assertTrue(any("bootstrap parameters" in failure for failure in failures), failures)
        self.assertFalse(
            any(action.startswith("s3:") for action in self.mod._actions(
                self.addon["Resources"]["Ec2EdgeAddonPolicy"]["Properties"]["PolicyDocument"]
            ))
        )
        self.assertFalse(
            any(resource.get("Type", "").startswith("AWS::S3::")
                for resource in self.addon["Resources"].values())
        )

    def test_dynamic_edge_roles_are_capped_by_a_fixed_permissions_boundary(self) -> None:
        for logical_id in ("Ec2EdgeInstanceBoundary", "Ec2EdgeDlmBoundary"):
            with self.subTest(boundary=logical_id):
                boundary = self.mod._resource(
                    self.addon, logical_id, "AWS::IAM::ManagedPolicy"
                )
                self.assertIsNotNone(boundary)
                boundary_actions = self.mod._actions(boundary["Properties"]["PolicyDocument"])
                self.assertFalse(
                    any(action.startswith(("iam:", "sts:", "s3:")) for action in boundary_actions)
                )

        execution = self.addon["Resources"]["Ec2EdgeCloudFormationExecutionRole"]
        statements = execution["Properties"]["Policies"][0]["PolicyDocument"]["Statement"]
        expected_creation = {
            "CreateBoundedEdgeInstanceRoles": {
                "suffix": "-instance",
                "boundary": "Ec2EdgeInstanceBoundary",
            },
            "CreateBoundedEdgeDlmRoles": {
                "suffix": "-dlm",
                "boundary": "Ec2EdgeDlmBoundary",
            },
        }
        for sid, expected in expected_creation.items():
            create_role = next(item for item in statements if item.get("Sid") == sid)
            self.assertTrue(str(create_role["Resource"]).endswith(expected["suffix"]))
            self.assertEqual(
                create_role["Condition"]["ArnEquals"]["iam:PermissionsBoundary"],
                expected["boundary"],
            )
        expected_pass = {
            "PassEdgeInstanceRole": ("-instance", "ec2.amazonaws.com"),
            "PassEdgeDlmRole": ("-dlm", "dlm.amazonaws.com"),
        }
        for sid, (suffix, service) in expected_pass.items():
            statement = next(item for item in statements if item.get("Sid") == sid)
            self.assertTrue(str(statement["Resource"]).endswith(suffix))
            self.assertEqual(
                statement["Condition"]["StringEquals"]["iam:PassedToService"],
                service,
            )
        execution_actions = self.mod._actions(execution["Properties"]["Policies"][0]["PolicyDocument"])
        self.assertIn("iam:PutRolePermissionsBoundary", execution_actions)
        self.assertNotIn("iam:DeleteRolePermissionsBoundary", execution_actions)
        self.assertNotIn("iam:UpdateAssumeRolePolicy", execution_actions)

        edge = self.mod.load_cfn(EDGE_CFN)
        expected = {
            "InstanceRole": (
                "${ProjectName}-edge-${EdgeId}-stage0-instance",
                "arn:${AWS::Partition}:iam::${AWS::AccountId}:policy/tokenkey-ec2-edge-instance-boundary",
            ),
            "DLMRole": (
                "${ProjectName}-edge-${EdgeId}-stage0-dlm",
                "arn:${AWS::Partition}:iam::${AWS::AccountId}:policy/tokenkey-ec2-edge-dlm-boundary",
            ),
        }
        for logical_id, (role_name, boundary_arn) in expected.items():
            with self.subTest(role=logical_id):
                self.assertEqual(
                    edge["Resources"][logical_id]["Properties"]["RoleName"],
                    role_name,
                )
                self.assertEqual(
                    edge["Resources"][logical_id]["Properties"]["PermissionsBoundary"],
                    boundary_arn,
                )

    def test_boundaries_preserve_attached_runtime_policy_permissions(self) -> None:
        required = {
            "Ec2EdgeInstanceBoundary": (
                "ec2:DescribeTags",
                "logs:CreateLogGroup",
                "logs:CreateLogStream",
                "logs:DescribeLogGroups",
                "logs:DescribeLogStreams",
                "logs:PutLogEvents",
                "logs:PutRetentionPolicy",
            ),
            "Ec2EdgeDlmBoundary": (
                "ec2:ModifySnapshotTier",
                "events:DeleteRule",
                "events:DescribeRule",
                "events:DisableRule",
                "events:EnableRule",
                "events:ListTargetsByRule",
                "events:PutRule",
                "events:PutTargets",
                "events:RemoveTargets",
            ),
        }
        for boundary_name, actions in required.items():
            for action in actions:
                with self.subTest(boundary=boundary_name, action=action):
                    mutated = copy.deepcopy(self.addon)
                    boundary = mutated["Resources"][boundary_name]
                    boundary["Properties"]["PolicyDocument"] = remove_token(
                        boundary["Properties"]["PolicyDocument"], action
                    )
                    failures = self.mod.validate_contract(mutated, self.base, self.edge)
                    self.assertTrue(any(action in failure for failure in failures), failures)

    def test_real_templates_satisfy_contract(self) -> None:
        self.assertEqual([], self.mod.validate_contract(self.addon, self.base, self.edge))


if __name__ == "__main__":
    unittest.main()
