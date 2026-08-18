#!/usr/bin/env python3
"""Compare live prod QA raw archive bucket policy against repository IAM contract."""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[2]
PROD_REGION = "us-east-1"
RAW_ARCHIVE_STACK = "tokenkey-prod-qa-raw-archive"
STAGE0_STACK = "tokenkey-prod-stage0"
STAGE0_PUBLIC_ROUTE_TABLE_LOGICAL = "PublicRouteTable"
EDGE_ROLE_NAME = "tokenkey-lightsail-ssm-hybrid"
EDGE_DENY_SID = "DenyLightsailEdgeRole"

APP_WRITE_SID = "AllowAppInstanceRoleWriteRaw"
APP_VERIFY_SID = "AllowAppInstanceRoleVerifyRaw"
APP_SIDS = {APP_WRITE_SID, APP_VERIFY_SID}
FORBIDDEN_APP_ACTIONS = {
    "s3:*",
    "s3:ListBucket",
    "s3:ListBucketMultipartUploads",
    "s3:GetBucketLocation",
    "s3:DeleteObject",
    "s3:DeleteObjectVersion",
    "s3:PutObjectAcl",
    "s3:GetObjectAcl",
}
EXPECTED_WRITE_ACTIONS = {
    "s3:PutObject",
    "s3:AbortMultipartUpload",
    "s3:ListMultipartUploadParts",
}
EXPECTED_VERIFY_ACTIONS = {"s3:GetObject"}
EXPECTED_APP_RESOURCES = [
    "/commit.json",
    "/segments/*/manifest.json",
    "/segments/*/records.parquet",
    "/segments/*/evidence.pack",
    "/segments/*/evidence-index.jsonl.zst",
    "/segments/*/orphan-evidence-index.jsonl.zst",
]
EXPECTED_SID_ACTIONS = {
    APP_WRITE_SID: EXPECTED_WRITE_ACTIONS,
    APP_VERIFY_SID: EXPECTED_VERIFY_ACTIONS,
}


def _aws_json(args: list[str]) -> dict[str, Any]:
    completed = subprocess.run(args, capture_output=True, text=True, check=False)
    if completed.returncode != 0:
        raise RuntimeError((completed.stderr or completed.stdout or "aws failed").strip()[:600])
    payload = json.loads(completed.stdout)
    if isinstance(payload, list):
        return {"Items": payload}
    if not isinstance(payload, dict):
        raise RuntimeError("aws json is not an object")
    return payload


def _stack_output(stack_name: str, key: str) -> str:
    payload = _aws_json(
        [
            "aws",
            "cloudformation",
            "describe-stacks",
            "--region",
            PROD_REGION,
            "--stack-name",
            stack_name,
            "--output",
            "json",
        ]
    )
    stacks = payload.get("Stacks")
    if not isinstance(stacks, list) or not stacks:
        raise RuntimeError(f"stack missing: {stack_name}")
    for item in stacks[0].get("Outputs", []):
        if isinstance(item, dict) and item.get("OutputKey") == key:
            value = item.get("OutputValue")
            if isinstance(value, str) and value.strip():
                return value.strip()
    raise RuntimeError(f"{stack_name} output missing: {key}")


def _stack_resource_physical_id(stack_name: str, logical_id: str) -> str:
    payload = _aws_json(
        [
            "aws",
            "cloudformation",
            "describe-stack-resources",
            "--region",
            PROD_REGION,
            "--stack-name",
            stack_name,
            "--logical-resource-id",
            logical_id,
            "--output",
            "json",
        ]
    )
    resources = payload.get("StackResources")
    if not isinstance(resources, list) or not resources:
        raise RuntimeError(f"{stack_name} resource missing: {logical_id}")
    physical = resources[0].get("PhysicalResourceId")
    if not isinstance(physical, str) or not physical.strip():
        raise RuntimeError(f"{stack_name} resource {logical_id} has no physical id")
    return physical.strip()


def _account_id() -> str:
    out = subprocess.check_output(
        ["aws", "sts", "get-caller-identity", "--query", "Account", "--output", "text"],
        text=True,
    ).strip()
    if not re.fullmatch(r"\d{12}", out):
        raise RuntimeError(f"unexpected aws account id: {out!r}")
    return out


