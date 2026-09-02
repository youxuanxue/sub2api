#!/usr/bin/env python3
"""Compatibility entrypoint for the retired diff-scoped display gate.

Static display ownership is now checked once, for the complete repository, by
``catalog-serving-drift.py`` A4. Keep this path temporarily so operator scripts
that still invoke ``check --base`` receive the canonical result instead of a
missing command. The live projection close-out remains
``ops/pricing/audit-display-coverage.py check --live``.
"""

from __future__ import annotations

import argparse
import subprocess
import sys
from pathlib import Path


REPO = Path(__file__).resolve().parents[2]
OWNER = REPO / "scripts/checks/catalog-serving-drift.py"


def run_owner(selftest: bool) -> int:
    command = [sys.executable, str(OWNER)]
    if selftest:
        command.append("--selftest")
    result = subprocess.run(command, cwd=REPO, check=False)
    if result.returncode == 0:
        print("display-coverage-gate: delegated to catalog-serving-drift A4")
    return result.returncode


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    check = sub.add_parser("check")
    check.add_argument("--base", default="origin/main", help="accepted for compatibility; ignored")
    sub.add_parser("selftest")
    args = parser.parse_args()
    return run_owner(args.command == "selftest")


if __name__ == "__main__":
    raise SystemExit(main())
