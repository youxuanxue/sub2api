#!/usr/bin/env python3
"""
check-gateway-tk.py — verify TokenKey gateway/service hotspot hooks.

Reads `scripts/sentinels/gateway-tk.json` and for each entry verifies:

  1. The file at `path` exists.
  2. Every literal string in `must_contain` appears at least once in the file.
  3. Every literal string in `must_not_contain` is absent from the file.

It also lints registry hygiene WITHIN each entry (anti-bloat guard):

  - duplicate `must_contain` needles in the same entry are noise;
  - a needle that is a substring of another needle in the same entry is
    vacuous (the longer literal present always implies the shorter one) —
    either drop it or strengthen it (e.g. `Name` → `Name(` / `func Name(` /
    `type Name struct`) so it pins a distinct symbol occurrence.

  Cross-entry overlap is intentionally NOT linted: two entries may share an
  anchor under different rationales, and a long needle in one entry must not
  excuse a short needle in another (entries evolve independently).

Exit codes:
  0  — all sentinels intact.
  1  — at least one sentinel missing, lost a required symbol, or failed
       registry hygiene.
  2  — registry file missing or malformed.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
REGISTRY_PATH = REPO_ROOT / "scripts" / "sentinels" / "gateway-tk.json"


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


_CONTENT_CACHE: dict[str, str] = {}


def read_file_cached(path_str: str) -> str:
    """Read a sentinel target file once even when many entries share the path
    (gateway_service.go alone is anchored by 15+ entries)."""
    if path_str not in _CONTENT_CACHE:
        _CONTENT_CACHE[path_str] = (REPO_ROOT / path_str).read_text(
            encoding="utf-8", errors="replace"
        )
    return _CONTENT_CACHE[path_str]


def lint_entry_hygiene(entry: dict) -> list[str]:
    """Within-entry anti-bloat lint; see module docstring."""
    needles = entry.get("must_contain") or []
    problems: list[str] = []
    seen: set[str] = set()
    for n in needles:
        if n in seen:
            problems.append(f"duplicate needle in same entry: `{n}`")
        seen.add(n)
    for a in seen:
        for b in seen:
            if a != b and a in b:
                problems.append(
                    f"vacuous needle `{a}` is a substring of sibling needle `{b}` "
                    "— drop it or strengthen it to pin a distinct symbol"
                )
    return problems


def check_sentinel(entry: dict) -> tuple[bool, list[str]]:
    path_str = entry.get("path")
    if not path_str:
        return False, ["entry missing 'path'"]
    file_path = REPO_ROOT / path_str
    if not file_path.is_file():
        return False, [f"file missing: {path_str}"]
    failures: list[str] = lint_entry_hygiene(entry)
    must_contain = entry.get("must_contain") or []
    if not must_contain and not failures:
        return True, []
    try:
        content = read_file_cached(path_str)
    except OSError as exc:
        return False, [f"cannot read {path_str}: {exc}"]
    for needle in must_contain:
        if needle not in content:
            failures.append(f"missing literal `{needle}` in {path_str}")
    for needle in entry.get("must_not_contain") or []:
        if needle in content:
            failures.append(f"forbidden literal `{needle}` still present in {path_str}")
    return (len(failures) == 0), failures


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true", help="only print failures")
    parser.add_argument("--json", action="store_true", help="emit a machine-readable JSON report")
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
        json.dump({"total": len(sentinels), "failed": fail_count, "results": results}, sys.stdout, indent=2)
        sys.stdout.write("\n")
    else:
        if not args.quiet:
            print(
                f"gateway TK sentinels: checking {len(sentinels)} entries from "
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
                print(f"gateway TK sentinels: PASS ({len(sentinels)}/{len(sentinels)} intact)")
        else:
            print(
                f"gateway TK sentinels: FAIL ({fail_count}/{len(sentinels)} regressed)",
                file=sys.stderr,
            )
            print("  Source of truth: scripts/sentinels/gateway-tk.json", file=sys.stderr)
            print(
                "  If a hook was intentionally moved/renamed, update the registry "
                "in the same commit. Do NOT silently delete entries.",
                file=sys.stderr,
            )

    return 0 if fail_count == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
