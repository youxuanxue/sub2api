#!/usr/bin/env python3
"""Run guarded QA maintenance on prod via SSM."""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
import time

PROD_REGION = "us-east-1"
HOST_RUNNER = "/usr/local/bin/tokenkey-qa-maintenance.sh"
HOST_RECEIPT = "/var/lib/tokenkey/qa-maintenance-last-run.json"
POLL_ATTEMPTS = 100
POLL_INTERVAL_SECONDS = 3


class QAMaintenanceError(RuntimeError):
    pass


def _aws_json(args: list[str]) -> dict:
    completed = subprocess.run(args, capture_output=True, text=True, check=False)
    if completed.returncode != 0:
        raise QAMaintenanceError((completed.stderr or completed.stdout or "aws failed").strip()[:600])
    payload = json.loads(completed.stdout)
    if not isinstance(payload, dict):
        raise QAMaintenanceError("aws json is not an object")
    return payload


def _resolve_instance() -> str:
    payload = _aws_json(
        [
            "aws",
            "cloudformation",
            "describe-stacks",
            "--region",
            PROD_REGION,
            "--stack-name",
            "tokenkey-prod-stage0",
            "--output",
            "json",
        ]
    )
    stacks = payload.get("Stacks")
    if not isinstance(stacks, list) or not stacks:
        raise QAMaintenanceError("prod stack missing")
    for item in stacks[0].get("Outputs", []):
        if isinstance(item, dict) and item.get("OutputKey") == "InstanceId":
            value = item.get("OutputValue")
            if isinstance(value, str) and value.startswith("i-"):
                return value
    raise QAMaintenanceError("prod InstanceId missing")


def _remote_command() -> str:
    return (
        "set -uo pipefail; "
        f"sudo {HOST_RUNNER} --trigger=operator; runner_rc=$?; "
        f"sudo cat {HOST_RECEIPT}; exit $runner_rc"
    )


def _poll(command_id: str, instance_id: str) -> str:
    for _ in range(POLL_ATTEMPTS):
        payload = _aws_json(
            [
                "aws",
                "ssm",
                "get-command-invocation",
                "--region",
                PROD_REGION,
                "--command-id",
                command_id,
                "--instance-id",
                instance_id,
                "--output",
                "json",
            ]
        )
        status = payload.get("Status")
        if status in {"Pending", "InProgress", "Delayed"}:
            time.sleep(POLL_INTERVAL_SECONDS)
            continue
        if status != "Success" or payload.get("ResponseCode") != 0:
            detail = str(payload.get("StandardErrorContent") or "").strip()
            raise QAMaintenanceError(f"ssm failed status={status!r}: {detail[:600]}")
        stdout = payload.get("StandardOutputContent")
        if not isinstance(stdout, str):
            raise QAMaintenanceError("ssm success without stdout")
        return stdout
    raise QAMaintenanceError("ssm did not finish")


def _validate_receipt(stdout: str) -> dict:
    lines = [line.strip() for line in stdout.splitlines() if line.strip()]
    payload = json.loads(lines[-1])
    if (
        not isinstance(payload, dict)
        or payload.get("schema_version") != "qa-maintenance-runner-v1"
        or payload.get("trigger") != "operator"
        or payload.get("runner_exit_code") != 0
        or payload.get("child_exit_code") != 0
        or payload.get("deletion_authorized") is not False
        or not isinstance(payload.get("run_id"), str)
        or not payload.get("run_id")
    ):
        raise QAMaintenanceError("remote receipt failed safety validation")
    normal = payload.get("normal")
    if (
        not isinstance(normal, dict)
        or normal.get("state") != "committed"
        or normal.get("restore_verified") is not True
        or normal.get("cleanup_eligible") is not False
    ):
        raise QAMaintenanceError("remote receipt lacks committed normal result")
    return payload


def run() -> dict:
    instance_id = _resolve_instance()
    payload = _aws_json(
        [
            "aws",
            "ssm",
            "send-command",
            "--region",
            PROD_REGION,
            "--instance-ids",
            instance_id,
            "--document-name",
            "AWS-RunShellScript",
            "--comment",
            "TokenKey QA maintenance",
            "--parameters",
            json.dumps({"commands": [_remote_command()]}, separators=(",", ":")),
            "--output",
            "json",
        ]
    )
    command_id = payload["Command"]["CommandId"]
    stdout = _poll(command_id, instance_id)
    return {
        "instance_id": instance_id,
        "command_id": command_id,
        "remote_receipt": _validate_receipt(stdout),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.parse_args()
    try:
        payload = run()
    except QAMaintenanceError as exc:
        print(f"qa maintenance refused: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(payload, ensure_ascii=True, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
