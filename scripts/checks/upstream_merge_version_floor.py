#!/usr/bin/env python3
"""Fail merge/upstream-* when backend VERSION would regress below origin/main.

TokenKey tags Stage0 releases from backend/cmd/server/VERSION. An upstream-merge
branch that still carries an older VERSION (e.g. 1.8.238 while main already
shipped 1.8.239) lands a silent downgrade on main and confuses the next
release-tag / image tag story.

This gate compares HEAD's VERSION to origin/main's VERSION with a simple
numeric X.Y.Z floor. Non-merge branches skip. Missing origin/main skips with
a warning so offline checkouts are not hard-blocked.

Usage:
  python3 scripts/checks/upstream_merge_version_floor.py
  python3 scripts/checks/upstream_merge_version_floor.py --selftest
"""
from __future__ import annotations

import argparse
import os
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
VERSION_PATH = "backend/cmd/server/VERSION"


def _run(args: list[str]) -> str:
    return subprocess.check_output(args, cwd=REPO, text=True).strip()


def _branch_name() -> str:
    return (
        os.environ.get("GITHUB_HEAD_REF")
        or os.environ.get("GITHUB_REF_NAME")
        or _run(["git", "branch", "--show-current"])
    )


def _is_merge_upstream(branch: str) -> bool:
    return branch.startswith("merge/upstream-")


def parse_version(raw: str) -> tuple[int, ...]:
    text = raw.strip()
    if not text:
        raise ValueError("empty VERSION")
    parts = text.split(".")
    if len(parts) < 2:
        raise ValueError(f"VERSION must look like X.Y.Z, got {text!r}")
    return tuple(int(p) for p in parts)


def compare_floor(head: str, main: str) -> str | None:
    """Return an error message when head < main; otherwise None."""
    head_v = parse_version(head)
    main_v = parse_version(main)
    if head_v < main_v:
        return (
            f"VERSION floor failed: HEAD={head.strip()} < origin/main={main.strip()}. "
            f"Bump {VERSION_PATH} to >= {main.strip()} before merging."
        )
    return None


def read_head_version() -> str:
    return (REPO / VERSION_PATH).read_text(encoding="utf-8")


def read_origin_main_version() -> str | None:
    try:
        return _run(["git", "show", f"origin/main:{VERSION_PATH}"])
    except subprocess.CalledProcessError:
        return None


def selftest() -> int:
    cases = [
        ("1.8.240", "1.8.239", None),
        ("1.8.239", "1.8.239", None),
        ("1.8.238", "1.8.239", "VERSION floor failed"),
        ("1.9.0", "1.8.999", None),
    ]
    for head, main, expect_substr in cases:
        err = compare_floor(head, main)
        if expect_substr is None:
            if err is not None:
                print(f"selftest fail: {head=} {main=} got {err!r}", file=sys.stderr)
                return 1
        elif err is None or expect_substr not in err:
            print(f"selftest fail: {head=} {main=} got {err!r}", file=sys.stderr)
            return 1
    try:
        parse_version("not-a-version")
    except ValueError:
        pass
    else:
        print("selftest fail: parse_version should reject garbage", file=sys.stderr)
        return 1
    print("ok: upstream_merge_version_floor selftest")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--selftest", action="store_true")
    args = parser.parse_args()
    if args.selftest:
        return selftest()

    branch = _branch_name()
    if not _is_merge_upstream(branch):
        print(f"skip: branch {branch!r} is not merge/upstream-*")
        return 0

    main_ver = read_origin_main_version()
    if main_ver is None:
        print("warn: origin/main VERSION unavailable; skip floor check", file=sys.stderr)
        return 0

    head_ver = read_head_version()
    err = compare_floor(head_ver, main_ver)
    if err:
        print(f"FAIL: {err}", file=sys.stderr)
        return 1
    print(f"ok: VERSION floor {head_ver.strip()} >= origin/main {main_ver.strip()}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
