#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
DEFAULT_MATRIX = REPO_ROOT / "deploy/aws/stage0/edge-targets.json"


def fail(message: str) -> None:
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(1)


def gha_quote(value: object) -> str:
    return str(value).replace("%", "%25").replace("\n", "%0A").replace("\r", "%0D")


def main() -> int:
    parser = argparse.ArgumentParser(description="Resolve a TokenKey Edge Stage0 target.")
    parser.add_argument("--edge-id", required=True)
    parser.add_argument("--confirm-stack", default="")
    parser.add_argument("--profile", default="")
    parser.add_argument("--matrix", default=str(DEFAULT_MATRIX))
    parser.add_argument("--allow-planned", action="store_true")
    parser.add_argument("--github-output", default="")
    args = parser.parse_args()

    matrix_path = pathlib.Path(args.matrix)
    if not matrix_path.is_file():
        fail(f"edge target matrix not found: {matrix_path}")

    data = json.loads(matrix_path.read_text(encoding="utf-8"))
    targets = data.get("targets") or {}
    target = targets.get(args.edge_id)
    if target is None:
        fail(f"unknown edge_id {args.edge_id}; known edges: {', '.join(sorted(targets))}")

    deployable = bool(target.get("deployable"))
    if not deployable and not args.allow_planned:
        fail(f"edge_id {args.edge_id} is planned but not deployable; validate uk1 first")

    profile = str(target.get("profile") or "")
    requested_profile = args.profile.strip()
    if requested_profile and requested_profile != profile:
        fail(f"profile mismatch for {args.edge_id}: requested {requested_profile}, matrix has {profile}")

    default_profile = str(data.get("default_profile") or "edge-minimal")
    if profile != default_profile:
        fail(f"edge_id {args.edge_id} profile {profile} is not the default allowed profile {default_profile}")

    budget = int(target.get("monthly_budget_usd", 0))
    max_budget = int(data.get("max_monthly_budget_usd", 16))
    if budget > max_budget:
        fail(f"edge_id {args.edge_id} budget ${budget} exceeds max ${max_budget}")

    stack = str(target.get("stack") or "")
    if args.confirm_stack and args.confirm_stack != stack:
        fail(f"confirm_stack mismatch: got {args.confirm_stack}, expected {stack}")

    required = [
        "region",
        "domain",
        "stack",
        "instance_type",
        "root_volume_gib",
        "data_volume_gib",
        "swap_gib",
        "snapshot_schedule",
        "monthly_budget_usd",
        "ssm_prefix",
    ]
    missing = [key for key in required if key not in target or target[key] in (None, "")]
    if missing:
        fail(f"edge_id {args.edge_id} missing required fields: {', '.join(missing)}")

    outputs = {
        "edge_id": args.edge_id,
        "deployable": str(deployable).lower(),
        "profile": profile,
        "region": target["region"],
        "domain": target["domain"],
        "stack": stack,
        "instance_type": target["instance_type"],
        "root_volume_gib": target["root_volume_gib"],
        "data_volume_gib": target["data_volume_gib"],
        "swap_gib": target["swap_gib"],
        "snapshot_schedule": target["snapshot_schedule"],
        "monthly_budget_usd": budget,
        "ssm_prefix": target["ssm_prefix"],
        "purpose": target.get("purpose", ""),
    }

    for key, value in outputs.items():
        print(f"{key}={value}")

    if args.github_output:
        with open(args.github_output, "a", encoding="utf-8") as fh:
            for key, value in outputs.items():
                fh.write(f"{key}={gha_quote(value)}\n")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
