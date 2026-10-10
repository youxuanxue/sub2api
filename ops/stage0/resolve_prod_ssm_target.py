#!/usr/bin/env python3
"""Resolve Stage0 prod SSM control-plane target.

Default remains AWS CFN ``i-*`` (us-east-1) until an operator flips the cutover
flag. Hetzner Hybrid ``mi-*`` is opt-in only.

Resolution order for ``--target auto`` (default):

1. ``PROD_SSM_TARGET`` env if set to ``aws`` / ``hetzner``
2. SSM String param ``/tokenkey/prod/control-plane-ssm-target`` (us-east-1)
   when value is ``aws`` / ``hetzner``
3. Otherwise ``aws``

Hetzner instance id is resolved from SSM inventory by ComputerName matching
``deploy/hetzner/prod-target-hetzner.json`` ``instance_name`` (registration
region from matrix ``ssm_region``, default ``eu-west-2``).
"""
from __future__ import annotations

import argparse
import json
import os
import pathlib
import re
import subprocess
import sys
from typing import Any

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
DEFAULT_MATRIX = REPO_ROOT / "deploy/hetzner/prod-target-hetzner.json"
CUTOVER_PARAM = "/tokenkey/prod/control-plane-ssm-target"
AWS_REGION = "us-east-1"
AWS_STACK = "tokenkey-prod-stage0"
INSTANCE_RE = re.compile(r"^(?:i|mi)-[0-9a-f]{8,}$")


def fail(message: str) -> None:
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(1)


def gha_quote(value: object) -> str:
    return str(value).replace("%", "%25").replace("\n", "%0A").replace("\r", "%0D")


def aws_json(args: list[str]) -> Any:
    try:
        out = subprocess.check_output(["aws", *args, "--output", "json"], text=True)
    except subprocess.CalledProcessError as exc:
        fail(f"aws {' '.join(args)} failed: {exc}")
    if not out.strip():
        return None
    return json.loads(out)


def read_cutover_param() -> str:
    try:
        out = subprocess.check_output(
            [
                "aws",
                "ssm",
                "get-parameter",
                "--region",
                AWS_REGION,
                "--name",
                CUTOVER_PARAM,
                "--query",
                "Parameter.Value",
                "--output",
                "text",
            ],
            text=True,
            stderr=subprocess.DEVNULL,
        ).strip()
    except subprocess.CalledProcessError:
        return ""
    return out.lower()


def resolve_mode(explicit: str) -> str:
    if explicit and explicit != "auto":
        return explicit
    env = (os.environ.get("PROD_SSM_TARGET") or "").strip().lower()
    if env in ("aws", "hetzner"):
        return env
    if env and env != "auto":
        fail(f"PROD_SSM_TARGET must be auto|aws|hetzner (got {env!r})")
    param = read_cutover_param()
    if param in ("aws", "hetzner"):
        return param
    if param and param != "auto":
        fail(f"{CUTOVER_PARAM} must be aws|hetzner (got {param!r})")
    return "aws"


def resolve_aws(stack: str) -> dict[str, str]:
    rows = aws_json(
        [
            "cloudformation",
            "describe-stacks",
            "--region",
            AWS_REGION,
            "--stack-name",
            stack,
            "--query",
            "Stacks[0].Outputs",
        ]
    )
    if not isinstance(rows, list):
        fail(f"no outputs for stack {stack}")
    mapping = {
        str(r.get("OutputKey")): str(r.get("OutputValue") or "")
        for r in rows
        if isinstance(r, dict)
    }
    instance_id = mapping.get("InstanceId", "")
    api_url = mapping.get("ApiUrl", "")
    if not INSTANCE_RE.match(instance_id) or not instance_id.startswith("i-"):
        fail(f"invalid AWS InstanceId for {stack}: {instance_id!r}")
    if not api_url.startswith("https://"):
        fail(f"invalid ApiUrl for {stack}: {api_url!r}")
    return {
        "target": "aws",
        "instance_id": instance_id,
        "ssm_region": AWS_REGION,
        "api_url": api_url,
        "deploy_profile": "prod",
        "platform": "aws",
    }


def load_hetzner_matrix(path: pathlib.Path) -> dict[str, Any]:
    data = json.loads(path.read_text(encoding="utf-8"))
    target = data.get("target")
    if not isinstance(target, dict):
        fail(f"invalid hetzner prod matrix: {path}")
    return target


def resolve_hetzner(matrix_path: pathlib.Path) -> dict[str, str]:
    target = load_hetzner_matrix(matrix_path)
    name = str(target.get("instance_name") or "")
    region = str(target.get("ssm_region") or "eu-west-2")
    domain = str(target.get("domain") or "api.tokenkey.dev")
    if not name:
        fail("hetzner matrix missing instance_name")
    # List then match ComputerName — Hybrid activations do not expose a stable
    # Name tag filter across accounts the way EC2 does.
    info = aws_json(
        [
            "ssm",
            "describe-instance-information",
            "--region",
            region,
        ]
    )
    rows = (info or {}).get("InstanceInformationList") or []
    matches = [
        r
        for r in rows
        if str(r.get("ComputerName") or "") == name
        or str(r.get("Name") or "") == name
    ]
    if not matches:
        fail(f"no Online Hybrid instance with ComputerName/Name={name!r} in {region}")
    online = [r for r in matches if str(r.get("PingStatus") or "") == "Online"]
    chosen = online[0] if online else matches[0]
    instance_id = str(chosen.get("InstanceId") or "")
    if not instance_id.startswith("mi-") or not INSTANCE_RE.match(instance_id):
        fail(f"expected Hybrid mi-* for {name}, got {instance_id!r}")
    return {
        "target": "hetzner",
        "instance_id": instance_id,
        "ssm_region": region,
        "api_url": f"https://{domain}",
        "deploy_profile": "prod",
        "platform": "hetzner",
        "computer_name": name,
    }


def write_github_output(path: str, outputs: dict[str, str]) -> None:
    if not path:
        return
    with open(path, "a", encoding="utf-8") as fh:
        for key, value in outputs.items():
            fh.write(f"{key}={gha_quote(value)}\n")


def main() -> int:
    parser = argparse.ArgumentParser(description="Resolve Stage0 prod SSM target.")
    parser.add_argument(
        "--target",
        choices=("auto", "aws", "hetzner"),
        default="auto",
        help="auto respects PROD_SSM_TARGET / cutover SSM param; default aws",
    )
    parser.add_argument("--stack", default=AWS_STACK)
    parser.add_argument("--matrix", default="")
    parser.add_argument(
        "--format",
        choices=("kv", "instance-id", "json"),
        default="kv",
    )
    parser.add_argument("--github-output", default="")
    args = parser.parse_args()

    mode = resolve_mode(args.target)
    matrix = pathlib.Path(args.matrix) if args.matrix else DEFAULT_MATRIX
    if mode == "aws":
        outputs = resolve_aws(args.stack)
    else:
        outputs = resolve_hetzner(matrix)

    if args.format == "instance-id":
        print(outputs["instance_id"])
    elif args.format == "json":
        print(json.dumps(outputs, sort_keys=True))
    else:
        for key, value in outputs.items():
            print(f"{key}={value}")
    write_github_output(args.github_output, outputs)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
