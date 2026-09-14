#!/usr/bin/env python3
"""Resolve SSM ``--instance-ids`` targets for Stage0 edges (Lightsail managed instances)."""

from __future__ import annotations

import argparse
import json
import pathlib
import subprocess
import sys
from dataclasses import dataclass

# When this file is importlib-loaded from ops/anthropic, sibling imports need the
# stage0 directory on sys.path (script-dir is not auto-prepended for that case).
_STAGE0 = pathlib.Path(__file__).resolve().parent
if str(_STAGE0) not in sys.path:
    sys.path.insert(0, str(_STAGE0))

from edge_routing_matrix import load_lightsail_targets, resolve_route_tab


@dataclass(frozen=True)
class EdgeExecutionIdentity:
    edge_id: str
    routing: str  # lightsail
    region: str
    instance_id: str
    domain: str
    ec2_stack: str
    ssm_prefix: str


def cfn_resolve_instance_id(region: str, stack: str) -> str:
    try:
        out = subprocess.check_output(
            [
                "aws",
                "cloudformation",
                "describe-stacks",
                "--region",
                region,
                "--stack-name",
                stack,
                "--query",
                "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue",
                "--output",
                "text",
            ],
            text=True,
        ).strip()
    except subprocess.CalledProcessError as e:
        raise SystemExit(f"describe-stacks failed for {stack}/{region}: {e}") from e
    if not out or out == "None":
        try:
            out = subprocess.check_output(
                [
                    "aws",
                    "cloudformation",
                    "describe-stack-resources",
                    "--region",
                    region,
                    "--stack-name",
                    stack,
                    "--query",
                    "StackResources[?ResourceType=='AWS::EC2::Instance'].PhysicalResourceId | [0]",
                    "--output",
                    "text",
                ],
                text=True,
            ).strip()
        except subprocess.CalledProcessError as e:
            raise SystemExit(
                f"describe-stack-resources fallback failed for {stack}/{region}: {e}",
            ) from e
    if not out or out == "None":
        raise SystemExit(f"no InstanceId resolvable for stack {stack}/{region}")
    return out


def ssm_parameter_managed_instance_id(region: str, ssm_prefix: str) -> str:
    name = f"{ssm_prefix.rstrip('/')}/ssm_managed_instance_id"
    try:
        out = subprocess.check_output(
            [
                "aws",
                "ssm",
                "get-parameter",
                "--region",
                region,
                "--name",
                name,
                "--query",
                "Parameter.Value",
                "--output",
                "text",
            ],
            text=True,
        ).strip()
    except subprocess.CalledProcessError as e:
        raise SystemExit(f"ssm get-parameter failed for {name} ({region}): {e}") from e
    if not out or out == "None":
        raise SystemExit(f"missing or empty SSM parameter {name}")
    return out


def resolve_edge_execution_identity(
    repo_root: pathlib.Path | str,
    edge_id: str,
    *,
    platform: str = "auto",
) -> EdgeExecutionIdentity:
    root = pathlib.Path(repo_root).resolve()
    eid = edge_id.strip()
    mode, region, _ = resolve_route_tab(
        root,
        eid,
        platform=platform,  # type: ignore[arg-type]
    )
    target = load_lightsail_targets(root)[eid]
    prefix = str(target.get("ssm_prefix") or "")
    domain = str(target.get("domain") or "")
    if not prefix or not domain:
        raise SystemExit(f"lightsail matrix entry {eid} missing ssm_prefix/domain")
    return EdgeExecutionIdentity(
        edge_id=eid, routing=mode, region=region,
        instance_id=ssm_parameter_managed_instance_id(region, prefix),
        domain=domain, ec2_stack="", ssm_prefix=prefix,
    )


def main() -> int:
    ap = argparse.ArgumentParser(
        description="Print REGION + INSTANCE_ID for SSM RunCommand on a Stage0 edge.",
    )
    ap.add_argument("--repo-root", default=".", help="git repo root (defaults to cwd)")
    ap.add_argument("--edge-id", required=True)
    ap.add_argument(
        "--platform",
        default="auto",
        choices=("auto", "ec2", "lightsail"),
    )
    ap.add_argument(
        "--format",
        choices=("env", "json"),
        default="env",
        help="env: export-friendly KEY=val lines; json: one object",
    )
    ns = ap.parse_args()
    try:
        ident = resolve_edge_execution_identity(
            ns.repo_root,
            ns.edge_id,
            platform=ns.platform,
        )
    except SystemExit as exc:
        print(f"::error::{exc}", file=sys.stderr)
        return int(exc.code) if isinstance(exc.code, int) else 2
    if ns.format == "json":
        print(
            json.dumps(
                {
                    "edge_id": ident.edge_id,
                    "routing": ident.routing,
                    "region": ident.region,
                    "instance_id": ident.instance_id,
                    "domain": ident.domain,
                    "ec2_stack": ident.ec2_stack,
                    "ssm_prefix": ident.ssm_prefix,
                },
                indent=2,
                ensure_ascii=False,
            ),
        )
        return 0
    safe_region = ident.region.replace("'", "'\"'\"'")
    safe_iid = ident.instance_id.replace("'", "'\"'\"'")
    print(f"REGION='{safe_region}'")
    print(f"INSTANCE_ID='{safe_iid}'")
    print(f"EDGE_ROUTING='{ident.routing}'")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
