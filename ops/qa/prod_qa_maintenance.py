#!/usr/bin/env python3
"""Run guarded QA maintenance on prod via SSM."""

from __future__ import annotations

import argparse
import json
import pathlib
import shlex
import subprocess
import sys
import time

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "ops" / "lib"))
import resolve_app_container  # noqa: E402

PROD_REGION = "us-east-1"
CONFIRMATION = "tokenkey-prod-qa-maintenance-v1"
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


def _remote_command(backfill_once: bool) -> str:
    flag = "--qa-maintenance-once"
    if backfill_once:
        flag += " --qa-maintenance-backfill-once"
    script = "\n".join(
        [
            "set -euo pipefail",
            *resolve_app_container.remote_shell_snippet(docker="sudo docker"),
            'if [ -z "$APP_CONTAINER" ]; then echo "qa maintenance refused: ambiguous app container" >&2; exit 40; fi',
            f'sudo docker exec "$APP_CONTAINER" /app/sub2api {flag} --confirm {CONFIRMATION}',
        ]
    )
    return "sudo bash -c " + shlex.quote(script)


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
        or payload.get("ok") is not True
        or payload.get("deletion_authorized") is not False
        or payload.get("job_name") != "qa-maintenance"
    ):
        raise QAMaintenanceError("remote receipt failed safety validation")
    return payload


def run(backfill_once: bool = False) -> dict:
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
            json.dumps({"commands": [_remote_command(backfill_once)]}, separators=(",", ":")),
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
    parser.add_argument("--backfill-once", action="store_true")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    try:
        payload = run(backfill_once=args.backfill_once)
    except QAMaintenanceError as exc:
        print(f"qa maintenance refused: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(payload, ensure_ascii=True, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
