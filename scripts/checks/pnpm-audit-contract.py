#!/usr/bin/env python3
"""Keep frontend security workflows on the shared, bulk-advisory audit owner."""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
ACTION = REPO_ROOT / ".github" / "actions" / "pnpm-audit" / "action.yml"
WORKFLOWS = (
    REPO_ROOT / ".github" / "workflows" / "backend-ci.yml",
    REPO_ROOT / ".github" / "workflows" / "security-scan.yml",
)
ACTION_REF = "uses: ./.github/actions/pnpm-audit"
ACTION_ANCHORS = (
    "version: 11.7.0",
    "--registry=https://registry.npmjs.org/",
    '("advisories", "vulnerabilities", "metadata")',
    "tools/check_pnpm_audit_exceptions.py",
)


def violations() -> list[str]:
    errors: list[str] = []
    action = ACTION.read_text(encoding="utf-8")
    for anchor in ACTION_ANCHORS:
        if anchor not in action:
            errors.append(f"{ACTION.relative_to(REPO_ROOT)} missing {anchor!r}")
    for path in WORKFLOWS:
        text = path.read_text(encoding="utf-8")
        if text.count(ACTION_REF) != 1:
            errors.append(f"{path.relative_to(REPO_ROOT)} must call the pnpm audit owner exactly once")
        if "pnpm audit" in text:
            errors.append(f"{path.relative_to(REPO_ROOT)} must not carry a second inline audit flow")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true")
    args = parser.parse_args()
    errors = violations()
    if errors:
        for error in errors:
            print(f"FAIL: {error}", file=sys.stderr)
        return 1
    if not args.quiet:
        print("pnpm audit contract: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
