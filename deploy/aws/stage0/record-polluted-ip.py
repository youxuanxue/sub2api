#!/usr/bin/env python3
"""Append an egress IP to deploy/aws/stage0/edge-polluted-ips.json (atomic replace).

Used by ops/lightsail/rotate-static-ip.sh after releasing a polluted Static IP.
EC2 rotations may call the same helper once workflow automation lands.

Exit codes:
  0 — entry recorded (or already present with same ip+region)
  2 — validation / duplicate conflict / I/O error
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import tempfile
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[3]
REGISTRY = REPO_ROOT / "deploy" / "aws" / "stage0" / "edge-polluted-ips.json"
IPV4 = re.compile(r"^(\d{1,3}\.){3}\d{1,3}$")


def fail(msg: str, code: int = 2) -> None:
    print(f"record-polluted-ip: {msg}", file=sys.stderr)
    sys.exit(code)


def validate_ip(ip: str) -> None:
    if not IPV4.match(ip):
        fail(f"invalid IPv4 address: {ip!r}")
    octets = [int(part) for part in ip.split(".")]
    if any(o < 0 or o > 255 for o in octets):
        fail(f"invalid IPv4 address: {ip!r}")


def load_registry(path: Path = REGISTRY) -> dict[str, Any]:
    if not path.exists():
        fail(f"missing registry: {path}")
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        fail(f"malformed JSON in {path}: {exc}")


def excluded_ips_for_region(data: dict[str, Any], region: str) -> set[str]:
    return {
        entry["ip"]
        for entry in data.get("polluted", [])
        if entry.get("region") == region and "ip" in entry
    }


def find_entry(data: dict[str, Any], ip: str, region: str) -> dict[str, Any] | None:
    for entry in data.get("polluted", []):
        if entry.get("ip") == ip and entry.get("region") == region:
            return entry
    return None


def atomic_write_registry(data: dict[str, Any], path: Path = REGISTRY) -> None:
    payload = json.dumps(data, indent=2, ensure_ascii=False) + "\n"
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(
        prefix=f".{path.name}.",
        suffix=".tmp",
        dir=str(path.parent),
    )
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp_name, path)
    except OSError as exc:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        fail(f"failed to write {path}: {exc}")


def append_entry(
    *,
    ip: str,
    region: str,
    notes: str,
    edge_id: str = "",
    platform: str = "",
    registry_path: Path = REGISTRY,
    dry_run: bool = False,
) -> bool:
    validate_ip(ip)
    if not region.strip():
        fail("region is required")
    if not notes.strip():
        fail("notes is required")

    data = load_registry(registry_path)
    existing = find_entry(data, ip, region)
    if existing is not None:
        print(
            f"record-polluted-ip: already registered {ip} in {region} "
            f"(notes={existing.get('notes', '')!r})",
            file=sys.stderr,
        )
        return False

    entry: dict[str, Any] = {
        "ip": ip,
        "region": region,
        "notes": notes.strip(),
    }
    if edge_id:
        entry["edge_id"] = edge_id
    if platform:
        entry["platform"] = platform

    data.setdefault("polluted", []).append(entry)
    if dry_run:
        print(json.dumps(entry, indent=2))
        return True

    atomic_write_registry(data, registry_path)
    print(f"record-polluted-ip: appended {ip} ({region}) to {registry_path}")
    return True


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--registry", type=Path, default=REGISTRY, help="Registry JSON path")
    sub = parser.add_subparsers(dest="command", required=True)

    append = sub.add_parser("append", help="Append an exclusion entry (idempotent on ip+region)")
    append.add_argument("--ip", required=True)
    append.add_argument("--region", required=True)
    append.add_argument("--notes", required=True)
    append.add_argument("--edge-id", default="")
    append.add_argument("--platform", choices=("ec2", "lightsail"), default="")
    append.add_argument("--dry-run", action="store_true")

    check = sub.add_parser("is-excluded", help="Exit 0 when ip+region is already excluded")
    check.add_argument("--ip", required=True)
    check.add_argument("--region", required=True)

    args = parser.parse_args()

    if args.command == "is-excluded":
        validate_ip(args.ip)
        data = load_registry(args.registry)
        if args.ip in excluded_ips_for_region(data, args.region):
            return 0
        return 1

    append_entry(
        ip=args.ip,
        region=args.region,
        notes=args.notes,
        edge_id=args.edge_id,
        platform=args.platform,
        registry_path=args.registry,
        dry_run=args.dry_run,
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
