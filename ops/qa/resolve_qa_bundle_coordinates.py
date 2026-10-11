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


def hetzner_coordinates(manifest: dict) -> dict[str, str]:
    expected = {"schema_version", "provider", "region", "endpoint", "bucket", "prefix", "archive_bucket", "archive_prefix"}
    if set(manifest) != expected or manifest["schema_version"] != 1 or manifest["provider"] != "hetzner":
        raise ValueError("unsupported QA producer manifest")
    region = manifest["region"]
    if region not in ("fsn1", "nbg1", "hel1") or manifest["endpoint"] != f"https://{region}.your-objectstorage.com":
        raise ValueError("invalid Hetzner storage endpoint")
    for key in ("bucket", "archive_bucket"):
        if not isinstance(manifest[key], str) or not re.fullmatch(r"[a-z0-9][a-z0-9-]{1,61}[a-z0-9]", manifest[key]):
            raise ValueError("invalid QA bucket")
    if manifest["bucket"] == manifest["archive_bucket"]:
        raise ValueError("raw and Bundle buckets must be separate")
    if manifest["prefix"] != "user-qa" or manifest["archive_prefix"] != "raw/v1":
        raise ValueError("QA logical prefixes must remain stable")
    return {key: manifest[key] for key in ("region", "endpoint", "bucket", "prefix", "archive_bucket", "archive_prefix")} | {
        "queue_driver": "postgres", "queue_url": "",
        "queue_postgres_dsn_file": "/run/secrets/tokenkey-qa/queue.dsn",
        "credentials_file": "/run/secrets/tokenkey-qa/bundle.json",
        "archive_credentials_file": "/run/secrets/tokenkey-qa/raw.json",
        "force_path_style": "true",
    }


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--provider", choices=("aws", "hetzner"), default="aws")
    parser.add_argument("--manifest", type=Path)
    parser.add_argument("--stack")
    parser.add_argument("--region")
    parser.add_argument("--github-output", type=Path, required=True)
    args = parser.parse_args()
    if args.provider == "hetzner":
        if args.manifest is None:
            parser.error("Hetzner requires a producer manifest")
        values = hetzner_coordinates(json.loads(args.manifest.read_text()))
    else:
        if not args.stack or not args.region:
            parser.error("AWS requires --stack and --region")
        response = subprocess.check_output([
            "aws", "cloudformation", "describe-stacks", "--stack-name", args.stack,
            "--region", args.region, "--output", "json",
        ], text=True)
        values = coordinates(json.loads(response), args.region) | {
            "queue_driver": "sqs", "queue_postgres_dsn_file": "", "credentials_file": "",
            "region": args.region, "prefix": "user-qa", "endpoint": "", "force_path_style": "false",
        }
    with args.github_output.open("a") as stream:
        for key, value in values.items():
            stream.write(f"{key}={value}\n")


if __name__ == "__main__":
    main()
