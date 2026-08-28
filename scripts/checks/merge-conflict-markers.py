#!/usr/bin/env python3
"""Reject unresolved line-level merge conflict markers in repository text files."""

from __future__ import annotations

import argparse
import re
from pathlib import Path


MARKER = re.compile(r"^(?:<{7}|>{7})(?:\s|$)")
SKIP_DIRS = {
    ".git",
    ".cache",
    ".idea",
    ".vscode",
    "coverage",
    "dist",
    "node_modules",
    "vendor",
}


def check(root: Path) -> list[str]:
    errors: list[str] = []
    for path in sorted(root.rglob("*")):
        if not path.is_file() or any(part in SKIP_DIRS for part in path.relative_to(root).parts):
            continue
        try:
            lines = path.read_text(encoding="utf-8").splitlines()
        except (UnicodeDecodeError, OSError):
            continue
        for line_number, line in enumerate(lines, 1):
            if MARKER.match(line):
                errors.append(f"{path.relative_to(root)}:{line_number}: unresolved merge conflict marker")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--quiet", action="store_true")
    args = parser.parse_args()

    errors = check(args.root.resolve())
    if errors:
        for error in errors:
            print(error)
        return 1
    if not args.quiet:
        print("ok: no unresolved merge conflict markers")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
