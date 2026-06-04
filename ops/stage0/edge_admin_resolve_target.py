#!/usr/bin/env python3
"""Resolve EC2 CFN vs Lightsail tag-based SSM routing for edge admin helper scripts.

The special target ``prod`` is not an edge: it resolves to the fixed prod
Stage0 EC2/CloudFormation stack (``tokenkey-prod-stage0`` in ``us-east-1``),
mirroring the prod-target convention used by ``ops/observability/run-probe.sh``.
This lets the admin-credential helpers (capture / reset / ensure) operate on the
prod gateway through the same single ``ec2`` code path as edges.
"""

from __future__ import annotations

import argparse
import pathlib
import sys

from edge_routing_matrix import resolve_route_tab

# prod Stage0 is a fixed EC2/CFN target outside the edge matrices. Kept here (the
# admin-target resolver) rather than in edge_routing_matrix so the general edge
# enumeration stays edge-only. Region + stack mirror run-probe.sh's prod target.
PROD_TARGET = "prod"
PROD_REGION = "us-east-1"
PROD_EC2_STACK = "tokenkey-prod-stage0"


def main() -> None:
    p = argparse.ArgumentParser(
        description="Print one TAB-separated row: MODE\\tREGION\\tSTACK_OR_EMPTY "
        "(MODE is ec2|lightsail; STACK_OR_EMPTY is CFN stack name or empty). "
        "edge_id 'prod' resolves to the prod Stage0 EC2 stack tokenkey-prod-stage0.",
    )
    p.add_argument("repo_root")
    p.add_argument("edge_id", help="edge id (e.g. uk1, us6) or the literal 'prod'")
    p.add_argument(
        "platform",
        nargs="?",
        default="auto",
        choices=("auto", "ec2", "lightsail"),
    )
    ns = p.parse_args()

    if ns.edge_id == PROD_TARGET:
        # prod is always EC2/CFN; --platform is ignored (no Lightsail prod stack).
        sys.stdout.write(f"ec2\t{PROD_REGION}\t{PROD_EC2_STACK}\n")
        return

    root = pathlib.Path(ns.repo_root).resolve()
    eid = ns.edge_id
    pref = ns.platform  # type: ignore[arg-type]

    mode, region, stack = resolve_route_tab(root, eid, pref)

    stack_out = stack or ""
    sys.stdout.write(f"{mode}\t{region}\t{stack_out}\n")


if __name__ == "__main__":
    main()
