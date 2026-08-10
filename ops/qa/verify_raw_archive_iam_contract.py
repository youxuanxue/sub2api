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
