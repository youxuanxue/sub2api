#!/usr/bin/env python3
"""Verify the GHCR timer workflow derives its edge fanout from the registry."""

from __future__ import annotations

import pathlib
import subprocess
import sys

import yaml


ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / ".github/workflows/ops-stage0-ghcr-prune-timer.yml"
RESOLVER = ROOT / "deploy/aws/stage0/resolve-edge-target.py"


def fail(message: str) -> int:
    print(f"FAIL: ghcr prune workflow: {message}", file=sys.stderr)
    return 1


def main() -> int:
    data = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
    jobs = data.get("jobs") or {}
    discover = jobs.get("discover-edges") or {}
    fanout = jobs.get("all-deployable-edges") or {}

    if not discover:
        return fail("missing discover-edges registry job")

    matrix_expr = str(((fanout.get("strategy") or {}).get("matrix") or ""))
    if "fromJson(needs.discover-edges.outputs.matrix)" not in matrix_expr:
        return fail("edge job matrix is not wired to discover-edges output")

    discover_runs = "\n".join(
        str(step.get("run") or "") for step in (discover.get("steps") or [])
    )
    if (
        "resolve-edge-target.py" not in discover_runs
        or "--target-selector" not in discover_runs
        or "edge:*" not in discover_runs
    ):
        return fail("discover-edges does not use the canonical edge resolver")

    deployable = subprocess.check_output(
        [sys.executable, str(RESOLVER), "--list-deployable"],
        cwd=ROOT,
        text=True,
    ).splitlines()
    if not deployable:
        return fail("canonical resolver returned no deployable edges")

    workflow_text = WORKFLOW.read_text(encoding="utf-8")
    if "instance_id: mi-" in workflow_text:
        return fail("workflow still embeds mutable SSM managed-instance ids")

    print(f"check-ghcr-prune-workflow: ok ({len(deployable)} registry targets)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
