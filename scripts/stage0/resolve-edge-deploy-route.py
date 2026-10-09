#!/usr/bin/env python3
"""Resolve canonical Edge deploy workflow + confirm token for gh dispatch.

Uses Lightsail / Hetzner matrices via ``edge_routing_matrix``. stdout is JSON
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
    edge_hetzner_deployable,
    load_hetzner_targets,
    load_lightsail_targets,
    resolve_route_tab,
)


def _fail(message: str, code: int = 1) -> None:
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(code)


def main() -> int:
    parser = argparse.ArgumentParser(description="Resolve Edge deploy workflow route.")
    parser.add_argument("--edge-id", required=True)
    parser.add_argument(
        "--platform",
        choices=("auto", "lightsail", "hetzner"),
        default="auto",
        help="Force matrix platform (default: auto — prefer deployable Hetzner).",
    )
    parser.add_argument(
        "--allow-planned",
        action="store_true",
        help="Allow non-deployable Hetzner/Lightsail rows when --platform is explicit.",
    )
    parser.add_argument("--json", action="store_true", help="Emit JSON on stdout.")
    args = parser.parse_args()

    edge_id = args.edge_id.strip()
    if not edge_id:
        _fail("edge-id is required")

    platform_pref = args.platform
    if platform_pref == "auto" and args.allow_planned:
        _fail("--allow-planned requires explicit --platform lightsail|hetzner")

    transport, region, _ = resolve_route_tab(REPO_ROOT, edge_id, platform_pref)

    if transport == "hetzner":
        target = load_hetzner_targets(REPO_ROOT).get(edge_id)
        if target is None:
            _fail(f"edge_id {edge_id} missing from Hetzner matrix")
        if not edge_hetzner_deployable(target) and not (
            platform_pref == "hetzner" and args.allow_planned
        ):
            _fail(
                f"edge_id {edge_id} is not deployable in the Hetzner matrix "
                "(pass --platform hetzner --allow-planned for Phase-1 validate)"
            )
        instance_name = str(target.get("instance_name") or "")
        if not instance_name:
            _fail(f"edge_id {edge_id} missing instance_name in hetzner matrix")
        payload = {
            "edge_id": edge_id,
            "platform": "hetzner",
            "region": region,
            "workflow_file": "deploy-edge-hetzner-stage0.yml",
            "confirm_flag": "confirm_instance",
            "confirm_value": instance_name,
        }
    else:
        target = load_lightsail_targets(REPO_ROOT).get(edge_id)
        if target is None:
            _fail(f"edge_id {edge_id} missing from Lightsail matrix")
        if not edge_deployable(target) and not (
            platform_pref == "lightsail" and args.allow_planned
        ):
            _fail(f"edge_id {edge_id} is not deployable in the Lightsail matrix")
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
