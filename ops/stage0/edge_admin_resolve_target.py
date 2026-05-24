#!/usr/bin/env python3
"""Resolve EC2 CFN vs Lightsail tag-based SSM routing for edge admin helper scripts."""

from __future__ import annotations

import argparse
import pathlib
import sys

from edge_routing_matrix import resolve_route_tab


def main() -> None:
    p = argparse.ArgumentParser(
        description="Print one TAB-separated row: MODE\\tREGION\\tSTACK_OR_EMPTY "
        "(MODE is ec2|lightsail; STACK_OR_EMPTY is CFN stack name or empty).",
    )
    p.add_argument("repo_root")
    p.add_argument("edge_id")
    p.add_argument(
        "platform",
        nargs="?",
        default="auto",
        choices=("auto", "ec2", "lightsail"),
    )
    ns = p.parse_args()

    root = pathlib.Path(ns.repo_root).resolve()
    eid = ns.edge_id
    pref = ns.platform  # type: ignore[arg-type]

    mode, region, stack = resolve_route_tab(root, eid, pref)

    stack_out = stack or ""
    sys.stdout.write(f"{mode}\t{region}\t{stack_out}\n")


if __name__ == "__main__":
    main()
