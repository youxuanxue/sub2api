#!/usr/bin/env python3
"""Pick the first deployable Edge with a non-empty native OAuth/Kiro pool.

Used by tokenkey-stage0-release-rollout target=all: canary full smoke runs on
this edge only; rollout-edges.sh covers the rest with infra-only smoke.

stdout (default): single edge id, e.g. ``us6``
stdout (--json): {"canary_edge":"us6","oauth_account_count":2,"candidates":[...]}
exit 0 when a canary is found; exit 1 when no deployable edge has OAuth pool.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
RESOLVE_EDGE = REPO_ROOT / "deploy/aws/stage0/resolve-edge-target.py"
RUN_PROBE = REPO_ROOT / "ops/observability/run-probe.sh"
OAUTH_PROBE = REPO_ROOT / "ops/stage0/edge_oauth_pool_probe.sh"
_COUNT_RE = re.compile(r"^\d+$")


def _fail(message: str, code: int = 1) -> None:
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(code)


def list_deployable_edges() -> list[str]:
    proc = subprocess.run(
        [sys.executable, str(RESOLVE_EDGE), "--list-deployable"],
        capture_output=True,
        text=True,
        check=False,
        cwd=REPO_ROOT,
    )
    if proc.returncode != 0:
        _fail(f"resolve-edge-target --list-deployable failed: {proc.stderr.strip()}")
    return [line.strip() for line in proc.stdout.splitlines() if line.strip()]


def probe_oauth_pool_count(edge_id: str, *, source_group: str, timeout_seconds: int) -> int | None:
    """Return account count, or None when SSM/probe transport failed."""
    proc = subprocess.run(
        [
            "bash",
            str(RUN_PROBE),
            "--target",
            f"edge:{edge_id}",
            "--script",
            str(OAUTH_PROBE),
            "--env",
            f"ANTHROPIC_SOURCE_GROUP={source_group}",
            "--comment",
            f"oauth-canary-pick edge={edge_id}",
            "--timeout-seconds",
            str(timeout_seconds),
        ],
        capture_output=True,
        text=True,
        check=False,
        cwd=REPO_ROOT,
    )
    if proc.returncode != 0:
        print(
            f"pick-oauth-canary-edge: edge={edge_id} probe failed rc={proc.returncode}",
            file=sys.stderr,
        )
        if proc.stderr.strip():
            print(proc.stderr.strip(), file=sys.stderr)
        return None
    raw = proc.stdout.strip().splitlines()
    if not raw:
        return 0
    last = raw[-1].strip()
    if not _COUNT_RE.match(last):
        print(
            f"pick-oauth-canary-edge: edge={edge_id} unexpected probe output: {last!r}",
            file=sys.stderr,
        )
        return None
    return int(last)


def pick_oauth_canary(
    edges: list[str],
    *,
    probe_count,
    source_group: str,
) -> tuple[str | None, list[dict]]:
    """Return (canary_edge, candidate_audit_rows). probe_count(edge_id)->int|None."""
    audit: list[dict] = []
    for edge_id in edges:
        count = probe_count(edge_id)
        row = {"edge_id": edge_id, "oauth_account_count": count}
        audit.append(row)
        if count is not None and count > 0:
            return edge_id, audit
    return None, audit


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Pick first deployable Edge with schedulable native OAuth/Kiro accounts.",
    )
    parser.add_argument(
        "--json",
        action="store_true",
        help="Emit JSON with canary_edge, oauth_account_count, and candidates audit.",
    )
    parser.add_argument(
        "--source-group",
        default="default",
        help="Anthropic OAuth pool group name (default: default).",
    )
    parser.add_argument(
        "--timeout-seconds",
        type=int,
        default=120,
        help="SSM timeout per edge probe (default: 120).",
    )
    args = parser.parse_args()

    if not RUN_PROBE.is_file():
        _fail(f"run-probe missing: {RUN_PROBE}")
    if not OAUTH_PROBE.is_file():
        _fail(f"oauth pool probe missing: {OAUTH_PROBE}")

    edges = list_deployable_edges()
    if not edges:
        _fail("no deployable edges in matrix")

    def _probe(edge_id: str) -> int | None:
        return probe_oauth_pool_count(
            edge_id,
            source_group=args.source_group,
            timeout_seconds=args.timeout_seconds,
        )

    canary, audit = pick_oauth_canary(
        edges,
        probe_count=_probe,
        source_group=args.source_group,
    )

    if canary is None:
        summary = ", ".join(
            f"{row['edge_id']}={row['oauth_account_count']}" for row in audit
        )
        _fail(
            "no deployable edge has schedulable native OAuth/Kiro accounts "
            f"(source_group={args.source_group}); probes: {summary}"
        )

    chosen = next(row for row in audit if row["edge_id"] == canary)
    if args.json:
        payload = {
            "canary_edge": canary,
            "oauth_account_count": chosen["oauth_account_count"],
            "source_group": args.source_group,
            "candidates": audit,
        }
        json.dump(payload, sys.stdout, separators=(",", ":"), sort_keys=True)
        print()
    else:
        print(canary)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
