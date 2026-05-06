#!/usr/bin/env python3
"""
check-frontend-tk-sentinels.py — verify that every load-bearing TokenKey-only
frontend surface (admin-UI fluid table, sidebar geometry, etc.) is still
present in the working tree.

Reads `scripts/frontend-tk-sentinels.json` (single source of truth) and for
each entry verifies:

  1. The file at `path` exists.
  2. Every literal string in `must_contain` appears at least once in the file.

Exit codes:
  0  — all sentinels intact.
  1  — at least one sentinel missing or has lost a required symbol.
  2  — registry file missing or malformed.

Usage:
  python3 scripts/check-frontend-tk-sentinels.py
  python3 scripts/check-frontend-tk-sentinels.py --quiet     # only print failures
  python3 scripts/check-frontend-tk-sentinels.py --json      # machine-readable report

Why this exists:
  Small TK frontend changes (sidebar w-44 vs upstream w-64, DataTable `fluid`
  prop, AccountsView `:sticky-edge-hints="false"`) are exactly the kinds of
  divergence that get silently reverted by upstream merges — they compile
  cleanly with or without the TK touches, so without a literal-content guard
  the regression is invisible until someone notices the UI looks wrong.
  Listing the load-bearing surfaces in `scripts/frontend-tk-sentinels.json`
  and gating both pre-commit (`scripts/preflight.sh`) and upstream merge PRs
  (`.github/workflows/upstream-merge-pr-shape.yml`) on this script upgrades
  the rule from "agent must remember" to "merge will fail". Sibling
  registries: newapi (fifth-platform), brand (outward surfaces).
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
REGISTRY_PATH = REPO_ROOT / "scripts" / "frontend-tk-sentinels.json"
LABEL = "frontend TK sentinels"


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
    if not must_contain:
        return True, []
    try:
        content = file_path.read_text(encoding="utf-8", errors="replace")
    except OSError as exc:
        return False, [f"cannot read {path_str}: {exc}"]
    failures: list[str] = []
    for needle in must_contain:
        if needle not in content:
            failures.append(f"missing literal `{needle}` in {path_str}")
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
            print(f"{LABEL}: checking {len(sentinels)} entries from "
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
                print(f"{LABEL}: PASS ({len(sentinels)}/{len(sentinels)} intact)")
        else:
            print(
                f"{LABEL}: FAIL ({fail_count}/{len(sentinels)} regressed)",
                file=sys.stderr,
            )
            print(
                f"  Source of truth: {REGISTRY_PATH.relative_to(REPO_ROOT)}",
                file=sys.stderr,
            )
            print(
                "  If a sentinel was intentionally moved/renamed, update the "
                "registry in the same commit. Do NOT silently delete entries.",
                file=sys.stderr,
            )

    return 0 if fail_count == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
