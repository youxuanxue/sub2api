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
EDGE_CFN = REPO_ROOT / "deploy/aws/cloudformation/stage0-edge-ec2.yaml"

ALLOWED_REGIONS = ("us-east-2", "us-west-2")
EDGE_STACK_PATTERN = "tokenkey-edge-*-stage0"

CALLER_ACTIONS = {
    "cloudwatch:DescribeAlarmHistory",
    "cloudwatch:DescribeAlarms",
    "cloudformation:CreateChangeSet",
    "cloudformation:DeleteChangeSet",
    "cloudformation:DescribeChangeSet",
    "cloudformation:DescribeStackEvents",
    "cloudformation:DescribeStacks",
    "cloudformation:ExecuteChangeSet",
    "cloudformation:GetTemplate",
    "cloudformation:GetTemplateSummary",
    "ec2:AllocateAddress",
    "ec2:CreateTags",
    "ec2:DescribeAddresses",
    "ec2:DescribeImages",
    "ec2:DescribeInstanceTypeOfferings",
    "ec2:DescribeInstances",
    "ec2:DescribeSnapshots",
    "ec2:DescribeVolumes",
    "ec2:DescribeVpcs",
    "iam:PassRole",
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
    "iam:PutRolePermissionsBoundary",
    "iam:RemoveRoleFromInstanceProfile",
    "iam:TagInstanceProfile",
    "iam:TagRole",
    "iam:UntagInstanceProfile",
    "iam:UntagRole",
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
BOUNDARY_REQUIRED_ACTIONS = {
    "Ec2EdgeInstanceBoundary": {
        "ec2:DescribeTags",
        "logs:CreateLogGroup",
        "logs:CreateLogStream",
        "logs:DescribeLogGroups",
        "logs:DescribeLogStreams",
        "logs:PutLogEvents",
        "logs:PutRetentionPolicy",
    },
    "Ec2EdgeDlmBoundary": {
        "ec2:ModifySnapshotTier",
        "events:DeleteRule",
        "events:DescribeRule",
        "events:DisableRule",
        "events:EnableRule",
        "events:ListTargetsByRule",
        "events:PutRule",
        "events:PutTargets",
        "events:RemoveTargets",
    },
}
EC2_CREATE_SCOPE_SIDS = {
    "CreateTaggedEc2EdgeResources",
    "RunTaggedEc2EdgeInstances",
    "TagNewEc2EdgeResources",
}
EC2_RESOURCE_SCOPE_SID = "ManageTaggedEc2EdgeResources"
REQUIRED_EC2_TAGS = {
    "aws:RequestTag/Project": "tokenkey",
    "aws:RequestTag/Environment": "edge",
}
REQUIRED_EC2_RESOURCE_TAGS = {
    "ec2:ResourceTag/Project": "tokenkey",
    "ec2:ResourceTag/Environment": "edge",
}
TAGGED_EDGE_RESOURCES = (
    "VPC",
    "InternetGateway",
    "PublicSubnet",
    "PublicRouteTable",
    "AppSecurityGroup",
    "DataVolume",
    "Instance",
)


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


def validate_contract(addon: dict, base: dict, edge: dict) -> list[str]:
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

    boundary_names = ("Ec2EdgeInstanceBoundary", "Ec2EdgeDlmBoundary")
    boundaries: dict[str, dict] = {}
    for name in boundary_names:
        boundary = _resource(addon, name, "AWS::IAM::ManagedPolicy")
        if boundary is None:
            failures.append(f"missing fixed {name}")
            continue
        boundaries[name] = boundary
        document = (boundary.get("Properties") or {}).get("PolicyDocument") or {}
        boundary_actions = _actions(document)
        for action in sorted(BOUNDARY_REQUIRED_ACTIONS[name] - boundary_actions):
            failures.append(f"{name} missing runtime action {action}")
        forbidden = {
            action for action in boundary_actions
            if action.startswith(("iam:", "sts:", "s3:"))
        }
        for action in sorted(forbidden):
            failures.append(f"{name} contains forbidden boundary action {action}")
        for statement in _statements(document):
            actions = _actions({"Statement": [statement]})
            if actions & {"ssm:GetParameter", "ssm:GetParameters"}:
                resources = _flatten_strings(statement.get("Resource"))
                if not resources or "*" in resources:
                    failures.append(f"{name} parameter reads must be path-scoped")

    statements_by_sid = {
        statement.get("Sid"): statement for statement in _statements(execution_policy)
    }
    expected_role_contracts = {
        "CreateBoundedEdgeInstanceRoles": (
            "stage0-instance",
            "Ec2EdgeInstanceBoundary",
        ),
        "CreateBoundedEdgeDlmRoles": (
            "stage0-dlm",
            "Ec2EdgeDlmBoundary",
        ),
    }
    for sid, (resource_suffix, boundary_ref) in expected_role_contracts.items():
        statement = statements_by_sid.get(sid, {})
        if "iam:CreateRole" not in _actions({"Statement": [statement]}):
            failures.append(f"missing {sid} iam:CreateRole contract")
            continue
        if not str(statement.get("Resource", "")).endswith(resource_suffix):
            failures.append(f"{sid} must target only *-{resource_suffix} roles")
        condition = statement.get("Condition") or {}
        actual_ref = (condition.get("ArnEquals") or {}).get("iam:PermissionsBoundary")
        if actual_ref != boundary_ref:
            failures.append(f"{sid} must require its fixed permissions boundary")
    expected_pass_contracts = {
        "PassEdgeInstanceRole": ("stage0-instance", "ec2.amazonaws.com"),
        "PassEdgeDlmRole": ("stage0-dlm", "dlm.amazonaws.com"),
    }
    for sid, (resource_suffix, service) in expected_pass_contracts.items():
        statement = statements_by_sid.get(sid, {})
        if "iam:PassRole" not in _actions({"Statement": [statement]}):
            failures.append(f"missing {sid} iam:PassRole contract")
            continue
        if not str(statement.get("Resource", "")).endswith(resource_suffix):
            failures.append(f"{sid} must target only *-{resource_suffix} roles")
        actual_service = ((statement.get("Condition") or {}).get("StringEquals") or {}).get(
            "iam:PassedToService"
        )
        if actual_service != service:
            failures.append(f"{sid} must restrict iam:PassedToService to {service}")
    for action in ("iam:DeleteRolePermissionsBoundary", "iam:UpdateAssumeRolePolicy"):
        if action in _actions(execution_policy):
            failures.append(f"execution role must not allow {action}")

    for sid in sorted(EC2_CREATE_SCOPE_SIDS):
        statement = statements_by_sid.get(sid)
        if statement is None:
            failures.append(f"missing EC2 tag scope statement {sid}")
            continue
        equals = ((statement.get("Condition") or {}).get("StringEquals") or {})
        like = ((statement.get("Condition") or {}).get("StringLike") or {})
        if any(equals.get(key) != value for key, value in REQUIRED_EC2_TAGS.items()):
            failures.append(f"{sid} missing EC2 tag scope Project=tokenkey Environment=edge")
        if like.get("aws:RequestTag/EdgeId") != "us*":
            failures.append(f"{sid} missing EC2 tag scope EdgeId=us*")
    managed = statements_by_sid.get(EC2_RESOURCE_SCOPE_SID)
    if managed is None:
        failures.append(f"missing EC2 tag scope statement {EC2_RESOURCE_SCOPE_SID}")
    else:
        equals = ((managed.get("Condition") or {}).get("StringEquals") or {})
        if any(equals.get(key) != value for key, value in REQUIRED_EC2_RESOURCE_TAGS.items()):
            failures.append(
                f"{EC2_RESOURCE_SCOPE_SID} missing EC2 tag scope Project=tokenkey Environment=edge"
            )

    expected_role_boundaries = {
        "InstanceRole": ("stage0-instance", "tokenkey-ec2-edge-instance-boundary"),
        "DLMRole": ("stage0-dlm", "tokenkey-ec2-edge-dlm-boundary"),
    }
    edge_resources = edge.get("Resources") or {}
    parameters = edge.get("Parameters") or {}
    for name, expected in (("ProjectName", ["tokenkey"]), ("Environment", ["edge"])):
        parameter = parameters.get(name) if isinstance(parameters, dict) else None
        if not isinstance(parameter, dict) or parameter.get("AllowedValues") != expected:
            failures.append(f"edge template {name} must be fixed to {expected[0]}")
    for logical_id in TAGGED_EDGE_RESOURCES:
        resource = edge_resources.get(logical_id) if isinstance(edge_resources, dict) else None
        tags = ((resource or {}).get("Properties") or {}).get("Tags") or []
        tag_keys = {
            tag.get("Key") for tag in tags
            if isinstance(tag, dict) and isinstance(tag.get("Key"), str)
        }
        for key in ("Project", "Environment", "EdgeId"):
            if key not in tag_keys:
                failures.append(f"edge template {logical_id} missing {key} tag")
    for logical_id, (role_suffix, policy_name) in expected_role_boundaries.items():
        role = edge_resources.get(logical_id) if isinstance(edge_resources, dict) else None
        if not isinstance(role, dict):
            failures.append(f"edge template missing {logical_id}")
            continue
        boundary = ((role.get("Properties") or {}).get("PermissionsBoundary"))
        role_name = ((role.get("Properties") or {}).get("RoleName"))
        if role_suffix not in str(role_name):
            failures.append(f"{logical_id} must use fixed {role_suffix} role name")
        if policy_name not in str(boundary):
            failures.append(f"{logical_id} must use {policy_name}")

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

    cfn_create = "cloudformation:CreateChangeSet"
    cfn_create_statements = [
        statement for statement in _statements(caller_policy)
        if cfn_create in _actions({"Statement": [statement]})
    ]
    for statement in cfn_create_statements:
        resources = _flatten_strings(statement.get("Resource"))
        if not resources or any(
            resource == "*" or EDGE_STACK_PATTERN not in resource
            for resource in resources
        ):
            failures.append(
                f"caller policy CloudFormation mutations must use stack scope {EDGE_STACK_PATTERN}"
            )
            break
        role_arn = ((statement.get("Condition") or {}).get("StringEquals") or {}).get(
            "cloudformation:RoleArn"
        )
        if role_arn != "Ec2EdgeCloudFormationExecutionRole.Arn":
            failures.append(
                "caller policy CloudFormation mutations must require the fixed execution role"
            )
            break
    if not cfn_create_statements:
        failures.append(f"caller policy missing stack scope {EDGE_STACK_PATTERN}")

    cfn_followup_actions = {
        "cloudformation:DeleteChangeSet",
        "cloudformation:DescribeChangeSet",
        "cloudformation:DescribeStackEvents",
        "cloudformation:DescribeStacks",
        "cloudformation:ExecuteChangeSet",
        "cloudformation:GetTemplate",
        "cloudformation:GetTemplateSummary",
    }
    followup_covered: set[str] = set()
    for statement in _statements(caller_policy):
        actions = _actions({"Statement": [statement]})
        followups = actions & cfn_followup_actions
        if not followups:
            continue
        followup_covered.update(followups)
        resources = _flatten_strings(statement.get("Resource"))
        if not resources or any(
            resource == "*" or EDGE_STACK_PATTERN not in resource
            for resource in resources
        ):
            failures.append(
                f"caller policy CloudFormation follow-up actions must use stack scope {EDGE_STACK_PATTERN}"
            )
        conditions = statement.get("Condition") or {}
        condition_keys = {
            key
            for operator in conditions.values()
            if isinstance(operator, dict)
            for key in operator
        }
        if "cloudformation:RoleArn" in condition_keys:
            failures.append(
                "caller policy CloudFormation follow-up actions contain unsupported RoleArn condition"
            )
    for action in sorted(cfn_followup_actions - followup_covered):
        failures.append(f"caller policy missing scoped CloudFormation follow-up action {action}")

    launch_dependencies = statements_by_sid.get("UseEc2EdgeLaunchDependencies") or {}
    dependency_resources = set(_flatten_strings(launch_dependencies.get("Resource")))
    expected_network_interfaces = {
        f"arn:${{AWS::Partition}}:ec2:{region}:${{AWS::AccountId}}:network-interface/*"
        for region in ALLOWED_REGIONS
    }
    for resource in sorted(expected_network_interfaces - dependency_resources):
        failures.append(f"RunInstances missing launch dependency {resource}")

    caller_by_sid = {
        statement.get("Sid"): statement for statement in _statements(caller_policy)
    }
    execution_by_sid = {
        statement.get("Sid"): statement for statement in _statements(execution_policy)
    }
    bootstrap_parameters = execution_by_sid.get("ManageEdgeBootstrapParameters") or {}
    bootstrap_resources = _flatten_strings(bootstrap_parameters.get("Resource"))
    expected_bootstrap_resources = {
        f"arn:${{AWS::Partition}}:ssm:{region}:${{AWS::AccountId}}:parameter/tokenkey/edge/us*/stage0/*"
        for region in ALLOWED_REGIONS
    }
    if set(bootstrap_resources) != expected_bootstrap_resources:
        failures.append(
            "execution role bootstrap parameters must be scoped to each approved Edge region and path"
        )
    alarm_statement = execution_by_sid.get("ManageEdgeAlarms") or {}
    alarm_resources = set(_flatten_strings(alarm_statement.get("Resource")))
    expected_alarm_resources = {
        f"arn:${{AWS::Partition}}:cloudwatch:{region}:${{AWS::AccountId}}:alarm:tokenkey-us*-*"
        for region in ALLOWED_REGIONS
    }
    if alarm_resources != expected_alarm_resources:
        failures.append("execution role alarm scope must cover only tokenkey Edge alarm names")

    ec2_command = caller_by_sid.get("SendRunShellScriptToTokenkeyEdge") or {}
    ec2_command_resources = set(_flatten_strings(ec2_command.get("Resource")))
    expected_ec2_command_resources = {
        f"arn:${{AWS::Partition}}:ec2:{region}:${{AWS::AccountId}}:instance/*"
        for region in ALLOWED_REGIONS
    }
    ec2_command_equals = ((ec2_command.get("Condition") or {}).get("StringEquals") or {})
    ec2_command_like = ((ec2_command.get("Condition") or {}).get("StringLike") or {})
    if (
        ec2_command_resources != expected_ec2_command_resources
        or ec2_command_equals.get("ssm:resourceTag/Project") != "tokenkey"
        or ec2_command_equals.get("ssm:resourceTag/Environment") != "edge"
        or ec2_command_like.get("ssm:resourceTag/EdgeId") != "us*"
    ):
        failures.append("caller policy EC2 instance SendCommand must be tag-scoped to TokenKey Edge instances")

    dlm_create = execution_by_sid.get("CreateTaggedEdgeSnapshotPolicy") or {}
    dlm_create_equals = ((dlm_create.get("Condition") or {}).get("StringEquals") or {})
    dlm_create_like = ((dlm_create.get("Condition") or {}).get("StringLike") or {})
    if (
        dlm_create_equals.get("aws:RequestTag/Project") != "tokenkey"
        or dlm_create_equals.get("aws:RequestTag/Environment") != "edge"
        or dlm_create_like.get("aws:RequestTag/EdgeId") != "us*"
    ):
        failures.append("execution role DLM create tags must identify one TokenKey Edge")
    dlm_manage = execution_by_sid.get("ManageTaggedEdgeSnapshotPolicies") or {}
    dlm_resources = set(_flatten_strings(dlm_manage.get("Resource")))
    expected_dlm_resources = {
        f"arn:${{AWS::Partition}}:dlm:{region}:${{AWS::AccountId}}:policy/*"
        for region in ALLOWED_REGIONS
    }
    dlm_manage_equals = ((dlm_manage.get("Condition") or {}).get("StringEquals") or {})
    dlm_manage_like = ((dlm_manage.get("Condition") or {}).get("StringLike") or {})
    if (
        dlm_resources != expected_dlm_resources
        or dlm_manage_equals.get("aws:ResourceTag/Project") != "tokenkey"
        or dlm_manage_equals.get("aws:ResourceTag/Environment") != "edge"
        or dlm_manage_like.get("aws:ResourceTag/EdgeId") != "us*"
    ):
        failures.append("execution role DLM policy scope must cover only tagged Edge policies")
    lightsail_command = caller_by_sid.get("SendRunShellScriptToTokenkeyLightsailEdge")
    if lightsail_command is None:
        failures.append("caller policy missing tagged Lightsail managed-instance SendCommand")
    else:
        resources = _flatten_strings(lightsail_command.get("Resource"))
        conditions = ((lightsail_command.get("Condition") or {}).get("StringEquals") or {})
        if (
            not resources
            or any(":ssm:" not in value or ":managed-instance/*" not in value for value in resources)
            or conditions.get("ssm:resourceTag/Project") != "tokenkey"
            or conditions.get("ssm:resourceTag/Platform") != "lightsail"
        ):
            failures.append("caller policy Lightsail managed-instance SendCommand must be tag-scoped")
    eip = caller_by_sid.get("AllocateEdgeEip") or {}
    eip_equals = ((eip.get("Condition") or {}).get("StringEquals") or {})
    eip_like = ((eip.get("Condition") or {}).get("StringLike") or {})
    if (
        eip_equals.get("aws:RequestTag/Project") != "tokenkey"
        or eip_equals.get("aws:RequestTag/Environment") != "edge"
        or eip_equals.get("aws:RequestTag/Purpose") != "migration-candidate"
        or eip_like.get("aws:RequestTag/EdgeId") != "us*"
    ):
        failures.append("caller policy EIP request tags must identify one edge candidate")
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
        edge = load_cfn(EDGE_CFN)
    except (OSError, ValueError, yaml.YAMLError) as exc:
        print(f"FAIL: cannot load EC2 Edge OIDC templates: {exc}", file=sys.stderr)
        return 2
    failures = validate_contract(addon, base, edge)
    if failures:
        print("FAIL: EC2 Edge OIDC permission contract", file=sys.stderr)
        for failure in failures:
            print(f"  - {failure}", file=sys.stderr)
        return 1
    print(f"ok: EC2 Edge OIDC permission coverage ({len(CALLER_ACTIONS) + len(EXECUTION_ACTIONS)} actions)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
