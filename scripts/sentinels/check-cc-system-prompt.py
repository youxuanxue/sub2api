#!/usr/bin/env python3
"""
check-cc-system-prompt.py — guard the canonical Claude Code system-prompt
anchors that TokenKey hardcodes in multiple Go files.

The CC system prompt is a load-bearing fingerprint dimension (see
scripts/sentinels/cc-system-prompt.json `rationale`). The same anchor strings
live in 3+ places that can silently diverge:

  - backend/internal/service/claude_code_validator.go  (6 inbound templates +
    billing prefix)
  - backend/internal/service/gateway_service.go         (1 injected banner +
    4 detection prefixes)

Reads `scripts/sentinels/cc-system-prompt.json` (single declared source) and:

  1. `sentinels[]`     — every literal in `must_contain` appears in `path`.
  2. `byte_identical[]`— each canonical literal appears verbatim in *all* of its
                         listed `paths` (so the injected banner stays byte-equal
                         to the validated banner).

This is a GUARD, not a generator: there is no --write. When real CC drifts
(detected by ops/anthropic/capture_cc_fingerprint.py against the same
`capture_anchors`), edit this registry + the Go copies by hand with capture
evidence, and record the decision in docs/spec-delta/cc-system-prompt.md.

Exit codes:
  0  — all anchors intact.
  1  — at least one anchor missing or a banner diverged.
  2  — registry file missing or malformed.

Usage:
  python3 scripts/sentinels/check-cc-system-prompt.py
  python3 scripts/sentinels/check-cc-system-prompt.py --quiet     # only failures
  python3 scripts/sentinels/check-cc-system-prompt.py --json      # machine report
  python3 scripts/sentinels/check-cc-system-prompt.py --selftest  # CI fixtures
"""
from __future__ import annotations

import argparse
import json
import sys
import tempfile
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
REGISTRY_PATH = REPO_ROOT / "scripts" / "sentinels" / "cc-system-prompt.json"


def load_registry(path: Path) -> dict:
    if not path.is_file():
        print(f"FATAL: registry file not found: {path}", file=sys.stderr)
        sys.exit(2)
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        print(f"FATAL: registry file is not valid JSON: {exc}", file=sys.stderr)
        sys.exit(2)
    if not isinstance(data.get("sentinels"), list):
        print("FATAL: registry missing 'sentinels' array.", file=sys.stderr)
        sys.exit(2)
    return data


def _read(root: Path, path_str: str) -> tuple[str | None, str | None]:
    """Returns (content, error). content is None on error."""
    fp = root / path_str
    if not fp.is_file():
        return None, f"file missing: {path_str}"
    try:
        return fp.read_text(encoding="utf-8", errors="replace"), None
    except OSError as exc:
        return None, f"cannot read {path_str}: {exc}"


def check_sentinel(root: Path, entry: dict) -> tuple[bool, list[str]]:
    path_str = entry.get("path")
    if not path_str:
        return False, ["entry missing 'path'"]
    content, err = _read(root, path_str)
    if content is None:
        return False, [err or "unreadable"]
    failures = [
        f"missing literal `{needle}` in {path_str}"
        for needle in (entry.get("must_contain") or [])
        if needle not in content
    ]
    return (len(failures) == 0), failures


def check_byte_identical(root: Path, group: dict) -> tuple[bool, list[str]]:
    literal = group.get("literal")
    if literal is None:
        return False, ["byte_identical entry missing 'literal'"]
    failures: list[str] = []
    for path_str in group.get("paths") or []:
        content, err = _read(root, path_str)
        if content is None:
            failures.append(err or f"unreadable: {path_str}")
        elif literal not in content:
            failures.append(
                f"canonical banner not byte-identical: `{literal}` absent in {path_str}"
            )
    return (len(failures) == 0), failures


