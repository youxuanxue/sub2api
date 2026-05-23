#!/usr/bin/env python3
"""Edge platform exclusivity gate.

EC2 Edge (`deploy/aws/stage0/edge-targets.json`) and Lightsail Edge
(`deploy/aws/lightsail/edge-targets-lightsail.json`) intentionally use the
SAME `<edge_id>` namespace, the SAME GitHub Environment `edge-<id>`, and the
SAME DNS domain `api-<id>.tokenkey.dev`. AWS resources are fully namespaced
(stack name, SSM prefix, Static IP name), so the two stacks can co-exist
without colliding inside AWS.

The single hard conflict is **DNS**: only one A record can point at one IP.
If both matrices declare the same `edge_id` as `deployable=true` at the same
time, operators get undefined behaviour (whichever stack DNS currently points
at "wins"; the other silently runs as a phantom).

The previous "rule" was the README sentence: "不要对同一 edge 混跑两种
provision". OPC §"升级原则": when a soft rule has been documented twice it
must become a check. This is the check.

Exit codes:
  0 — no `edge_id` is `deployable=true` in both matrices (or one of the
       matrices is missing/empty).
  1 — collision detected; print conflicting ids and operator guidance.
  2 — a matrix file is unreadable / malformed (not a content collision, but
       still a blocker because we cannot prove exclusivity).

stdlib-only.
"""
from __future__ import annotations

import json
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
EC2_MATRIX = REPO_ROOT / "deploy/aws/stage0/edge-targets.json"
LIGHTSAIL_MATRIX = REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json"


def _deployable_ids(path: pathlib.Path) -> set[str]:
    if not path.is_file():
        return set()
    payload = json.loads(path.read_text(encoding="utf-8"))
    targets = payload.get("targets") or {}
    return {
        edge_id
        for edge_id, target in targets.items()
        if isinstance(target, dict) and target.get("deployable") is True
    }


def main() -> int:
    try:
        ec2_ids = _deployable_ids(EC2_MATRIX)
        ls_ids = _deployable_ids(LIGHTSAIL_MATRIX)
    except json.JSONDecodeError as exc:
        print(f"FAIL: edge matrix JSON malformed: {exc}", file=sys.stderr)
        return 2
    except OSError as exc:
        print(f"FAIL: cannot read edge matrix: {exc}", file=sys.stderr)
        return 2

    collisions = sorted(ec2_ids & ls_ids)
    if not collisions:
        return 0

    print(
        "FAIL: edge_id is deployable=true on BOTH EC2 and Lightsail simultaneously: "
        + ", ".join(collisions),
        file=sys.stderr,
    )
    print(
        "  DNS A record api-<id>.tokenkey.dev can only resolve to one IP. Operating "
        "both stacks for the same <id> means one is unreachable externally and ops "
        "/ smoke / sticky routing all silently target whichever wins DNS at the moment.",
        file=sys.stderr,
    )
    print(
        "  Resolve by setting deployable=false in one of:",
        file=sys.stderr,
    )
    print(f"    - {EC2_MATRIX.relative_to(REPO_ROOT)}", file=sys.stderr)
    print(f"    - {LIGHTSAIL_MATRIX.relative_to(REPO_ROOT)}", file=sys.stderr)
    print(
        "  (or shut down the losing stack via its own workflow before flipping the flag).",
        file=sys.stderr,
    )
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
