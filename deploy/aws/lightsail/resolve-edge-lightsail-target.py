#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
DEFAULT_MATRIX = REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json"


def fail(message: str) -> None:
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(1)


def gha_quote(value: object) -> str:
    return str(value).replace("%", "%25").replace("\n", "%0A").replace("\r", "%0D")


def load_matrix(path: str) -> dict:
    matrix_path = pathlib.Path(path)
    if not matrix_path.is_file():
        fail(f"lightsail edge matrix not found: {matrix_path}")
    return json.loads(matrix_path.read_text(encoding="utf-8"))


def resolve_target(
    data: dict,
    edge_id: str,
    *,
    confirm_instance: str = "",
    allow_planned: bool = False,
) -> dict:
    targets = data.get("targets") or {}
    target = targets.get(edge_id)
    if target is None:
        fail(f"unknown edge_id {edge_id}; known: {', '.join(sorted(targets))}")

    deployable = bool(target.get("deployable"))
    if not deployable and not allow_planned:
        fail(
            f"edge_id {edge_id} is planned but not deployable; "
            "set deployable=true in deploy/aws/lightsail/edge-targets-lightsail.json"
        )

    instance_name = str(target.get("instance_name") or "")
    if confirm_instance and confirm_instance != instance_name:
        fail(f"confirm_instance mismatch: got {confirm_instance}, expected {instance_name}")

    required = [
        "profile",
        "lightsail_region",
        "domain",
        "instance_name",
        "static_ip_name",
        "bundle_id",
        "blueprint_id",
        "availability_zone",
        "ssm_prefix",
        "monthly_budget_usd",
    ]
    missing = [key for key in required if key not in target or target[key] in (None, "")]
    if missing:
        fail(f"edge_id {edge_id} missing required fields: {', '.join(missing)}")

    default_profile = str(data.get("default_profile") or "edge-lightsail-minimal")
    profile = str(target.get("profile") or "")
    if profile != default_profile:
        fail(f"edge_id {edge_id} profile {profile} != default {default_profile}")

    budget = int(target.get("monthly_budget_usd", 0))
    max_budget = int(data.get("max_monthly_budget_usd", 12))
    if budget > max_budget:
        fail(f"edge_id {edge_id} budget ${budget} exceeds max ${max_budget}")

    return {
        "edge_id": edge_id,
        "deployable": str(deployable).lower(),
        "profile": profile,
        "lightsail_region": target["lightsail_region"],
        "ec2_equivalent_region": target.get("ec2_equivalent_region", target["lightsail_region"]),
        "availability_zone": target["availability_zone"],
        "domain": target["domain"],
        "instance_name": instance_name,
        "static_ip_name": target["static_ip_name"],
        "bundle_id": target["bundle_id"],
        "blueprint_id": target["blueprint_id"],
        "monthly_budget_usd": budget,
        "ssm_prefix": target["ssm_prefix"],
        "purpose": target.get("purpose", ""),
    }


def write_outputs(path: str, outputs: dict) -> None:
    if not path:
        return
    with open(path, "a", encoding="utf-8") as fh:
        for key, value in outputs.items():
            fh.write(f"{key}={gha_quote(value)}\n")


def main() -> int:
    parser = argparse.ArgumentParser(description="Resolve a TokenKey Edge Lightsail target.")
    parser.add_argument("--edge-id", required=True)
    parser.add_argument("--confirm-instance", default="")
    parser.add_argument("--matrix", default=str(DEFAULT_MATRIX))
    parser.add_argument("--allow-planned", action="store_true")
    parser.add_argument("--github-output", default="")
    args = parser.parse_args()

    data = load_matrix(args.matrix)
    outputs = resolve_target(
        data,
        args.edge_id,
        confirm_instance=args.confirm_instance,
        allow_planned=args.allow_planned,
    )

    for key, value in outputs.items():
        print(f"{key}={value}")

    write_outputs(args.github_output, outputs)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