def _stack_parameter(stack_name: str, key: str) -> str:
    payload = _aws_json(
        [
            "aws",
            "cloudformation",
            "describe-stacks",
            "--region",
            PROD_REGION,
            "--stack-name",
            stack_name,
            "--output",
            "json",
        ]
    )
    stacks = payload.get("Stacks")
    if not isinstance(stacks, list) or not stacks:
        raise RuntimeError(f"stack missing: {stack_name}")
    for item in stacks[0].get("Parameters", []):
        if isinstance(item, dict) and item.get("ParameterKey") == key:
            value = item.get("ParameterValue")
            if isinstance(value, str) and value.strip():
                return value.strip()
    raise RuntimeError(f"{stack_name} parameter missing: {key}")


def resolve_app_role_arn() -> str:
    return _stack_parameter(RAW_ARCHIVE_STACK, "AppInstanceRoleArn")


def _live_bucket_policy(bucket: str) -> list[dict[str, Any]]:
    payload = _aws_json(
        [
            "aws",
            "s3api",
            "get-bucket-policy",
            "--bucket",
            bucket,
            "--output",
            "json",
        ]
    )
    policy = json.loads(payload["Policy"])
    statements = policy.get("Statement")
    if not isinstance(statements, list):
        raise RuntimeError("bucket policy has no statements")
    return [item for item in statements if isinstance(item, dict)]


def _statement_for_principal(statements: list[dict[str, Any]], principal_arn: str) -> dict[str, dict[str, Any]]:
    matched: dict[str, dict[str, Any]] = {}
    for statement in statements:
        principal = statement.get("Principal")
        if isinstance(principal, dict):
            aws_principal = principal.get("AWS")
        else:
            aws_principal = principal
        candidates = aws_principal if isinstance(aws_principal, list) else [aws_principal]
        if principal_arn not in {str(item) for item in candidates}:
            continue
        sid = statement.get("Sid")
        if isinstance(sid, str) and sid:
            matched[sid] = statement
    return matched


def _action_set(statement: dict[str, Any]) -> set[str]:
    actions = statement.get("Action")
    if isinstance(actions, str):
        return {actions}
    if isinstance(actions, list):
        return {str(item) for item in actions if isinstance(item, str)}
    return set()


def _resource_list(statement: dict[str, Any]) -> list[str]:
    resources = statement.get("Resource")
    if isinstance(resources, str):
        return [resources]
    if isinstance(resources, list):
        return [str(item) for item in resources if isinstance(item, str)]
    return []


def _expected_app_resources(bucket: str) -> list[str]:
    base = f"arn:aws:s3:::{bucket}/raw/v1/date=*/hour=*"
    return sorted(f"{base}{suffix}" for suffix in EXPECTED_APP_RESOURCES)


def _principal_arns(statement: dict[str, Any]) -> set[str]:
    principal = statement.get("Principal")
    if isinstance(principal, dict):
        principal = principal.get("AWS")
    values = principal if isinstance(principal, list) else [principal]
    return {str(value) for value in values if isinstance(value, str)}


def _statement_names_principal(statement: dict[str, Any], principal_arn: str) -> bool:
    if principal_arn in _principal_arns(statement):
        return True
    condition = statement.get("Condition")
    if not isinstance(condition, dict):
        return False
    for operator in ("ArnEquals", "ArnLike", "StringEquals", "StringLike"):
        clause = condition.get(operator)
        if not isinstance(clause, dict):
            continue
        value = clause.get("aws:PrincipalArn")
        candidates = value if isinstance(value, list) else [value]
        if principal_arn in {str(item) for item in candidates if isinstance(item, str)}:
            return True
    return False


