#!/usr/bin/env python3
"""Resolve an admin target through the shared live prod/edge identity owners."""
from __future__ import annotations

import argparse
import pathlib

from edge_ssm_execution import resolve_edge_execution_identity
from resolve_prod_ssm_target import DEFAULT_MATRIX, resolve_aws, resolve_hetzner, resolve_mode, AWS_STACK


def main() -> None:
    parser = argparse.ArgumentParser(description="Print MODE\tSSM_REGION\tINSTANCE_ID for admin operations.")
    parser.add_argument("repo_root")
    parser.add_argument("edge_id")
    parser.add_argument("platform", nargs="?", default="auto", choices=("auto", "ec2", "lightsail", "hetzner"))
    args = parser.parse_args()
    if args.edge_id == "prod":
        if args.platform == "lightsail":
            parser.error("prod has no Lightsail target")
        mode = resolve_mode("aws" if args.platform == "ec2" else args.platform)
        target = resolve_aws(AWS_STACK) if mode == "aws" else resolve_hetzner(DEFAULT_MATRIX)
        mode = "ec2" if mode == "aws" else mode
        print(f"{mode}\t{target['ssm_region']}\t{target['instance_id']}")
    else:
        target = resolve_edge_execution_identity(pathlib.Path(args.repo_root), args.edge_id, platform=args.platform)
        print(f"{target.routing}\t{target.region}\t{target.instance_id}")


if __name__ == "__main__":
    main()
