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
STACK = "tokenkey-prod-stage0"
RAW_ARCHIVE_STACK = "tokenkey-prod-qa-raw-archive"

APP_WRITE_SID = "AllowAppInstanceRoleWriteRaw"
APP_VERIFY_SID = "AllowAppInstanceRoleVerifyRaw"
FORBIDDEN_APP_ACTIONS = {"s3:ListBucket", "s3:ListBucketMultipartUploads", "s3:GetBucketLocation"}
EXPECTED_SUFFIXES = [
    "commit.json",
    "manifest.json",
    "records.parquet",
    "evidence.pack",
    "evidence-index.jsonl.zst",
    "orphan-evidence-index.jsonl.zst",
]


def _aws_json(args: list[str]) -> dict[str, Any]:
    completed = subprocess.run(args, capture_output=True, text=True, check=False)
    if completed.returncode != 0:
        raise RuntimeError((completed.stderr or completed.stdout or "aws failed").strip()[:600])
    payload = json.loads(completed.stdout)
    if not isinstance(payload, dict):
        raise RuntimeError("aws json is not an object")
    return payload


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


def evaluate(
    *,
    bucket: str,
    app_role_arn: str,
    statements: list[dict[str, Any]] | None = None,
) -> dict[str, Any]:
    failures: list[str] = []
    if statements is None:
        try:
            statements = _live_bucket_policy(bucket)
        except RuntimeError as exc:
            return {"ok": False, "status": "unknown", "failures": [str(exc)], "bucket": bucket}

    app = _statement_for_principal(statements, app_role_arn)
    missing_sids = {APP_WRITE_SID, APP_VERIFY_SID} - set(app)
    if missing_sids:
        failures.extend(f"missing_app_sid:{sid}" for sid in sorted(missing_sids))

    for sid, statement in app.items():
        actions = statement.get("Action")
        action_set = {actions} if isinstance(actions, str) else set(actions or [])
        forbidden = sorted(action_set & FORBIDDEN_APP_ACTIONS)
        if forbidden:
            failures.append(f"{sid}:forbidden_action:{','.join(forbidden)}")
        resources = statement.get("Resource")
        resource_list = resources if isinstance(resources, list) else [resources]
        for resource in resource_list:
            if not isinstance(resource, str):
                failures.append(f"{sid}:invalid_resource")
                continue
            if "raw/partial/" in resource:
                failures.append(f"{sid}:partial_prefix_allowed")
            if sid == APP_VERIFY_SID and resource.endswith("/*"):
                failures.append(f"{sid}:broad_raw_prefix_read")

    if APP_WRITE_SID in app and APP_VERIFY_SID in app:
        write_resources = app[APP_WRITE_SID].get("Resource")
        verify_resources = app[APP_VERIFY_SID].get("Resource")
        write_list = write_resources if isinstance(write_resources, list) else [write_resources]
        verify_list = verify_resources if isinstance(verify_resources, list) else [verify_resources]
        for statement_resources in (write_list, verify_list):
            suffixes = [resource.rsplit("/", 1)[-1] for resource in statement_resources if isinstance(resource, str)]
            if suffixes != EXPECTED_SUFFIXES:
                failures.append("app_suffix_scope_drift")
                break
            if not all("raw/v1/date=*/hour=*" in resource for resource in statement_resources if isinstance(resource, str)):
                failures.append("app_hour_prefix_drift")

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
    args = parser.parse_args()

    account_id = _account_id()
    bucket = f"tokenkey-prod-qa-raw-archive-{account_id}"
    app_role_arn = resolve_app_role_arn()
    verdict = evaluate(bucket=bucket, app_role_arn=app_role_arn)
    print(json.dumps(verdict, ensure_ascii=True, sort_keys=True))
    return 0 if verdict["ok"] else 2


if __name__ == "__main__":
    raise SystemExit(main())
