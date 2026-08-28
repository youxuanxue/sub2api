#!/usr/bin/env python3
"""Pick a deployable Edge for release canary smoke from capacity and traffic.

Used by tokenkey-stage0-release-rollout target=all: canary full smoke runs on
this edge only; rollout-edges.sh covers the rest with infra-only smoke.

stdout (default): single edge id, e.g. ``us6``
stdout (--json): {"canary_edge":"us6","oauth_account_count":2,"candidates":[...]}
Probe every Edge, reject missing capacity/traffic facts, then sort eligible
hosts by lower 30-minute completed requests, greater memory headroom, and
matrix order. OAuth/Kiro pool size is audit-only.
"""
from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
RESOLVE_EDGE = REPO_ROOT / "deploy/aws/stage0/resolve-edge-target.py"
RUN_PROBE = REPO_ROOT / "ops/observability/run-probe.sh"
RELEASE_PROBE = REPO_ROOT / "ops/stage0/edge_release_canary_probe.sh"
OAUTH_PROBE = REPO_ROOT / "ops/stage0/edge_oauth_pool_probe.sh"


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


def probe_edge_release_facts(edge_id: str, *, source_group: str, timeout_seconds: int) -> dict | None:
    """Return strict release facts, or None when transport/output is invalid."""
    proc = subprocess.run(
        [
            "bash",
            str(RUN_PROBE),
            "--target",
            f"edge:{edge_id}",
            "--script",
            str(RELEASE_PROBE),
            "--with",
            str(OAUTH_PROBE),
            "--env",
            f"ANTHROPIC_SOURCE_GROUP={source_group}",
            "--comment",
            f"release-canary-pick edge={edge_id}",
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
            f"pick-edge-canary: edge={edge_id} probe failed rc={proc.returncode}",
            file=sys.stderr,
        )
        if proc.stderr.strip():
            print(proc.stderr.strip(), file=sys.stderr)
        return None
    lines = [line.strip() for line in proc.stdout.splitlines() if line.strip()]
    if not lines:
        return None
    try:
        payload = json.loads(lines[-1])
    except json.JSONDecodeError:
        print(
            f"pick-edge-canary: edge={edge_id} unexpected probe output: {lines[-1]!r}",
            file=sys.stderr,
        )
        return None
    if not validate_release_facts(payload):
        print(f"pick-edge-canary: edge={edge_id} invalid release facts", file=sys.stderr)
        return None
    return payload


def validate_release_facts(payload: object) -> bool:
    required = {
        "mem_available_bytes", "active_app_working_set_bytes", "memory_required_bytes",
        "memory_headroom_bytes", "disk_available_bytes", "completed_requests_30m",
        "oauth_account_count", "eligible", "rejection_reasons",
    }
    if not isinstance(payload, dict) or set(payload) != required:
        return False
    numeric = required - {"eligible", "rejection_reasons"}
    if any(payload[key] is not None and (not isinstance(payload[key], int) or isinstance(payload[key], bool)) for key in numeric):
        return False
    if any(payload[key] is not None and payload[key] < 0 for key in numeric - {"memory_headroom_bytes"}):
        return False
    reasons = payload["rejection_reasons"]
    if not isinstance(payload["eligible"], bool) or not isinstance(reasons, list):
        return False
    if any(not isinstance(reason, str) or not reason for reason in reasons):
        return False
    if payload["eligible"]:
        hard_facts = (
            payload["mem_available_bytes"],
            payload["active_app_working_set_bytes"],
            payload["memory_required_bytes"],
            payload["memory_headroom_bytes"],
            payload["disk_available_bytes"],
            payload["completed_requests_30m"],
        )
        if any(not isinstance(value, int) or isinstance(value, bool) or value < 0 for value in hard_facts):
            return False
        expected_required = max(
            335_544_320,
            payload["active_app_working_set_bytes"] + 134_217_728,
        )
        if payload["memory_required_bytes"] != expected_required:
            return False
        if payload["memory_headroom_bytes"] != payload["mem_available_bytes"] - expected_required:
            return False
        if payload["disk_available_bytes"] < 5_368_709_120:
            return False
        if reasons:
            return False
    elif not reasons:
        return False
    return True


def pick_oauth_canary(
    edges: list[str],
    *,
    probe_facts,
    source_group: str,
) -> tuple[str | None, list[dict]]:
    """Return (canary_edge, complete candidate audit)."""
    audit: list[dict] = []
    for matrix_index, edge_id in enumerate(edges):
        facts = probe_facts(edge_id)
        if facts is None or not validate_release_facts(facts):
            row = {
                "edge_id": edge_id,
                "matrix_index": matrix_index,
                "eligible": False,
                "rejection_reasons": [
                    "probe_transport_failed" if facts is None else "probe_facts_invalid"
                ],
            }
        else:
            row = {"edge_id": edge_id, "matrix_index": matrix_index, **facts}
        audit.append(row)
    eligible = [row for row in audit if row.get("eligible") is True]
    if not eligible:
        return None, audit
    eligible.sort(key=lambda row: (
        row["completed_requests_30m"],
        -row["memory_headroom_bytes"],
        row["matrix_index"],
    ))
    return eligible[0]["edge_id"], audit


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Pick the lowest-traffic capacity-safe deployable Edge.",
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
    if not RELEASE_PROBE.is_file():
        _fail(f"release canary probe missing: {RELEASE_PROBE}")
    if not OAUTH_PROBE.is_file():
        _fail(f"oauth pool probe missing: {OAUTH_PROBE}")

    edges = list_deployable_edges()
    if not edges:
        _fail("no deployable edges in matrix")

    def _probe(edge_id: str) -> dict | None:
        return probe_edge_release_facts(
            edge_id,
            source_group=args.source_group,
            timeout_seconds=args.timeout_seconds,
        )

    canary, audit = pick_oauth_canary(
        edges,
        probe_facts=_probe,
        source_group=args.source_group,
    )

    if canary is None:
        summary = ", ".join(f"{row['edge_id']}={row['rejection_reasons']}" for row in audit)
        _fail(
            "no deployable edge passed release capacity/traffic admission; "
            f"probes: {summary}"
        )

    chosen = next(row for row in audit if row["edge_id"] == canary)
    if args.json:
        payload = {
            "canary_edge": canary,
            "oauth_account_count": chosen.get("oauth_account_count"),
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
