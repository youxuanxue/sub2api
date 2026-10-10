#!/usr/bin/env python3
"""Resolve TokenKey prod Hetzner target from prod-target-hetzner.json."""
from __future__ import annotations

import argparse
import json
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
DEFAULT_MATRIX = REPO_ROOT / "deploy/hetzner/prod-target-hetzner.json"

ALLOWED_LOCATIONS = frozenset({"fsn1"})
ALLOWED_SERVER_TYPES = frozenset({"cax21"})
REQUIRED_ARCHITECTURE = "arm"


def fail(message: str) -> None:
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(1)


def gha_quote(value: object) -> str:
    return str(value).replace("%", "%25").replace("\n", "%0A").replace("\r", "%0D")


def load_prod_matrix(path: pathlib.Path) -> dict:
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict):
        fail(f"invalid prod matrix: {path}")
    if str(data.get("platform") or "") != "hetzner":
        fail("matrix platform must be hetzner")
    if str(data.get("role") or "") != "prod":
        fail("matrix role must be prod")
    target = data.get("target")
    if not isinstance(target, dict):
        fail("matrix target must be an object")
    return data


def resolve_target(
    data: dict,
    *,
    confirm_instance: str = "",
    allow_planned: bool = False,
) -> dict:
    target = dict(data["target"])
    deployable = bool(target.get("deployable"))
    if not deployable and not allow_planned:
        fail(
            "prod target is planned but not deployable; "
            "set deployable=true only after Phase-4 gates (see "
            "docs/approved/hetzner-cloud-full-migration.md)"
        )

    instance_name = str(target.get("instance_name") or "")
    if confirm_instance and confirm_instance != instance_name:
        fail(f"confirm_instance mismatch: got {confirm_instance}, expected {instance_name}")

    required = [
        "location",
        "server_type",
        "architecture",
        "image",
        "domain",
        "staging_domain",
        "instance_name",
        "ssh_key_name",
        "ssm_prefix",
        "swap_gib",
        "volume_name",
        "volume_size_gb",
        "volume_mount",
        "ssm_hybrid_role_name",
    ]
    missing = [key for key in required if key not in target or target[key] in (None, "")]
    if missing:
        fail(f"prod target missing required fields: {', '.join(missing)}")

    location = str(target["location"])
    server_type = str(target["server_type"])
    architecture = str(target["architecture"])
    volume_mount = str(target["volume_mount"])
    volume_size_gb = int(target["volume_size_gb"])

    allowed_locations = set(data.get("allowed_locations") or ALLOWED_LOCATIONS)
    allowed_types = set(data.get("allowed_server_types") or ALLOWED_SERVER_TYPES)
    required_arch = str(data.get("required_architecture") or REQUIRED_ARCHITECTURE)

    if location not in allowed_locations:
        fail(f"prod location {location} not in {sorted(allowed_locations)}")
    if server_type not in allowed_types:
        fail(f"prod server_type {server_type} not in {sorted(allowed_types)}")
    if architecture != required_arch:
        fail(f"prod architecture {architecture} != required {required_arch}")
    if volume_mount != "/var/lib/tokenkey":
        fail(f"prod volume_mount must be /var/lib/tokenkey (got {volume_mount})")
    if volume_size_gb < 40:
        fail(f"prod volume_size_gb must be >= 40 (got {volume_size_gb})")

    ssm_prefix = str(target["ssm_prefix"])
    if not ssm_prefix.startswith("/tokenkey/hetzner/prod"):
        fail(f"prod ssm_prefix must start with /tokenkey/hetzner/prod (got {ssm_prefix})")

    role_name = str(target["ssm_hybrid_role_name"])
    if role_name != "tokenkey-hetzner-ssm-hybrid-prod":
        fail(f"unexpected ssm_hybrid_role_name={role_name}")

    return {
        "role": "prod",
        "platform": "hetzner",
        "deployable": str(deployable).lower(),
        "profile": str(target.get("profile") or "prod-hetzner-cax21"),
        "swap_gib": int(target["swap_gib"]),
        "location": location,
        "server_type": server_type,
        "architecture": architecture,
        "image": target["image"],
        "domain": target["domain"],
        "staging_domain": target["staging_domain"],
        "instance_name": instance_name,
        "ssh_key_name": target["ssh_key_name"],
        "volume_name": target["volume_name"],
        "volume_size_gb": volume_size_gb,
        "volume_mount": volume_mount,
        "ssm_prefix": ssm_prefix,
        "ssm_region": str(target.get("ssm_region") or "eu-west-2"),
        "ssm_hybrid_role_name": role_name,
        "purpose": target.get("purpose", ""),
    }


def write_outputs(path: str, outputs: dict) -> None:
    if not path:
        return
    with open(path, "a", encoding="utf-8") as fh:
        for key, value in outputs.items():
            fh.write(f"{key}={gha_quote(value)}\n")


def main() -> int:
    parser = argparse.ArgumentParser(description="Resolve TokenKey prod Hetzner target.")
    parser.add_argument("--confirm-instance", default="")
    parser.add_argument("--matrix", default="")
    parser.add_argument("--allow-planned", action="store_true")
    parser.add_argument("--github-output", default="")
    args = parser.parse_args()

    path = pathlib.Path(args.matrix) if args.matrix else DEFAULT_MATRIX
    data = load_prod_matrix(path)
    outputs = resolve_target(
        data,
        confirm_instance=args.confirm_instance,
        allow_planned=args.allow_planned,
    )
    for key, value in outputs.items():
        print(f"{key}={value}")
    write_outputs(args.github_output, outputs)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
