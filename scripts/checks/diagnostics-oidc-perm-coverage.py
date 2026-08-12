#!/usr/bin/env python3
"""Ensure ops-daily-diagnostics AWS calls are granted by the base OIDC role."""

from __future__ import annotations

import argparse
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
BASE_CFN = REPO_ROOT / "deploy/aws/cloudformation/cicd-oidc.yaml"

# Actions issued on the GHA runner with OIDC creds (not via SSM).
EXPECTED_BASE_ACTIONS: list[tuple[str, str]] = [
    (
        "ec2:DescribeSnapshots",
        "ops/observability/data_layer_snapshot_signal.sh on prod diagnostics (#1554)",
    ),
    (
        "cloudformation:DescribeStackResources",
        "ops/qa/verify_raw_archive_iam_contract.py stack resource lookup (#1621)",
    ),
    (
        "s3:GetBucketPolicy",
        "ops/qa/verify_raw_archive_iam_contract.py bucket policy read (#1621)",
    ),
    (
        "ec2:DescribeVpcEndpoints",
        "ops/qa/verify_raw_archive_iam_contract.py S3 gateway endpoint check (#1620)",
    ),
    (
        "ec2:DescribeRouteTables",
        "ops/qa/verify_raw_archive_iam_contract.py route table gateway route check (#1620)",
    ),
]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true")
    args = parser.parse_args()
    try:
        base_text = BASE_CFN.read_text(encoding="utf-8")
    except OSError as exc:
        print(f"FAIL: cannot read {BASE_CFN.relative_to(REPO_ROOT)}: {exc}", file=sys.stderr)
        return 2

    missing = [
        (action, notes)
        for action, notes in EXPECTED_BASE_ACTIONS
        if action not in base_text
    ]
    if not missing:
        msg = f"ok: diagnostics OIDC perm coverage ({len(EXPECTED_BASE_ACTIONS)} actions)"
        print(msg if args.quiet else f"{msg} in {BASE_CFN.relative_to(REPO_ROOT)}")
        return 0

    print("FAIL: ops-daily-diagnostics actions missing from base OIDC policy:", file=sys.stderr)
    for action, notes in missing:
        print(f"  - {action} ({notes})", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
