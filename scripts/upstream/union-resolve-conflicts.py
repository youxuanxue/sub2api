#!/usr/bin/env python3
"""Union-resolve git merge conflicts by keeping both HEAD and upstream blocks.

Safe only for additive conflicts where each side introduced distinct lines.
Skips files listed in --skip and leaves conflict markers when blocks overlap
identically (same stripped content).
"""
from __future__ import annotations

import argparse
import re
import subprocess
import sys
from pathlib import Path

CONFLICT_RE = re.compile(
    r"^<<<<<<< HEAD\n(.*?)^=======\n(.*?)^>>>>>>> upstream/main\n",
    re.MULTILINE | re.DOTALL,
)


def union_resolve(text: str) -> tuple[str, int, int]:
    resolved = 0
    skipped = 0

    def repl(match: re.Match[str]) -> str:
        nonlocal resolved, skipped
        ours = match.group(1)
        theirs = match.group(2)
        ours_s = ours.rstrip("\n")
        theirs_s = theirs.rstrip("\n")
        if ours_s == theirs_s:
            resolved += 1
            return ours
        if not ours_s:
            resolved += 1
            return theirs
        if not theirs_s:
            resolved += 1
            return ours
        # Both non-empty: union with newline separator if needed.
        resolved += 1
        if ours.endswith("\n") or not ours:
            join = ours + theirs
        else:
            join = ours + "\n" + theirs
        return join

    out = CONFLICT_RE.sub(repl, text)
    remaining = out.count("<<<<<<<")
    skipped = remaining
    return out, resolved, skipped


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("paths", nargs="*", help="Files to process (default: all unmerged)")
    parser.add_argument("--skip", nargs="*", default=[], help="Basenames or paths to skip")
    args = parser.parse_args()

    skip = set(args.skip)
    if args.paths:
        files = [Path(p) for p in args.paths]
    else:
        out = subprocess.check_output(
            ["git", "diff", "--name-only", "--diff-filter=U"], text=True
        )
        files = [Path(p.strip()) for p in out.splitlines() if p.strip()]

    total_resolved = 0
    still_conflict: list[str] = []

    for path in files:
        if str(path) in skip or path.name in skip:
            continue
        if not path.exists():
            continue
        text = path.read_text(encoding="utf-8", errors="replace")
        if "<<<<<<<" not in text:
            continue
        new_text, resolved, remaining = union_resolve(text)
        if remaining:
            still_conflict.append(str(path))
        if new_text != text:
            path.write_text(new_text, encoding="utf-8")
            total_resolved += resolved

    print(f"union-resolved hunks: {total_resolved}")
    print(f"files still with markers: {len(still_conflict)}")
    for p in still_conflict[:30]:
        print(f"  {p}")
    if len(still_conflict) > 30:
        print(f"  ... and {len(still_conflict) - 30} more")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
