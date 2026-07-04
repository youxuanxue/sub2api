#!/usr/bin/env python3
"""Verify endpoint-compat-baseline.md tracks the deployed server VERSION."""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
BASELINE = REPO_ROOT / "docs/ops/endpoint-compat-baseline.md"
VERSION_FILE = REPO_ROOT / "backend/cmd/server/VERSION"


def read_version() -> str:
    if not VERSION_FILE.is_file():
        raise SystemExit(f"missing VERSION file: {VERSION_FILE}")
    version = VERSION_FILE.read_text(encoding="utf-8").strip()
    if not re.fullmatch(r"\d+\.\d+\.\d+", version):
        raise SystemExit(f"invalid VERSION file contents: {version!r}")
    return version


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.parse_args()

    version = read_version()
    if not BASELINE.is_file():
        print(f"endpoint-compat baseline freshness: FAIL missing {BASELINE}", file=sys.stderr)
        return 1

    if f"v{version}" not in BASELINE.read_text(encoding="utf-8"):
        print(
            "endpoint-compat baseline freshness: FAIL "
            f"docs/ops/endpoint-compat-baseline.md must mention deployed runtime anchor v{version}",
            file=sys.stderr,
        )
        return 1

    print(f"endpoint-compat baseline freshness: ok (runtime anchor v{version})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