def evaluate_edge_qa_boundary(
    *,
    account_id: str,
    buckets: dict[str, list[dict[str, Any]]],
    partition: str = "aws",
) -> dict[str, Any]:
    edge_role_arn = f"arn:{partition}:iam::{account_id}:role/{EDGE_ROLE_NAME}"
    failures: list[str] = []
    checked_buckets = sorted(buckets)
    for bucket, statements in sorted(buckets.items()):
        expected_resources = {
            f"arn:{partition}:s3:::{bucket}",
            f"arn:{partition}:s3:::{bucket}/*",
        }
        matching_denies = [
            statement
            for statement in statements
            if statement.get("Sid") == EDGE_DENY_SID
        ]
        if len(matching_denies) != 1:
            failures.append(f"{bucket}:edge_deny_count:{len(matching_denies)}")
        else:
            deny = matching_denies[0]
            if deny.get("Effect") != "Deny":
                failures.append(f"{bucket}:edge_deny_effect")
            if deny.get("Principal") != "*":
                failures.append(f"{bucket}:edge_deny_principal")
            if _action_set(deny) != {"s3:*"}:
                failures.append(f"{bucket}:edge_deny_actions")
            if set(_resource_list(deny)) != expected_resources:
                failures.append(f"{bucket}:edge_deny_resources")
            condition = deny.get("Condition")
            expected_condition = {
                "ArnEquals": {"aws:PrincipalArn": edge_role_arn}
            }
            if condition != expected_condition:
                failures.append(f"{bucket}:edge_deny_condition")

        for statement in statements:
            if statement.get("Effect") != "Allow":
                continue
            if _statement_names_principal(statement, edge_role_arn):
                sid = str(statement.get("Sid") or "missing")
                failures.append(f"{bucket}:edge_role_allowed:{sid}")

    return {
        "ok": not failures,
        "status": "applied" if not failures else "drift",
        "edge_role_arn": edge_role_arn,
        "buckets": checked_buckets,
        "failures": failures,
    }


def verify_live_edge_qa_boundary(account_id: str) -> dict[str, Any]:
    try:
        raw_bucket = _stack_output(RAW_ARCHIVE_STACK, "QaRawArchiveBucketName")
        bundle_bucket = _stack_output(RAW_ARCHIVE_STACK, "QaBundleBucketName")
        buckets = {
            raw_bucket: _live_bucket_policy(raw_bucket),
            bundle_bucket: _live_bucket_policy(bundle_bucket),
        }
    except RuntimeError as exc:
        return {
            "ok": False,
            "status": "unknown",
            "edge_role_arn": f"arn:aws:iam::{account_id}:role/{EDGE_ROLE_NAME}",
            "buckets": [],
            "failures": [str(exc)],
        }
    return evaluate_edge_qa_boundary(account_id=account_id, buckets=buckets)


def _verify_s3_gateway_endpoint(
    *,
    vpc_id: str,
    route_table_ids: list[str],
    endpoint_id: str,
) -> list[str]:
    failures: list[str] = []
    payload = _aws_json(
        [
            "aws",
            "ec2",
            "describe-vpc-endpoints",
            "--region",
            PROD_REGION,
            "--vpc-endpoint-ids",
            endpoint_id,
            "--output",
            "json",
        ]
    )
    endpoints = payload.get("VpcEndpoints")
    if not isinstance(endpoints, list) or not endpoints:
        return [f"s3_endpoint_missing:{endpoint_id}"]
    endpoint = endpoints[0]
    if endpoint.get("State") != "available":
        failures.append("s3_endpoint_not_available")
    if endpoint.get("VpcEndpointType") != "Gateway":
        failures.append("s3_endpoint_not_gateway")
    service_name = endpoint.get("ServiceName")
    expected_service = f"com.amazonaws.{PROD_REGION}.s3"
    if service_name != expected_service:
        failures.append("s3_endpoint_service_name_drift")
    if endpoint.get("VpcId") != vpc_id:
        failures.append("s3_endpoint_vpc_mismatch")
    prefix_list_id = endpoint.get("PrefixListId")
    attached = endpoint.get("RouteTableIds") or []
    if not isinstance(attached, list):
        attached = []
    attached_set = {str(item) for item in attached}
    for route_table_id in route_table_ids:
        if route_table_id not in attached_set:
            failures.append(f"s3_endpoint_route_table_not_attached:{route_table_id}")
        route_payload = _aws_json(
            [
                "aws",
                "ec2",
                "describe-route-tables",
                "--region",
                PROD_REGION,
                "--route-table-ids",
                route_table_id,
                "--output",
                "json",
            ]
        )
        tables = route_payload.get("RouteTables")
        if not isinstance(tables, list) or not tables:
            failures.append(f"route_table_missing:{route_table_id}")
            continue
        routes = tables[0].get("Routes") or []
        has_gateway_route = False
        for route in routes:
            if not isinstance(route, dict):
                continue
            destination_prefix_list_id = route.get("DestinationPrefixListId")
            prefix_matches = (
                destination_prefix_list_id == prefix_list_id
                if isinstance(prefix_list_id, str) and prefix_list_id
                else isinstance(destination_prefix_list_id, str)
                and bool(destination_prefix_list_id)
            )
            if route.get("GatewayId") == endpoint_id and prefix_matches:
                has_gateway_route = True
                break
        if not has_gateway_route:
            failures.append(f"route_table_missing_s3_gateway_route:{route_table_id}")
    return failures


