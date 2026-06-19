#!/usr/bin/env python3
"""
check-perf-query-shape.py — verify that performance-critical query SHAPES are
still present in the working tree.

Reads `scripts/sentinels/perf-query-shape.json` (single source of truth) and
for each entry verifies:

  1. The file at `path` exists.
  2. Every literal string in `must_contain` appears at least once in the file.
  3. Every literal string in `must_not_contain` is absent from the file.

Exit codes:
  0  — all sentinels intact.
  1  — at least one sentinel missing or has lost a required shape anchor.
  2  — registry file missing or malformed.

Usage:
  python3 scripts/sentinels/check-perf-query-shape.py
  python3 scripts/sentinels/check-perf-query-shape.py --quiet   # only print failures
  python3 scripts/sentinels/check-perf-query-shape.py --json    # machine-readable report

Why this exists:
  Some performance fixes hinge on a SQL/query shape whose regression is
  semantically invisible — a revert returns byte-identical results, so no
  unit/integration test catches it, and an EXPLAIN-plan test is unreliable on
  small fixtures (the planner seq-scans tiny tables regardless of shape). The
  guarded shapes live in upstream-shaped files that upstream actively churns,
  so an upstream merge can silently restore the slow form with all tests green.
  Listing the load-bearing shape anchors in scripts/sentinels/perf-query-shape.json
  and gating both pre-commit (scripts/preflight.sh) and upstream merge PRs
  (.github/workflows/upstream-merge-pr-shape.yml) on this script upgrades the
  inline "do NOT simplify this back" comment from "reviewer must remember" to
  "merge will fail". See PR #877 for the first guarded shape.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
REGISTRY_PATH = REPO_ROOT / "scripts" / "sentinels" / "perf-query-shape.json"


def load_registry() -> dict:
    if not REGISTRY_PATH.is_file():
        print(
            f"FATAL: registry file not found: {REGISTRY_PATH.relative_to(REPO_ROOT)}",
            file=sys.stderr,
        )
        sys.exit(2)
    try:
        with REGISTRY_PATH.open("r", encoding="utf-8") as f:
            data = json.load(f)
    except json.JSONDecodeError as exc:
        print(
            f"FATAL: registry file is not valid JSON: {exc}",
            file=sys.stderr,
        )
        sys.exit(2)
    if "sentinels" not in data or not isinstance(data["sentinels"], list):
        print("FATAL: registry missing 'sentinels' array.", file=sys.stderr)
        sys.exit(2)
    return data


def check_sentinel(entry: dict) -> tuple[bool, list[str]]:
    """Returns (ok, list_of_failure_messages)."""
    path_str = entry.get("path")
    if not path_str:
        return False, ["entry missing 'path'"]
    file_path = REPO_ROOT / path_str
    if not file_path.is_file():
        return False, [f"file missing: {path_str}"]
    must_contain = entry.get("must_contain") or []
    must_not_contain = entry.get("must_not_contain") or []
    if not must_contain and not must_not_contain:
        return True, []
    try:
        content = file_path.read_text(encoding="utf-8", errors="replace")
    except OSError as exc:
        return False, [f"cannot read {path_str}: {exc}"]
    failures: list[str] = []
    for needle in must_contain:
        if needle not in content:
            failures.append(f"missing literal `{needle}` in {path_str}")
    for needle in must_not_contain:
        if needle in content:
            failures.append(f"forbidden literal `{needle}` still present in {path_str}")
    return (len(failures) == 0), failures


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true", help="only print failures")
    parser.add_argument(
        "--json",
        action="store_true",
        help="emit a machine-readable JSON report on stdout",
    )
    args = parser.parse_args()

    registry = load_registry()
    sentinels = registry["sentinels"]

    results = []
    fail_count = 0
    for entry in sentinels:
        ok, failures = check_sentinel(entry)
        if not ok:
            fail_count += 1
        results.append(
            {
                "path": entry.get("path"),
                "ok": ok,
                "failures": failures,
                "rationale": entry.get("rationale", ""),
            }
        )

    if args.json:
        json.dump(
            {"total": len(sentinels), "failed": fail_count, "results": results},
            sys.stdout,
            indent=2,
        )
        sys.stdout.write("\n")
    else:
        if not args.quiet:
            print(f"perf query-shape sentinels: checking {len(sentinels)} entries from "
                  f"{REGISTRY_PATH.relative_to(REPO_ROOT)}")
        for r in results:
            if r["ok"]:
                if not args.quiet:
                    print(f"  ok: {r['path']}")
            else:
                print(f"  FAIL: {r['path']}")
                for msg in r["failures"]:
                    print(f"        - {msg}")
                if r["rationale"]:
                    print(f"        why it matters: {r['rationale']}")
        if fail_count == 0:
            if not args.quiet:
                print(f"perf query-shape sentinels: PASS ({len(sentinels)}/{len(sentinels)} intact)")
        else:
            print(
                f"perf query-shape sentinels: FAIL ({fail_count}/{len(sentinels)} regressed)",
                file=sys.stderr,
            )
            print(
                "  Source of truth: scripts/sentinels/perf-query-shape.json",
                file=sys.stderr,
            )
            print(
                "  If a guarded shape was intentionally rewritten to a different "
                "efficient form, update the must_contain anchors (and re-measure) "
                "in the same commit. Do NOT silently delete entries.",
                file=sys.stderr,
            )

    return 0 if fail_count == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
