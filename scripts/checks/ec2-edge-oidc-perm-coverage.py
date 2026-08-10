#!/usr/bin/env python3
"""Validate the EC2 Edge OIDC addon permission and scope contract."""
from __future__ import annotations

import argparse
import pathlib
import sys
from typing import Any

import yaml


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
ADDON_CFN = REPO_ROOT / "deploy/aws/cloudformation/cicd-oidc-ec2-edge-addon.yaml"
BASE_CFN = REPO_ROOT / "deploy/aws/cloudformation/cicd-oidc.yaml"

ALLOWED_REGIONS = ("us-east-2", "us-west-2")
EDGE_STACK_PATTERN = "tokenkey-edge-*-stage0"

CALLER_ACTIONS = {
    "cloudwatch:DescribeAlarmHistory",
    "cloudwatch:DescribeAlarms",
    "cloudformation:CreateChangeSet",
    "cloudformation:DeleteChangeSet",
    "cloudformation:DeleteStack",
    "cloudformation:DescribeChangeSet",
    "cloudformation:DescribeStackEvents",
    "cloudformation:DescribeStacks",
    "cloudformation:ExecuteChangeSet",
    "cloudformation:GetTemplate",
    "cloudformation:GetTemplateSummary",
    "ec2:AllocateAddress",
    "ec2:CreateSnapshot",
    "ec2:CreateTags",
    "ec2:DescribeAddresses",
    "ec2:DescribeImages",
    "ec2:DescribeInstanceTypeOfferings",
    "ec2:DescribeInstances",
    "ec2:DescribeSnapshots",
    "ec2:DescribeVolumes",
    "ec2:DescribeVpcs",
    "ec2:ReleaseAddress",
    "iam:PassRole",
    "lightsail:GetInstanceMetricData",
    "servicequotas:GetServiceQuota",
    "ssm:DescribeParameters",
    "ssm:DescribeInstanceInformation",
    "ssm:GetCommandInvocation",
    "ssm:GetParameter",
    "ssm:ListCommandInvocations",
    "ssm:ListCommands",
    "ssm:SendCommand",
}
EXECUTION_ACTIONS = {
    "cloudwatch:DeleteAlarms",
    "cloudwatch:DescribeAlarms",
    "cloudwatch:PutMetricAlarm",
    "dlm:CreateLifecyclePolicy",
    "dlm:DeleteLifecyclePolicy",
    "dlm:GetLifecyclePolicies",
    "dlm:GetLifecyclePolicy",
    "dlm:ListTagsForResource",
    "dlm:TagResource",
    "dlm:UntagResource",
    "dlm:UpdateLifecyclePolicy",
    "ec2:AssociateAddress",
    "ec2:AssociateRouteTable",
    "ec2:AttachInternetGateway",
    "ec2:AttachVolume",
    "ec2:AuthorizeSecurityGroupIngress",
    "ec2:CreateInternetGateway",
    "ec2:CreateRoute",
    "ec2:CreateRouteTable",
    "ec2:CreateSecurityGroup",
    "ec2:CreateSubnet",
    "ec2:CreateTags",
    "ec2:CreateVolume",
    "ec2:CreateVpc",
    "ec2:DeleteInternetGateway",
    "ec2:DeleteRoute",
    "ec2:DeleteRouteTable",
    "ec2:DeleteSecurityGroup",
    "ec2:DeleteSubnet",
    "ec2:DeleteTags",
    "ec2:DeleteVolume",
    "ec2:DeleteVpc",
    "ec2:Describe*",
    "ec2:DetachInternetGateway",
    "ec2:DetachVolume",
    "ec2:DisassociateAddress",
    "ec2:DisassociateRouteTable",
    "ec2:ModifyInstanceAttribute",
    "ec2:ModifyInstanceCreditSpecification",
    "ec2:ModifySubnetAttribute",
    "ec2:ModifyVolume",
    "ec2:ModifyVpcAttribute",
    "ec2:ReplaceRoute",
    "ec2:RevokeSecurityGroupIngress",
    "ec2:RunInstances",
    "ec2:StartInstances",
    "ec2:StopInstances",
    "ec2:TerminateInstances",
    "iam:AddRoleToInstanceProfile",
    "iam:AttachRolePolicy",
    "iam:CreateInstanceProfile",
    "iam:CreateRole",
    "iam:DeleteInstanceProfile",
    "iam:DeleteRole",
    "iam:DeleteRolePolicy",
    "iam:DetachRolePolicy",
    "iam:GetInstanceProfile",
    "iam:GetRole",
    "iam:GetRolePolicy",
    "iam:ListAttachedRolePolicies",
    "iam:ListInstanceProfilesForRole",
    "iam:ListRolePolicies",
    "iam:ListRoleTags",
    "iam:PassRole",
    "iam:PutRolePolicy",
    "iam:RemoveRoleFromInstanceProfile",
    "iam:TagInstanceProfile",
    "iam:TagRole",
    "iam:UntagInstanceProfile",
    "iam:UntagRole",
    "iam:UpdateAssumeRolePolicy",
    "iam:UpdateRoleDescription",
    "ssm:DeleteParameter",
    "ssm:GetParameter",
    "ssm:GetParameters",
    "ssm:PutParameter",
}
BASE_FORBIDDEN_ACTIONS = {
    "cloudformation:CreateChangeSet",
    "ec2:AllocateAddress",
    "ec2:ModifyInstanceCreditSpecification",
    "ec2:RunInstances",
    "iam:PassRole",
}
LIGHTSAIL_SOURCE_PARAMETER_SUFFIX = (
    "parameter/tokenkey/lightsail/us*/ssm_managed_instance_id"
)
GHCR_PAT_PARAMETER_SUFFIX = "parameter/tokenkey/edge/us*/ghcr/pat"


