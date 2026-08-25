#!/usr/bin/env python3
"""List Go packages that gain test files when the integration tag is enabled."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import subprocess
import sys


def _go_list(root: Path, tags: str | None = None) -> dict[str, dict[str, object]]:
    command = ["go", "list", "-json"]
    if tags:
        command.append(f"-tags={tags}")
    command.append("./...")
    result = subprocess.run(
        command,
        cwd=root,
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise RuntimeError(result.stderr.strip() or "go list failed")

    packages: dict[str, dict[str, object]] = {}
    decoder = json.JSONDecoder()
    offset = 0
    while offset < len(result.stdout):
        while offset < len(result.stdout) and result.stdout[offset].isspace():
            offset += 1
        if offset >= len(result.stdout):
            break
        package, offset = decoder.raw_decode(result.stdout, offset)
        packages[str(package["ImportPath"])] = package
    return packages


def integration_packages(root: Path) -> list[str]:
    root = root.resolve()
    baseline = _go_list(root)
    tagged = _go_list(root, "integration")
    selected: list[str] = []
    for import_path, package in tagged.items():
        baseline_package = baseline.get(import_path, {})
        baseline_tests = set(baseline_package.get("TestGoFiles", [])) | set(
            baseline_package.get("XTestGoFiles", [])
        )
        tagged_tests = set(package.get("TestGoFiles", [])) | set(
            package.get("XTestGoFiles", [])
        )
        if not tagged_tests - baseline_tests:
            continue
        relative = Path(str(package["Dir"])).resolve().relative_to(root)
        selected.append("." if relative == Path(".") else f"./{relative.as_posix()}")
    return sorted(selected)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path.cwd())
    args = parser.parse_args(argv)
    try:
        packages = integration_packages(args.root)
    except (OSError, RuntimeError, ValueError) as exc:
        print(f"integration package discovery failed: {exc}", file=sys.stderr)
        return 1
    if not packages:
        print("no integration-tagged test packages found", file=sys.stderr)
        return 1
    print("\n".join(packages))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
