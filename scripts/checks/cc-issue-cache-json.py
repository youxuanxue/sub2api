#!/usr/bin/env python3
"""Validate the anthropics/claude-code issue triage cache files.

The three `.cache/anthropic/cc-*.json` files are partly hand-curated (cc-fixes /
cc-fact-checks carry the human-pinned "TokenKey already fixed this" records) and
partly machine-generated (cc-triage). A hand-edit that breaks JSON or drops a
required top-level key would otherwise only surface in CI; this gate makes it a
local preflight FAIL.

The anthropic-cc-issue-watchdog workflow also json-validates these inline; this
mirror exists so the failure is caught before push. stdlib-only.

Exit 0 = all present files valid; exit 1 = a present file is invalid. Missing
files are OK (cc-triage is generated on first run).
"""
from __future__ import annotations

import argparse
import json
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
CACHE_DIR = REPO_ROOT / ".cache" / "anthropic"

# file -> required top-level keys
SPECS = {
    "cc-triage.json": ("version", "issues"),
    "cc-fixes.json": ("version", "issues"),
    "cc-fact-checks.json": ("version", "checks"),
}


def check_file(path: pathlib.Path, required: tuple[str, ...]) -> tuple[bool, str]:
    if not path.exists():
        return True, f"skip (absent): {path.name}"
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, OSError) as exc:
        return False, f"invalid JSON in {path.name}: {exc}"
    if not isinstance(data, dict):
        return False, f"{path.name}: top level must be a JSON object"
    missing = [k for k in required if k not in data]
    if missing:
        return False, f"{path.name}: missing required key(s): {', '.join(missing)}"
    rows = data.get(required[1])
    if not isinstance(rows, list):
        return False, f"{path.name}: '{required[1]}' must be a list"
    return True, f"ok: {path.name} ({len(rows)} entries)"


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--quiet", action="store_true")
    args = parser.parse_args()

    ok = True
    for name, required in SPECS.items():
        good, msg = check_file(CACHE_DIR / name, required)
        if not good:
            ok = False
            print(f"FAIL: {msg}", file=sys.stderr)
        elif not args.quiet:
            print(f"  {msg}")
    if not ok:
        print("cc-issue cache JSON gate: FAIL", file=sys.stderr)
        return 1
    if not args.quiet:
        print("cc-issue cache JSON gate: PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