class _CfnLoader(yaml.SafeLoader):
    pass


def _construct_cfn_tag(loader: _CfnLoader, _suffix: str, node: yaml.Node) -> Any:
    if isinstance(node, yaml.ScalarNode):
        return loader.construct_scalar(node)
    if isinstance(node, yaml.SequenceNode):
        return loader.construct_sequence(node)
    return loader.construct_mapping(node)


_CfnLoader.add_multi_constructor("!", _construct_cfn_tag)


def load_cfn(path: pathlib.Path) -> dict:
    document = yaml.load(path.read_text(encoding="utf-8"), Loader=_CfnLoader)
    if not isinstance(document, dict):
        raise ValueError(f"{path} root must be a mapping")
    return document


def _as_list(value: object) -> list:
    return value if isinstance(value, list) else [value]


def _actions(policy: dict) -> set[str]:
    actions: set[str] = set()
    for statement in _as_list(policy.get("Statement") or []):
        if not isinstance(statement, dict) or statement.get("Effect") != "Allow":
            continue
        actions.update(str(action) for action in _as_list(statement.get("Action") or []))
    return actions


def _statements(policy: dict) -> list[dict]:
    return [
        statement for statement in _as_list(policy.get("Statement") or [])
        if isinstance(statement, dict) and statement.get("Effect") == "Allow"
    ]


def _flatten_strings(value: object) -> list[str]:
    if isinstance(value, dict):
        return [item for child in value.values() for item in _flatten_strings(child)]
    if isinstance(value, list):
        return [item for child in value for item in _flatten_strings(child)]
    return [value] if isinstance(value, str) else []


def _resource(addon: dict, name: str, resource_type: str) -> dict | None:
    resource = (addon.get("Resources") or {}).get(name)
    if not isinstance(resource, dict) or resource.get("Type") != resource_type:
        return None
    return resource


def _role_policy(role: dict) -> dict:
    policies = ((role.get("Properties") or {}).get("Policies") or [])
    if not policies or not isinstance(policies[0], dict):
        return {}
    document = policies[0].get("PolicyDocument")
    return document if isinstance(document, dict) else {}


