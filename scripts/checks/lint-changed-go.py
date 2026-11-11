#!/usr/bin/env python3
"""Choose golangci-lint package args from the preflight diff.

Prints one token to stdout:
  FULL     lint the whole backend module
  SKIP     no changed Go packages
  ./pkg …  lint only these packages (paths relative to backend/)
"""
from __future__ import annotations

import argparse
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
FULL_FILES = {
    "backend/go.mod",
    "backend/go.sum",
    "backend/.golangci.yml",
    "backend/.golangci.yaml",
    "backend/.golangci.toml",
}


def _truthy(value: str | None) -> bool:
    return (value or "").lower() in {"1", "true", "yes"}


def _git_paths(root: Path, *args: str) -> list[str]:
    try:
        out = subprocess.check_output(
            ["git", "-C", str(root), *args],
            stderr=subprocess.DEVNULL,
        )
    except (OSError, subprocess.CalledProcessError):
        return []
    return [line for line in out.decode().splitlines() if line]


def collect_paths(root: Path, base: str) -> list[str]:
    """Branch commits plus the pending index and worktree, including untracked files."""
    found: set[str] = set()
    found.update(_git_paths(root, "diff", "--name-only", "--no-renames", f"{base}...HEAD"))
    found.update(_git_paths(root, "diff", "--name-only", "--no-renames"))
    found.update(_git_paths(root, "diff", "--cached", "--name-only", "--no-renames"))
    found.update(_git_paths(root, "ls-files", "--others", "--exclude-standard"))
    return sorted(found)


def classify(paths: list[str], *, force_full: bool = False, root: Path | None = None) -> str:
    if force_full or any(path in FULL_FILES for path in paths):
        return "FULL"
    packages: set[str] = set()
    for path in paths:
        if not path.startswith("backend/") or not path.endswith(".go"):
            continue
        parent = Path(path[len("backend/"):]).parent.as_posix()
        package = "." if parent == "." else "./" + parent
        if root is not None and not (root / "backend" / parent).is_dir():
            continue
        packages.add(package)
    if not packages:
        return "SKIP"
    return " ".join(sorted(packages))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--packages", action="store_true", help="print FULL, SKIP, or package paths")
    parser.parse_args()
    base = os.environ.get("PREFLIGHT_BASE", "origin/main")
    decision = classify(
        collect_paths(ROOT, base),
        force_full=_truthy(os.environ.get("PREFLIGHT_FULL_LINT")),
        root=ROOT,
    )
    print(decision)
    return 0


if __name__ == "__main__":
    sys.exit(main())
