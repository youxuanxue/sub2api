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
import re
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

_GO_FUNCTION_RE = re.compile(
    r"(?m)^func\s+(?:\([^\n)]*\)\s*)?(?P<name>[A-Za-z_][A-Za-z0-9_]*)\s*\("
)
_FAILOVER_FIELD_RE = re.compile(r"(?m)^\s*ShouldFailover\s+bool\b")


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
    for a in sorted(seen):
        for b in sorted(seen):
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
    must_not_contain = entry.get("must_not_contain") or []
    if not must_contain and not must_not_contain:
        return (len(failures) == 0), failures
    try:
        content = read_file_cached(path_str)
    except OSError as exc:
        return False, [f"cannot read {path_str}: {exc}"]
    for needle in must_contain:
        if needle not in content:
            failures.append(f"missing literal `{needle}` in {path_str}")
    for needle in must_not_contain:
        if needle in content:
            failures.append(f"forbidden literal `{needle}` still present in {path_str}")
    return (len(failures) == 0), failures


def check_failover_ssot() -> tuple[bool, list[str]]:
    """Reject new adapter-local retry-next-account policy owners.

    Provider adapters may parse payloads and expose a shouldFailover facade for
    existing callers, but every such facade must directly submit an observation
    to classifyGatewayFailover. Splitting by top-level Go function declaration
    keeps this check deterministic without depending on a local Go toolchain.
    """
    service_dir = REPO_ROOT / "backend" / "internal" / "service"
    failures: list[str] = []
    for file_path in sorted(service_dir.glob("*.go")):
        if file_path.name.endswith("_test.go"):
            continue
        source = file_path.read_text(encoding="utf-8", errors="replace")
        rel = file_path.relative_to(REPO_ROOT)
        matches = list(_GO_FUNCTION_RE.finditer(source))
        for index, match in enumerate(matches):
            name = match.group("name")
            if "shouldfailover" not in name.lower():
                continue
            end = matches[index + 1].start() if index + 1 < len(matches) else len(source)
            if "classifyGatewayFailover(" not in source[match.start():end]:
                failures.append(
                    f"{rel}:{source.count(chr(10), 0, match.start()) + 1} "
                    f"{name} must call classifyGatewayFailover directly"
                )
        if _FAILOVER_FIELD_RE.search(source):
            failures.append(
                f"{rel} declares ShouldFailover bool; keep failover output in the global decision owner"
            )
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

    failover_ok, failover_failures = check_failover_ssot()
    if not failover_ok:
        fail_count += 1
    results.append(
        {
            "path": "backend/internal/service (global failover SSOT invariant)",
            "ok": failover_ok,
            "failures": failover_failures,
            "rationale": (
                "Every production shouldFailover facade must delegate to "
                "classifyGatewayFailover; protocol classifiers cannot own a "
                "ShouldFailover boolean."
            ),
        }
    )

    if args.json:
        json.dump({"total": len(sentinels), "failed": fail_count, "results": results}, sys.stdout, indent=2)
        sys.stdout.write("\n")
    else:
        if not args.quiet:
            print(
                f"gateway TK sentinels: checking {len(sentinels)} registered entries "
                f"from {REGISTRY_PATH.relative_to(REPO_ROOT)} plus global invariants"
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
                print(f"gateway TK sentinels: PASS ({len(results)}/{len(results)} intact)")
        else:
            print(
                f"gateway TK sentinels: FAIL ({fail_count}/{len(results)} regressed)",
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
