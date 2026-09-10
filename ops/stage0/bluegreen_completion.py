#!/usr/bin/env python3
"""Observe cutover early, then join the same SSM deployment after gateway checks."""
from __future__ import annotations

import argparse
import base64
import json
import os
import re
import subprocess
import time

import ssm_execution


def invocation(instance: str, command: str) -> dict:
    result = subprocess.run(["aws", "--region", ssm_execution.PROD_REGION, "ssm", "get-command-invocation",
                             "--instance-id", instance, "--command-id", command, "--output", "json"],
                            capture_output=True, text=True)
    if result.returncode:
        if "InvocationDoesNotExist" in result.stderr:
            return {"Status": "Pending"}
        raise RuntimeError("cannot inspect deployment command: " + result.stderr)
    return json.loads(result.stdout)


def require_success(value: dict) -> None:
    if value.get("Status") != "Success" or value.get("ResponseCode") != 0:
        raise RuntimeError("deployment did not finish successfully: " + value.get("Status", "unknown")
                           + "\n" + value.get("StandardErrorContent", "")[-2000:])


def validate_receipt(receipt: dict, token: str, tag: str) -> str:
    timestamp = receipt.get("cutover_at", "")
    if (receipt.get("token") != token or receipt.get("tag") != tag
            or not re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ", timestamp)):
        raise ValueError("cutover receipt does not match this deployment")
    return timestamp


def wait(instance: str, command: str, token: str, tag: str, phase: str, timeout: int) -> str | None:
    if not re.fullmatch(r"[a-f0-9]{32}", token):
        raise ValueError("invalid deployment token")
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        state = invocation(instance, command)
        if state.get("Status") not in {"Pending", "InProgress", "Delayed"}:
            require_success(state)
            if phase == "complete":
                return None
        if phase == "cutover":
            # The command-specific receipt avoids both stale timestamps and SSM's
            # buffered/truncated output while the deployment is still draining.
            script = ("set -euo pipefail\n"
                      f"if [ -f /var/lib/tokenkey/bluegreen-cutover-{token}.json ]; then "
                      f"cat /var/lib/tokenkey/bluegreen-cutover-{token}.json; else echo '{{}}'; fi\n")
            receipt = json.loads(ssm_execution.run_shell_b64(instance, base64.b64encode(script.encode()).decode(),
                                                           "read current deployment cutover receipt"))
            if receipt:
                return validate_receipt(receipt, token, tag)
            if state.get("Status") == "Success":
                raise RuntimeError("deployment succeeded without its cutover receipt")
        time.sleep(3)
    raise TimeoutError(f"deployment {phase} wait expired; host lock and pending drain remain authoritative")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("cutover", "complete"))
    parser.add_argument("--instance-id", required=True)
    parser.add_argument("--command-id", required=True)
    parser.add_argument("--token", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--timeout", type=int, default=1200)
    args = parser.parse_args()
    ssm_execution.PROD_REGION = os.environ.get("AWS_REGION", "us-east-1")
    timestamp = wait(args.instance_id, args.command_id, args.token, args.tag, args.phase, args.timeout)
    if timestamp:
        print("cutover_at=" + timestamp)
        if os.environ.get("GITHUB_OUTPUT"):
            with open(os.environ["GITHUB_OUTPUT"], "a") as stream:
                stream.write("cutover_at=" + timestamp + "\n")
    else:
        print("bluegreen completion: old requests drained and deployment command succeeded")


if __name__ == "__main__":
    main()
