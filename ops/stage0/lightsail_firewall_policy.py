#!/usr/bin/env python3
"""Classify structured Lightsail firewall state for Stage0 checks."""
from __future__ import annotations

import argparse
import ipaddress
import json
import sys
from typing import Any


def _is_public_ipv4_slash_32(value: Any) -> bool:
    try:
        network = ipaddress.ip_network(str(value), strict=True)
    except ValueError:
        return False
    address = network.network_address
    return (
        network.version == 4
        and network.prefixlen == 32
        and not address.is_private
        and not address.is_loopback
        and not address.is_link_local
        and not address.is_multicast
        and not address.is_unspecified
        and not address.is_reserved
    )


def classify_ssh_posture(payload: Any) -> str:
    states = payload.get("portStates") if isinstance(payload, dict) else payload
    if not isinstance(states, list):
        raise ValueError("expected a portStates array")
    ssh = [
        item
        for item in states
        if isinstance(item, dict)
        and item.get("fromPort") == 22
        and item.get("toPort") == 22
        and item.get("protocol") == "tcp"
        and item.get("state") == "open"
    ]
    if not ssh:
        return "closed"
    if len(ssh) != 1:
        return "unsafe"
    state = ssh[0]
    cidrs = state.get("cidrs") or []
    ipv6_cidrs = state.get("ipv6Cidrs") or []
    aliases = state.get("cidrListAliases") or []
    if (
        isinstance(cidrs, list)
        and len(cidrs) == 1
        and _is_public_ipv4_slash_32(cidrs[0])
        and ipv6_cidrs == []
        and aliases == []
    ):
        return "restricted"
    return "unsafe"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("ssh-posture",))
    args = parser.parse_args()
    payload = json.load(sys.stdin)
    if args.operation == "ssh-posture":
        print(classify_ssh_posture(payload))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"lightsail_firewall_policy: {exc}", file=sys.stderr)
        raise SystemExit(2) from exc
