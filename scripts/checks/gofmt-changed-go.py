#!/usr/bin/env python3
"""Fail when changed Go files in the branch or working tree are not gofmt-clean."""
from __future__ import annotations

import argparse
import fnmatch
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def _git_lines(args: list[str]) -> list[str]:
    proc = subprocess.run(
        ["git", *args],
        cwd=ROOT,
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        detail = (proc.stderr or proc.stdout or "git failed").strip()
        raise RuntimeError(f"git {' '.join(args)}: {detail}")
    return [line.strip() for line in proc.stdout.splitlines() if line.strip()]


def collect_changed_go_files(base: str) -> list[str]:
    files: set[str] = set()
    if subprocess.run(
        ["git", "rev-parse", "--verify", f"{base}^{{commit}}"],
        cwd=ROOT,
        capture_output=True,
        check=False,
    ).returncode == 0:
        files.update(_git_lines(["diff", "--name-only", f"{base}...HEAD", "--", "*.go"]))
    files.update(_git_lines(["diff", "--name-only", "--", "*.go"]))
    files.update(_git_lines(["diff", "--cached", "--name-only", "--", "*.go"]))
    files.update(_git_lines(["ls-files", "--others", "--exclude-standard"]))
    return sorted(
        path
        for path in files
        if fnmatch.fnmatch(path, "*.go") and (ROOT / path).is_file()
    )


def gofmt_dirty(files: list[str]) -> list[str]:
    if not files:
        return []
    proc = subprocess.run(
        ["gofmt", "-l", *files],
        cwd=ROOT,
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode not in (0, 1):
        detail = (proc.stderr or proc.stdout or "gofmt failed").strip()
        raise RuntimeError(detail)
    return [line.strip() for line in proc.stdout.splitlines() if line.strip()]


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--base", default="origin/main")
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args()

    try:
        files = collect_changed_go_files(args.base)
        dirty = gofmt_dirty(files)
    except RuntimeError as exc:
        if not args.quiet:
            print(f"gofmt-changed-go: {exc}", file=sys.stderr)
        return 2

    if dirty:
        if not args.quiet:
            print("gofmt-changed-go: changed Go files are not gofmt-clean:")
            for path in dirty:
                print(f"  {path}")
            print("Run: gofmt -w <files>")
        return 1

    if not args.quiet:
        if files:
            print(f"gofmt-changed-go: ok ({len(files)} changed Go file(s))")
        else:
            print("gofmt-changed-go: ok (no changed Go files)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