def run_checks(
    root: Path, registry: dict, quiet: bool, as_json: bool, silent: bool = False
) -> int:
    results = []
    fail_count = 0

    for entry in registry["sentinels"]:
        ok, failures = check_sentinel(root, entry)
        fail_count += 0 if ok else 1
        results.append(
            {
                "kind": "sentinel",
                "id": entry.get("path"),
                "ok": ok,
                "failures": failures,
                "rationale": entry.get("rationale", ""),
            }
        )

    for group in registry.get("byte_identical") or []:
        ok, failures = check_byte_identical(root, group)
        fail_count += 0 if ok else 1
        results.append(
            {
                "kind": "byte_identical",
                "id": group.get("literal"),
                "ok": ok,
                "failures": failures,
                "rationale": group.get("rationale", ""),
            }
        )

    total = len(results)

    if silent:
        return 0 if fail_count == 0 else 1

    if as_json:
        json.dump(
            {"total": total, "failed": fail_count, "results": results},
            sys.stdout,
            indent=2,
            ensure_ascii=False,
        )
        sys.stdout.write("\n")
        return 0 if fail_count == 0 else 1

    if not quiet:
        print(f"cc system-prompt anchors: checking {total} entries")
    for r in results:
        if r["ok"]:
            if not quiet:
                print(f"  ok: [{r['kind']}] {r['id']}")
        else:
            print(f"  FAIL: [{r['kind']}] {r['id']}")
            for msg in r["failures"]:
                print(f"        - {msg}")
            if r["rationale"]:
                print(f"        why it matters: {r['rationale']}")

    if fail_count == 0:
        if not quiet:
            print(f"cc system-prompt anchors: PASS ({total}/{total} intact)")
    else:
        print(
            f"cc system-prompt anchors: FAIL ({fail_count}/{total} regressed)",
            file=sys.stderr,
        )
        print(
            "  Source of truth: scripts/sentinels/cc-system-prompt.json\n"
            "  Anchors changed by a real CC update? Update the registry + the Go\n"
            "  copies with capture evidence and record it in\n"
            "  docs/spec-delta/cc-system-prompt.md. Do NOT silently delete anchors.",
            file=sys.stderr,
        )
    return 0 if fail_count == 0 else 1


def run_selftest() -> int:
    failures: list[str] = []

    def expect(cond: bool, msg: str) -> None:
        if not cond:
            failures.append(msg)

    banner = "You are Claude Code, Anthropic's official CLI for Claude."
    registry = {
        "version": 1,
        "byte_identical": [
            {"literal": banner, "paths": ["a.go", "b.go"]},
        ],
        "sentinels": [
            {"path": "a.go", "must_contain": [banner, "x-anthropic-billing-header"]},
            {"path": "b.go", "must_contain": [banner]},
        ],
    }

    def write_tree(root: Path, a_body: str, b_body: str) -> None:
        (root / "a.go").write_text(a_body, encoding="utf-8")
        (root / "b.go").write_text(b_body, encoding="utf-8")

    # Case 1: compliant — both files carry the banner, a.go carries billing prefix.
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        write_tree(
            root,
            a_body=f'const x = "{banner}"\nconst y = "x-anthropic-billing-header"\n',
            b_body=f'const z = "{banner}"\n',
        )
        rc = run_checks(root, registry, quiet=True, as_json=False, silent=True)
        expect(rc == 0, "case1 compliant: expected PASS (rc=0)")

    # Case 2: missing anchor — a.go lost the billing prefix.
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        write_tree(
            root,
            a_body=f'const x = "{banner}"\n',  # billing prefix dropped
            b_body=f'const z = "{banner}"\n',
        )
        rc = run_checks(root, registry, quiet=True, as_json=False, silent=True)
        expect(rc == 1, "case2 missing-anchor: expected FAIL (rc=1)")

    # Case 3: banner split — b.go banner mutated by one char.
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        write_tree(
            root,
            a_body=f'const x = "{banner}"\nconst y = "x-anthropic-billing-header"\n',
            b_body='const z = "You are Claude Code, Anthropic\'s official CLI for Claude!"\n',
        )
        rc = run_checks(root, registry, quiet=True, as_json=False, silent=True)
        expect(rc == 1, "case3 banner-split: expected FAIL (rc=1)")

    if failures:
        print("check-cc-system-prompt selftest: FAIL", file=sys.stderr)
        for f in failures:
            print(f"  - {f}", file=sys.stderr)
        return 1
    print("check-cc-system-prompt selftest: PASS (3/3 cases)")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true", help="only print failures")
    parser.add_argument("--json", action="store_true", help="machine-readable report")
    parser.add_argument("--selftest", action="store_true", help="run internal fixtures")
    args = parser.parse_args()

    if args.selftest:
        return run_selftest()

    registry = load_registry(REGISTRY_PATH)
    return run_checks(REPO_ROOT, registry, quiet=args.quiet, as_json=args.json)


if __name__ == "__main__":
    sys.exit(main())