def evaluate(
    *,
    bucket: str,
    app_role_arn: str,
    statements: list[dict[str, Any]] | None = None,
    verify_network: bool = False,
) -> dict[str, Any]:
    failures: list[str] = []
    if statements is None:
        try:
            statements = _live_bucket_policy(bucket)
        except RuntimeError as exc:
            return {"ok": False, "status": "unknown", "failures": [str(exc)], "bucket": bucket}

    app = _statement_for_principal(statements, app_role_arn)
    missing_sids = APP_SIDS - set(app)
    if missing_sids:
        failures.extend(f"missing_app_sid:{sid}" for sid in sorted(missing_sids))

    expected_resources = _expected_app_resources(bucket)
    for sid, statement in app.items():
        if sid not in APP_SIDS:
            failures.append(f"unexpected_app_sid:{sid}")
            continue
        if statement.get("Effect") != "Allow":
            failures.append(f"{sid}:effect_not_allow")
        action_set = _action_set(statement)
        forbidden = sorted(action_set & FORBIDDEN_APP_ACTIONS)
        if forbidden:
            failures.append(f"{sid}:forbidden_action:{','.join(forbidden)}")
        expected_actions = EXPECTED_SID_ACTIONS[sid]
        extra = sorted(action_set - expected_actions)
        missing = sorted(expected_actions - action_set)
        if extra:
            failures.append(f"{sid}:unexpected_action:{','.join(extra)}")
        if missing:
            failures.append(f"{sid}:missing_action:{','.join(missing)}")
        resources = _resource_list(statement)
        for resource in resources:
            if "raw/partial/" in resource:
                failures.append(f"{sid}:partial_prefix_allowed")
            if sid == APP_VERIFY_SID and resource.endswith("/*"):
                failures.append(f"{sid}:broad_raw_prefix_read")
            if not resource.startswith(f"arn:aws:s3:::{bucket}/raw/v1/date=*/hour=*"):
                failures.append(f"{sid}:resource_prefix_drift")
        if sorted(resources) != expected_resources:
            failures.append(f"{sid}:resource_scope_drift")

    if verify_network:
        try:
            vpc_id = _stack_resource_physical_id(STAGE0_STACK, "VPC")
            route_table_id = _stack_resource_physical_id(
                STAGE0_STACK, STAGE0_PUBLIC_ROUTE_TABLE_LOGICAL
            )
            endpoint_id = _stack_output(RAW_ARCHIVE_STACK, "QaRawArchiveS3EndpointId")
            failures.extend(
                _verify_s3_gateway_endpoint(
                    vpc_id=vpc_id,
                    route_table_ids=[route_table_id],
                    endpoint_id=endpoint_id,
                )
            )
        except RuntimeError as exc:
            failures.append(f"network_contract:{exc}")

    status = "applied" if not failures else "drift"
    return {
        "ok": not failures,
        "status": status,
        "bucket": bucket,
        "app_role_arn": app_role_arn,
        "failures": failures,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--json", action="store_true")
    parser.add_argument(
        "--edge-qa-boundary",
        action="store_true",
        help="read prod raw archive and QA Bundle policies and verify the shared edge-role deny",
    )
    args = parser.parse_args()

    account_id = _account_id()
    if args.edge_qa_boundary:
        verdict = verify_live_edge_qa_boundary(account_id)
    else:
        bucket = f"tokenkey-prod-qa-raw-archive-{account_id}"
        app_role_arn = resolve_app_role_arn()
        verdict = evaluate(bucket=bucket, app_role_arn=app_role_arn, verify_network=True)
    print(json.dumps(verdict, ensure_ascii=True, sort_keys=True))
    return 0 if verdict["ok"] else 2


if __name__ == "__main__":
    raise SystemExit(main())
