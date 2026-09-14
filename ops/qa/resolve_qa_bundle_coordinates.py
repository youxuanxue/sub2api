#!/usr/bin/env python3
"""Read QA producer coordinates without coupling gateway deploys to worker health."""
from __future__ import annotations

import argparse
import json
import re
import subprocess
from pathlib import Path


def coordinates(stack: dict, region: str) -> dict[str, str]:
    stacks = stack.get("Stacks", [])
    if len(stacks) != 1:
        raise ValueError("expected exactly one QA stack")
    outputs = {row["OutputKey"]: row["OutputValue"] for row in stacks[0].get("Outputs", [])}
    bucket = outputs.get("QaBundleBucketName", "")
    queue = outputs.get("QaBundleQueueUrl", "")
    if not re.fullmatch(r"[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]", bucket):
        raise ValueError("QA stack has no valid Bundle bucket")
    if not re.fullmatch(r"https://sqs\." + re.escape(region)
                        + r"\.amazonaws\.com/[0-9]{12}/[A-Za-z0-9_-]+(?:\.fifo)?", queue):
        raise ValueError("QA stack has no valid Bundle queue in the deployment region")
    return {"bucket": bucket, "queue_url": queue}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--stack", required=True)
    parser.add_argument("--region", required=True)
    parser.add_argument("--github-output", type=Path, required=True)
    args = parser.parse_args()
    response = subprocess.check_output([
        "aws", "cloudformation", "describe-stacks", "--stack-name", args.stack,
        "--region", args.region, "--output", "json",
    ], text=True)
    values = coordinates(json.loads(response), args.region)
    with args.github_output.open("a") as stream:
        for key, value in values.items():
            stream.write(f"{key}={value}\n")


if __name__ == "__main__":
    main()
