#!/usr/bin/env python3
"""
check-newapi-sentinels.py — verify that every load-bearing surface of TokenKey's
fifth platform `newapi` is still present in the working tree.

Reads `scripts/newapi-sentinels.json` (single source of truth) and for each
entry verifies:

  1. The file at `path` exists.
  2. Every literal string in `must_contain` appears at least once in the file.

Exit codes:
  0  — all sentinels intact.
  1  — at least one sentinel missing or has lost a required symbol.
  2  — registry file missing or malformed.

Usage:
  python3 scripts/check-newapi-sentinels.py
  python3 scripts/check-newapi-sentinels.py --quiet     # only print failures
  python3 scripts/check-newapi-sentinels.py --json      # machine-readable report

Why this exists:
  TokenKey's fifth platform `newapi` was silently regressed multiple times by
  upstream merges before this guard existed (see CLAUDE.md §5.x). Listing the
  load-bearing surfaces in `scripts/newapi-sentinels.json` and gating both
  pre-commit (`scripts/preflight.sh § 10`) and upstream merge PRs
  (`.github/workflows/upstream-merge-pr-shape.yml` Check 4) on this script
  upgrades the rule from "agent must remember" to "merge will fail".
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
REGISTRY_PATH = REPO_ROOT / "scripts" / "newapi-sentinels.json"


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
            print(f"newapi sentinels: checking {len(sentinels)} entries from "
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
                print(f"newapi sentinels: PASS ({len(sentinels)}/{len(sentinels)} intact)")
        else:
            print(
                f"newapi sentinels: FAIL ({fail_count}/{len(sentinels)} regressed)",
                file=sys.stderr,
            )
            print(
                "  Source of truth: scripts/newapi-sentinels.json",
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
