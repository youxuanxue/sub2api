#!/usr/bin/env python3
"""
check-relay-invariants.py — verify that every load-bearing, TokenKey-deliberate
relay behavior still has its characterization test present in the working tree.

Reads `scripts/sentinels/relay-invariants.json` (single source of truth) and for
each entry verifies:

  1. The test file at `path` exists.
  2. Every literal in `must_contain` (each a `func TestX(` definition prefix)
     appears at least once in the file.
  3. Every literal in `must_not_contain` (if any) is absent from the file.

It is the same shape as check-newapi.py / check-grok.py, but the anchored
literals are TEST FUNCTION DEFINITIONS rather than production symbols: the
behavior these tests encode (G4 cooldown scoping, kiro/grok refresh candidates,
SSE stream-error 502, org-ban/bodyless/oauth401/usage-policy/TLS-fingerprint/
request-owned-429 cooldown polarity, thinking model-reference routing) is
DELIBERATE and TK-correct, and the only durable proof of each choice is its
test. An upstream merge that silently deletes or guts one of these tests — the
exact failure class PR #835 surfaced — now fails this check instead of shipping
a green CI.

Exit codes:
  0  — all relay-invariant tests intact.
  1  — at least one test file missing or has lost a pinned test function.
  2  — registry file missing or malformed.

Usage:
  python3 scripts/sentinels/check-relay-invariants.py
  python3 scripts/sentinels/check-relay-invariants.py --quiet     # only failures
  python3 scripts/sentinels/check-relay-invariants.py --json      # machine-readable

Wiring:
  - scripts/preflight.sh runs this on every PR (local pre-commit + CI preflight).
  - .github/workflows/upstream-merge-pr-shape.yml runs the same script, so a
    green local preflight implies a green merge-PR check.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
REGISTRY_PATH = REPO_ROOT / "scripts" / "sentinels" / "relay-invariants.json"


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
        print(f"FATAL: registry file is not valid JSON: {exc}", file=sys.stderr)
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
        return False, [f"test file missing: {path_str}"]
    must_contain = entry.get("must_contain") or []
    must_not_contain = entry.get("must_not_contain") or []
    if not must_contain and not must_not_contain:
        return False, [f"entry for {path_str} pins no test function (must_contain empty)"]
    try:
        content = file_path.read_text(encoding="utf-8", errors="replace")
    except OSError as exc:
        return False, [f"cannot read {path_str}: {exc}"]
    failures: list[str] = []
    for needle in must_contain:
        if needle not in content:
            failures.append(f"missing test `{needle}` in {path_str}")
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
            print(
                f"relay-invariant tests: checking {len(sentinels)} entries from "
                f"{REGISTRY_PATH.relative_to(REPO_ROOT)}"
            )
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
                print(
                    f"relay-invariant tests: PASS ({len(sentinels)}/{len(sentinels)} intact)"
                )
        else:
            print(
                f"relay-invariant tests: FAIL ({fail_count}/{len(sentinels)} regressed)",
                file=sys.stderr,
            )
            print(
                "  Source of truth: scripts/sentinels/relay-invariants.json",
                file=sys.stderr,
            )
            print(
                "  A pinned characterization test was deleted/renamed. If the behavior "
                "was intentionally retired, update the registry in the SAME PR (this is "
                "the human-review checkpoint). Otherwise restore the test — an upstream "
                "merge silently reverted a deliberate TokenKey relay choice.",
                file=sys.stderr,
            )

    return 0 if fail_count == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
