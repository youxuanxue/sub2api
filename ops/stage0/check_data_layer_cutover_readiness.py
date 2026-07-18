#!/usr/bin/env python3
"""Block prod RDS cutover while prod-capable ops still require local Postgres."""

from __future__ import annotations

import argparse
import pathlib
import re
import sys


LOCAL_POSTGRES = re.compile(r"docker\s+exec\s+tokenkey-postgres\b")


def candidate_files(root: pathlib.Path) -> list[pathlib.Path]:
    files = [root / "ops/stage0/sync-feishu-config.sh"]
    files.extend(sorted((root / "ops/observability").glob("*.sh")))
    return [path for path in files if path.is_file()]


def blockers(root: pathlib.Path) -> list[pathlib.Path]:
    return [
        path.relative_to(root)
        for path in candidate_files(root)
        if LOCAL_POSTGRES.search(path.read_text(encoding="utf-8"))
    ]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--root",
        type=pathlib.Path,
        default=pathlib.Path(__file__).resolve().parents[2],
    )
    args = parser.parse_args()
    found = blockers(args.root.resolve())
    if found:
        print(
            "data-layer cutover blocked: prod-capable consumers still require "
            "tokenkey-postgres; migrate them to /usr/local/bin/tokenkey-psql:",
            file=sys.stderr,
        )
        for path in found:
            print(f"  - {path}", file=sys.stderr)
        return 1
    print("data-layer cutover readiness: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
