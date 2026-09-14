#!/usr/bin/env python3
"""Resolve canonical Edge deploy workflow + confirm token for gh dispatch.

Uses the canonical Lightsail ``edge_routing_matrix``. stdout is JSON
when ``--json`` is set; otherwise KEY=value lines for shell consumers.
"""
from __future__ import annotations

import argparse
import json
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT / "ops" / "stage0"))

from edge_routing_matrix import (  # noqa: E402
    edge_deployable,
    load_lightsail_targets,
    resolve_route_tab,
)


def _fail(message: str, code: int = 1) -> None:
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(code)


def main() -> int:
    parser = argparse.ArgumentParser(description="Resolve Edge deploy workflow route.")
    parser.add_argument("--edge-id", required=True)
    parser.add_argument("--json", action="store_true", help="Emit JSON on stdout.")
    args = parser.parse_args()

    edge_id = args.edge_id.strip()
    if not edge_id:
        _fail("edge-id is required")

    target = load_lightsail_targets(REPO_ROOT).get(edge_id)
    if not edge_deployable(target):
        _fail(f"edge_id {edge_id} is not deployable in the Lightsail matrix")
    _, region, _ = resolve_route_tab(REPO_ROOT, edge_id)
    instance_name = str(target.get("instance_name") or "")
    if not instance_name:
        _fail(f"edge_id {edge_id} missing instance_name in lightsail matrix")
    payload = {
        "edge_id": edge_id,
        "platform": "lightsail",
        "region": region,
        "workflow_file": "deploy-edge-lightsail-stage0.yml",
        "confirm_flag": "confirm_instance",
        "confirm_value": instance_name,
    }

    if args.json:
        json.dump(payload, sys.stdout, separators=(",", ":"), sort_keys=True)
        sys.stdout.write("\n")
    else:
        for key, value in payload.items():
            print(f"{key}={value}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
