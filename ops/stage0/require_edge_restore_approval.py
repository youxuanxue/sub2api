#!/usr/bin/env python3
"""Fail closed unless an explicit edge-restore approval token is present.

2026-10-17 prod-only: every edge matrix row is deployable=false. RETIRED skills
and workflow_dispatch UIs still look runnable; this gate is the single owner that
turns those false doors into a hard stop. Recovery requires a human to type the
literal token (workflow input or CONFIRM_EDGE_RESTORE env) after re-approving an
edge identity — not --allow-planned alone.

Exit 0 on match; exit 1 otherwise. Prints a short stderr reason (and ::error::
when GITHUB_ACTIONS=true).
"""
from __future__ import annotations

import argparse
import os
import sys

# Literal marker — keep in sync with workflow input descriptions and skill copy.
EDGE_RESTORE_APPROVAL_TOKEN = "I_APPROVE_EDGE_RESTORE"
ENV_NAME = "CONFIRM_EDGE_RESTORE"


def approved(token: str | None) -> bool:
    return (token or "").strip() == EDGE_RESTORE_APPROVAL_TOKEN


def refuse(message: str) -> int:
    if os.environ.get("GITHUB_ACTIONS", "").lower() == "true":
        print(f"::error::{message}", file=sys.stderr)
    print(f"require_edge_restore_approval: {message}", file=sys.stderr)
    return 1


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--token",
        default="",
        help="Explicit approval token (workflow input). Overrides env when non-empty.",
    )
    parser.add_argument(
        "--env",
        default=ENV_NAME,
        help=f"Env var to read when --token is empty (default {ENV_NAME}).",
    )
    args = parser.parse_args(argv)

    token = (args.token or "").strip() or os.environ.get(args.env, "")
    if approved(token):
        print(
            "require_edge_restore_approval: ok "
            f"(token matched; edge fleet remains deployable=false until matrix edit)"
        )
        return 0
    return refuse(
        "edge deploy/recovery is retired (2026-10-17 prod-only). "
        f"Re-approval requires the literal token {EDGE_RESTORE_APPROVAL_TOKEN!r} "
        f"via --token or ${{{args.env}}}. "
        "--allow-planned alone is not enough."
    )


if __name__ == "__main__":
    raise SystemExit(main())
