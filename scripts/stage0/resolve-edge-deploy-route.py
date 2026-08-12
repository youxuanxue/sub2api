#!/usr/bin/env python3
"""Resolve canonical Edge deploy workflow + confirm token for gh dispatch.

Uses the active owner by default. Explicit EC2 may select a migration candidate.
"""
from __future__ import annotations

import argparse
import json
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT / "ops" / "stage0"))

from edge_routing_matrix import (  # noqa: E402
    edge_effective_deployable,
    load_lightsail_targets,
    resolve_route_tab,
)


def _fail(message: str, code: int = 1) -> None:
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(code)


def main() -> int:
    parser = argparse.ArgumentParser(description="Resolve Edge deploy workflow route.")
    parser.add_argument("--edge-id", required=True)
    parser.add_argument("--platform", choices=("auto", "ec2", "lightsail"), default="auto")
    parser.add_argument("--json", action="store_true", help="Emit JSON on stdout.")
    args = parser.parse_args()

    edge_id = args.edge_id.strip()
    if not edge_id:
        _fail("edge-id is required")

    ec2_path = REPO_ROOT / "deploy/aws/stage0/edge-targets.json"
    if not ec2_path.is_file():
        _fail(f"EC2 edge matrix not found: {ec2_path}")

    ec2_data = json.loads(ec2_path.read_text(encoding="utf-8"))
    ls_targets = load_lightsail_targets(REPO_ROOT)
    ec2_target = (ec2_data.get("targets") or {}).get(edge_id)
    ls_target = ls_targets.get(edge_id)

    if args.platform == "auto" and not edge_effective_deployable(ec2_target, ls_target):
        _fail(
            f"edge_id {edge_id} is not effectively deployable "
            "(set deployable=true in exactly one EC2 or Lightsail matrix)"
        )

    mode, region, stack = resolve_route_tab(REPO_ROOT, edge_id, args.platform)

    if mode == "lightsail":
        if not ls_target:
            _fail(f"edge_id {edge_id} resolved to lightsail but lightsail matrix entry missing")
        instance_name = str(ls_target.get("instance_name") or "")
        if not instance_name:
            _fail(f"edge_id {edge_id} missing instance_name in lightsail matrix")
        payload = {
            "edge_id": edge_id,
            "platform": "lightsail",
            "region": region,
            "workflow_file": "deploy-edge-lightsail-stage0.yml",
            "confirm_flag": "confirm_instance",
            "confirm_value": instance_name,
            "allow_migration_candidate": False,
        }
    else:
        if not ec2_target:
            _fail(f"edge_id {edge_id} resolved to EC2 but the matrix entry is missing")
        payload = {
            "edge_id": edge_id,
            "platform": "ec2",
            "region": region,
            "workflow_file": "deploy-edge-stage0.yml",
            "confirm_flag": "confirm_stack",
            "confirm_value": stack,
            "allow_migration_candidate": (
                ec2_target.get("deployable") is not True
                and ec2_target.get("migration_candidate") is True
            ),
        }

    if args.json:
        json.dump(payload, sys.stdout, separators=(",", ":"), sort_keys=True)
        sys.stdout.write("\n")
    else:
        for key, value in payload.items():
            rendered = str(value).lower() if isinstance(value, bool) else value
            print(f"{key}={rendered}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