def validate_contract(addon: dict, base: dict) -> list[str]:
    failures: list[str] = []
    caller_resource = _resource(addon, "Ec2EdgeAddonPolicy", "AWS::IAM::Policy")
    execution_resource = _resource(
        addon, "Ec2EdgeCloudFormationExecutionRole", "AWS::IAM::Role"
    )
    if caller_resource is None:
        failures.append("missing independent Ec2EdgeAddonPolicy")
        caller_policy: dict = {}
    else:
        candidate = (caller_resource.get("Properties") or {}).get("PolicyDocument")
        caller_policy = candidate if isinstance(candidate, dict) else {}
    if execution_resource is None:
        failures.append("missing independent Ec2EdgeCloudFormationExecutionRole")
        execution_policy: dict = {}
    else:
        execution_policy = _role_policy(execution_resource)

    caller_actions = _actions(caller_policy)
    execution_actions = _actions(execution_policy)
    for action in sorted(CALLER_ACTIONS - caller_actions):
        failures.append(f"caller policy missing {action}")
    for action in sorted(caller_actions - CALLER_ACTIONS):
        failures.append(f"caller policy unexpected {action}")
    for action in sorted(EXECUTION_ACTIONS - execution_actions):
        failures.append(f"execution role missing {action}")
    for action in sorted(execution_actions - EXECUTION_ACTIONS):
        failures.append(f"execution role unexpected {action}")

    addon_strings = _flatten_strings(addon)
    caller_strings = _flatten_strings(caller_policy)
    for region in ALLOWED_REGIONS:
        if region not in addon_strings:
            failures.append(f"addon missing allowed region {region}")
        if region not in caller_strings:
            failures.append(f"caller policy missing allowed region {region}")
        if not any(
            f":ssm:{region}:" in value
            and LIGHTSAIL_SOURCE_PARAMETER_SUFFIX in value
            for value in caller_strings
        ):
            failures.append(
                "caller policy missing "
                f"{region} {LIGHTSAIL_SOURCE_PARAMETER_SUFFIX}"
            )

    for statement in _statements(caller_policy):
        if "ssm:GetParameter" not in _actions({"Statement": [statement]}):
            continue
        for resource in _flatten_strings(statement.get("Resource")):
            if GHCR_PAT_PARAMETER_SUFFIX in resource:
                failures.append(
                    "caller policy ssm:GetParameter must not cover GHCR PAT values"
                )
                break

    cfn_mutations = {"cloudformation:CreateChangeSet", "cloudformation:DeleteStack"}
    cfn_scope_statements = [
        statement for statement in _statements(caller_policy)
        if _actions({"Statement": [statement]}) & cfn_mutations
    ]
    for statement in cfn_scope_statements:
        resources = _flatten_strings(statement.get("Resource"))
        if not resources or any(
            resource == "*" or EDGE_STACK_PATTERN not in resource
            for resource in resources
        ):
            failures.append(
                f"caller policy CloudFormation mutations must use stack scope {EDGE_STACK_PATTERN}"
            )
            break
    if not cfn_scope_statements:
        failures.append(f"caller policy missing stack scope {EDGE_STACK_PATTERN}")
    unexpected_regions = {
        value for value in caller_strings
        if value.startswith("us-") and value not in ALLOWED_REGIONS
    }
    for region in sorted(unexpected_regions):
        failures.append(f"addon contains unapproved region {region}")

    base_resources = base.get("Resources") or {}
    base_actions: set[str] = set()
    if isinstance(base_resources, dict):
        for resource in base_resources.values():
            if not isinstance(resource, dict):
                continue
            properties = resource.get("Properties") or {}
            for policy in properties.get("Policies") or []:
                if isinstance(policy, dict) and isinstance(policy.get("PolicyDocument"), dict):
                    base_actions.update(_actions(policy["PolicyDocument"]))
    for action in sorted(BASE_FORBIDDEN_ACTIONS & base_actions):
        failures.append(f"base OIDC role must not grant EC2 Edge provisioning action {action}")

    return failures


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true")
    parser.parse_args()
    try:
        addon = load_cfn(ADDON_CFN)
        base = load_cfn(BASE_CFN)
    except (OSError, ValueError, yaml.YAMLError) as exc:
        print(f"FAIL: cannot load EC2 Edge OIDC templates: {exc}", file=sys.stderr)
        return 2
    failures = validate_contract(addon, base)
    if failures:
        print("FAIL: EC2 Edge OIDC permission contract", file=sys.stderr)
        for failure in failures:
            print(f"  - {failure}", file=sys.stderr)
        return 1
    print(f"ok: EC2 Edge OIDC permission coverage ({len(CALLER_ACTIONS) + len(EXECUTION_ACTIONS)} actions)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
