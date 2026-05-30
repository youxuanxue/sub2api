#!/usr/bin/env python3
"""merge-gate-parity.py — keep upstream-merge-pr-shape.yml and preflight.sh in sync.

The merge/upstream-* PR-shape gate (.github/workflows/upstream-merge-pr-shape.yml
checks 4-13) runs the SAME sentinel / contract checks that scripts/preflight.sh
runs locally, deliberately, so a green local preflight implies a green merge gate.
They are hand-maintained copies that could drift (a 14th sentinel wired into one
but not the other). Rather than collapse them — which would touch the merge-gate
machinery itself — this guard makes the drift fail closed:

  1. Every script in scripts/sentinels/merge-gate-parity.json MUST be invoked in
     BOTH scripts/preflight.sh AND upstream-merge-pr-shape.yml.
  2. Every scripts/sentinels/check-*.py referenced in the workflow MUST be listed
     in the manifest (so adding a sentinel to the workflow forces a manifest
     update; scripts/checks/* are covered by rule 1 since that dir also holds
     merge-topology helpers like skip-ci-marker.py that are NOT gate sentinels).
  3. The one merge-gate check that is a go test, not a script
     (TestUS077_QAEvidenceDatasetCheck_), must appear in both files.

Exit 0 = in sync; 1 = drift; 2 = environment failure.

Usage: python3 scripts/checks/merge-gate-parity.py [--quiet]
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
MANIFEST = REPO / "scripts/sentinels/merge-gate-parity.json"
PREFLIGHT = REPO / "scripts/preflight.sh"
WORKFLOW = REPO / ".github/workflows/upstream-merge-pr-shape.yml"
GO_TEST_TOKEN = "TestUS077_QAEvidenceDatasetCheck_"
SENTINEL_REF_RE = re.compile(r"scripts/sentinels/check-[A-Za-z0-9_-]+\.py")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args()

    try:
        listed = set(json.loads(MANIFEST.read_text(encoding="utf-8"))["scripts"])
        preflight_text = PREFLIGHT.read_text(encoding="utf-8")
        workflow_text = WORKFLOW.read_text(encoding="utf-8")
    except (OSError, json.JSONDecodeError, KeyError) as exc:
        print(f"FAIL: cannot read inputs: {exc}", file=sys.stderr)
        return 2

    failures: list[str] = []

    # Rule 1 — each manifest script is invoked in BOTH files.
    for script in sorted(listed):
        if script not in preflight_text:
            failures.append(f"{script}: listed in manifest but NOT invoked in scripts/preflight.sh")
        if script not in workflow_text:
            failures.append(f"{script}: listed in manifest but NOT invoked in upstream-merge-pr-shape.yml")

    # Rule 2 — each sentinel referenced in the workflow is in the manifest.
    for script in sorted(set(SENTINEL_REF_RE.findall(workflow_text))):
        if script not in listed:
            failures.append(
                f"{script}: invoked in upstream-merge-pr-shape.yml but missing from "
                "scripts/sentinels/merge-gate-parity.json (add it, and run it in preflight.sh too)"
            )

    # Rule 3 — the QA-evidence go test is the one non-script merge-gate check.
    if GO_TEST_TOKEN not in preflight_text:
        failures.append(f"{GO_TEST_TOKEN}: go test not found in scripts/preflight.sh")
    if GO_TEST_TOKEN not in workflow_text:
        failures.append(f"{GO_TEST_TOKEN}: go test not found in upstream-merge-pr-shape.yml")

    if failures:
        print("[check_merge_gate_parity] FAIL: merge-gate / preflight sentinel-set drift:", file=sys.stderr)
        for f in failures:
            print(f"  - {f}", file=sys.stderr)
        return 1

    if not args.quiet:
        print(f"[check_merge_gate_parity] ok: {len(listed)} merge-gate sentinels in sync (preflight <-> workflow)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
