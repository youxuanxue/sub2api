#!/usr/bin/env python3
"""Allocate a fresh VPC EIP for an edge, refusing any allocation that lands on
a known-polluted IP listed in deploy/aws/stage0/edge-polluted-ips.json.

Used by .github/workflows/deploy-edge-stage0.yml operation=rotate_egress_ip
before the CloudFormation update-stack that binds the new allocation to the
edge instance.

Output: a single line of JSON on stdout, suitable for $GITHUB_OUTPUT capture:
  {"allocation_id": "eipalloc-...", "public_ip": "1.2.3.4", "attempts": 1}

Exit codes:
  0 — allocation obtained and emitted as JSON
  2 — exhausted --max-attempts; every fresh allocation landed on a polluted IP
  3 — AWS API failure or bad input
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import re
import subprocess
import sys
from typing import Any


# AWS CLI shorthand parser treats `,`, `=`, `[`, `]`, `{`, `}` as structural
# separators inside `--tag-specifications Tags=[{Key=...,Value=...},...]`. Any
# of these characters in a Tag Value breaks the entire allocate-address call.
# Replace with spaces so the human-readable reason stays useful while staying
# shorthand-safe. (Operator-supplied rotation_reason flows through here.)
_TAG_VALUE_UNSAFE = re.compile(r'[,=\[\]{}"]')


REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
POLLUTED_FILE = REPO_ROOT / "deploy" / "aws" / "stage0" / "edge-polluted-ips.json"


def fail(msg: str, code: int = 3) -> None:
    print(f"allocate-clean-egress-eip: {msg}", file=sys.stderr)
    sys.exit(code)


def load_polluted_ips(region: str) -> set[str]:
    if not POLLUTED_FILE.exists():
        fail(f"missing polluted-IPs registry: {POLLUTED_FILE}")
    try:
        data = json.loads(POLLUTED_FILE.read_text())
    except json.JSONDecodeError as e:
        fail(f"malformed JSON in {POLLUTED_FILE}: {e}")
    return {
        entry["ip"]
        for entry in data.get("polluted", [])
        if entry.get("region") == region and "ip" in entry
    }


def aws_ec2(region: str, *args: str) -> dict[str, Any]:
    cmd = ["aws", "ec2", "--region", region, "--output", "json", *args]
    try:
        res = subprocess.run(cmd, capture_output=True, text=True, check=False)
    except FileNotFoundError:
        fail("aws CLI not found on PATH")
    if res.returncode != 0:
        fail(f"aws ec2 {args[0] if args else ''} failed: {res.stderr.strip() or res.stdout.strip()}")
    if not res.stdout.strip():
        return {}
    try:
        return json.loads(res.stdout)
    except json.JSONDecodeError as e:
        fail(f"aws ec2 returned non-JSON: {e}")
    return {}


def allocate_once(region: str, tag_specs: str) -> tuple[str, str]:
    out = aws_ec2(
        region,
        "allocate-address",
        "--domain", "vpc",
        "--tag-specifications", tag_specs,
    )
    alloc = out.get("AllocationId")
    ip = out.get("PublicIp")
    if not alloc or not ip:
        fail(f"allocate-address returned unexpected payload: {out!r}")
    return alloc, ip


def release(region: str, allocation_id: str) -> None:
    aws_ec2(region, "release-address", "--allocation-id", allocation_id)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--region", required=True, help="AWS region for the edge")
    parser.add_argument("--edge-id", required=True, help="Edge id (uk1, us1, ...)")
    parser.add_argument(
        "--reason",
        required=True,
        help="Short string written to the EIP's tokenkey:replaces-reason tag and emitted in the JSON output",
    )
    parser.add_argument(
        "--max-attempts",
        type=int,
        default=int(os.environ.get("ALLOCATE_MAX_ATTEMPTS", "5")),
        help="Maximum allocate-address calls before giving up (default 5)",
    )
    # Optional billing/identity tags — preserves parity with the tag set the
    # pre-OPC CFN-managed AWS::EC2::EIP carried, so AWS billing cost-allocation
    # by tag and cross-account ops queries continue to attribute the EIP to the
    # right project/environment/profile/budget bucket.
    parser.add_argument("--environment", default="edge", help="Environment tag value (default 'edge')")
    parser.add_argument("--profile", default="edge-minimal", help="Profile tag value (default 'edge-minimal')")
    parser.add_argument("--monthly-budget-usd", default="", help="MonthlyBudgetUsd tag value (e.g. '16'); blank to omit")
    args = parser.parse_args()

    polluted = load_polluted_ips(args.region)

    sanitized_reason = _TAG_VALUE_UNSAFE.sub(" ", args.reason)[:200]
    extra_tags = (
        f"{{Key=Environment,Value={args.environment}}},"
        f"{{Key=Profile,Value={args.profile}}}"
    )
    if args.monthly_budget_usd:
        extra_tags += f",{{Key=MonthlyBudgetUsd,Value={args.monthly_budget_usd}}}"
    # No -candidate suffix on Name and no tokenkey:status=candidate tag:
    # the cicd-oidc IAM gates ec2:CreateTags on ec2:CreateAction=AllocateAddress,
    # so we cannot flip the tag from "candidate" to "active" after a successful
    # swap. Calling the EIP its final name from allocation time avoids leaving
    # the active edge EIP with a stale "candidate" label in the AWS console.
    # Brief window where two EIPs share Name=tokenkey-<edge>-eip (between swap
    # success and operator release of the old one) is harmless — edge-ip-status.sh
    # filters by instance-id, not Name.
    tag_spec = (
        f"ResourceType=elastic-ip,Tags=["
        f"{{Key=Project,Value=tokenkey}},"
        f"{{Key=EdgeId,Value={args.edge_id}}},"
        f"{{Key=Name,Value=tokenkey-{args.edge_id}-eip}},"
        f"{{Key=tokenkey:replaces-reason,Value={sanitized_reason}}},"
        f"{extra_tags}"
        f"]"
    )

    released_polluted: list[str] = []
    for attempt in range(1, args.max_attempts + 1):
        alloc, ip = allocate_once(args.region, tag_spec)
        if ip in polluted:
            released_polluted.append(ip)
            release(args.region, alloc)
            continue
        print(json.dumps({
            "allocation_id": alloc,
            "public_ip": ip,
            "attempts": attempt,
            "released_polluted": released_polluted,
            "reason": args.reason,
            "edge_id": args.edge_id,
            "region": args.region,
        }))
        return 0

    fail(
        f"exhausted {args.max_attempts} attempts; every allocation landed on a polluted IP. "
        f"Released: {released_polluted}. The region's EIP pool may be globally dirty for "
        f"this upstream; try a different region or open a quota / Trust & Safety ticket.",
        code=2,
    )
    return 2


if __name__ == "__main__":
    sys.exit(main())
