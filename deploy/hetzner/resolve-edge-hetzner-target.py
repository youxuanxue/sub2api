#!/usr/bin/env python3
"""Resolve a TokenKey Edge Hetzner target from edge-targets-hetzner.json.

Phase-1: matrix validation only. Live rollout still routes via Lightsail until
deployable=true flips and dispatch wiring lands (see approved migration doc).
"""
from __future__ import annotations

import argparse
import json
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
DEFAULT_MATRIX = REPO_ROOT / "deploy/hetzner/edge-targets-hetzner.json"

ALLOWED_LOCATIONS = frozenset({"fsn1"})
ALLOWED_SERVER_TYPES = frozenset({"cax21"})
REQUIRED_ARCHITECTURE = "arm"


def fail(message: str) -> None:
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(1)


def gha_quote(value: object) -> str:
    return str(value).replace("%", "%25").replace("\n", "%0A").replace("\r", "%0D")


def load_matrix(path: str) -> dict:
    matrix_path = pathlib.Path(path)
    if not matrix_path.is_file():
        fail(f"hetzner edge matrix not found: {matrix_path}")
    return json.loads(matrix_path.read_text(encoding="utf-8"))


def resolve_target(
    data: dict,
    edge_id: str,
    *,
    confirm_instance: str = "",
    allow_planned: bool = False,
) -> dict:
    if str(data.get("platform") or "") != "hetzner":
        fail("matrix platform must be hetzner")

    targets = data.get("targets") or {}
    target = targets.get(edge_id)
    if target is None:
        fail(f"unknown edge_id {edge_id}; known: {', '.join(sorted(targets))}")

    deployable = bool(target.get("deployable"))
    if not deployable and not allow_planned:
        fail(
            f"edge_id {edge_id} is planned but not deployable; "
            "set deployable=true only after Phase-2 gates (see "
            "docs/approved/hetzner-cloud-full-migration.md §17)"
        )

    instance_name = str(target.get("instance_name") or "")
    if confirm_instance and confirm_instance != instance_name:
        fail(f"confirm_instance mismatch: got {confirm_instance}, expected {instance_name}")

    required = [
        "profile",
        "location",
        "server_type",
        "architecture",
        "image",
        "domain",
        "staging_domain",
        "instance_name",
        "floating_ip_name",
        "ssh_key_name",
        "ssm_prefix",
        "swap_gib",
    ]
    missing = [key for key in required if key not in target or target[key] in (None, "")]
    if missing:
        fail(f"edge_id {edge_id} missing required fields: {', '.join(missing)}")

    location = str(target["location"])
    server_type = str(target["server_type"])
    architecture = str(target["architecture"])

    allowed_locations = set(data.get("allowed_locations") or ALLOWED_LOCATIONS)
    allowed_types = set(data.get("allowed_server_types") or ALLOWED_SERVER_TYPES)
    required_arch = str(data.get("required_architecture") or REQUIRED_ARCHITECTURE)

    if location not in allowed_locations:
        fail(f"edge_id {edge_id} location {location} not in {sorted(allowed_locations)}")
    if server_type not in allowed_types:
        fail(f"edge_id {edge_id} server_type {server_type} not in {sorted(allowed_types)}")
    if architecture != required_arch:
        fail(
            f"edge_id {edge_id} architecture {architecture} != required {required_arch} "
            "(E0: arm64 fleet)"
        )

    default_profile = str(data.get("default_profile") or "edge-hetzner-cax21")
    profile = str(target.get("profile") or "")
    if profile != default_profile:
        fail(f"edge_id {edge_id} profile {profile} != default {default_profile}")

    ssm_prefix = str(target["ssm_prefix"])
    if not ssm_prefix.startswith("/tokenkey/hetzner/"):
        fail(f"edge_id {edge_id} ssm_prefix must start with /tokenkey/hetzner/")

    return {
        "edge_id": edge_id,
        "platform": "hetzner",
        "deployable": str(deployable).lower(),
        "profile": profile,
        "swap_gib": int(target["swap_gib"]),
        "location": location,
        "server_type": server_type,
        "architecture": architecture,
        "image": target["image"],
        "domain": target["domain"],
        "staging_domain": target["staging_domain"],
        "instance_name": instance_name,
        "floating_ip_name": target["floating_ip_name"],
        "ssh_key_name": target["ssh_key_name"],
        "ssm_prefix": ssm_prefix,
        "ssm_hybrid_role_name": f"tokenkey-hetzner-ssm-hybrid-{edge_id}",
        "purpose": target.get("purpose", ""),
    }


def write_outputs(path: str, outputs: dict) -> None:
    if not path:
        return
    with open(path, "a", encoding="utf-8") as fh:
        for key, value in outputs.items():
            fh.write(f"{key}={gha_quote(value)}\n")


def main() -> int:
    parser = argparse.ArgumentParser(description="Resolve a TokenKey Edge Hetzner target.")
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
