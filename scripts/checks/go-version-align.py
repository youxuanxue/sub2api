#!/usr/bin/env python3
"""Keep all Go toolchain pins aligned with backend/go.mod."""
from __future__ import annotations

import argparse
import re
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
VERSION = r"(\d+\.\d+\.\d+)"

SINGLE_PIN_PATTERNS = {
    "Dockerfile": re.compile(rf"^ARG GOLANG_IMAGE=golang:{VERSION}-alpine$", re.MULTILINE),
    "backend/Dockerfile": re.compile(rf"^FROM golang:{VERSION}-alpine$", re.MULTILINE),
    "deploy/Dockerfile": re.compile(rf"^ARG GOLANG_IMAGE=golang:{VERSION}-alpine$", re.MULTILINE),
    "tools/error_clustering/go.mod": re.compile(rf"^go {VERSION}$", re.MULTILINE),
}
README_PATTERNS = (
    re.compile(rf"img\.shields\.io/badge/Go-{VERSION}-00ADD8\.svg"),
    re.compile(rf"\|[^\n|]+\| Go {VERSION}, Gin, Ent \|"),
)
READMES = ("README.md", "README_CN.md", "README_JA.md")
OWNER_PATTERN = re.compile(rf"^go {VERSION}$", re.MULTILINE)


def read(path: Path) -> str:
    if not path.is_file():
        raise ValueError(f"missing required file: {path}")
    return path.read_text(encoding="utf-8")


def one_version(root: Path, rel: str, pattern: re.Pattern[str]) -> str:
    matches = pattern.findall(read(root / rel))
    if len(matches) != 1:
        raise ValueError(f"{rel}: expected exactly one Go version pin, found {len(matches)}")
    return matches[0]


def check(root: Path) -> list[str]:
    owner = one_version(root, "backend/go.mod", OWNER_PATTERN)
    failures: list[str] = []

    for rel, pattern in SINGLE_PIN_PATTERNS.items():
        try:
            actual = one_version(root, rel, pattern)
        except ValueError as exc:
            failures.append(str(exc))
            continue
        if actual != owner:
            failures.append(f"{rel}: Go {actual} != backend/go.mod Go {owner}")

    for rel in READMES:
        text = read(root / rel)
        for pattern in README_PATTERNS:
            matches = pattern.findall(text)
            if len(matches) != 1:
                failures.append(
                    f"{rel}: expected exactly one match for {pattern.pattern!r}, found {len(matches)}"
                )
                continue
            if matches[0] != owner:
                failures.append(f"{rel}: Go {matches[0]} != backend/go.mod Go {owner}")

    return failures


def write_fixture(root: Path, owner: str, docker: str | None = None) -> None:
    docker = docker or owner
    files = {
        "backend/go.mod": f"module example.test/app\n\ngo {owner}\n",
        "Dockerfile": f"ARG GOLANG_IMAGE=golang:{docker}-alpine\n",
        "backend/Dockerfile": f"FROM golang:{owner}-alpine\n",
        "deploy/Dockerfile": f"ARG GOLANG_IMAGE=golang:{owner}-alpine\n",
        "tools/error_clustering/go.mod": f"module example.test/tool\n\ngo {owner}\n",
    }
    readme = (
        f"[![Go](https://img.shields.io/badge/Go-{owner}-00ADD8.svg)](https://golang.org/)\n"
        f"| Backend | Go {owner}, Gin, Ent |\n"
    )
    files.update({rel: readme for rel in READMES})
    for rel, content in files.items():
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")


def selftest() -> int:
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        write_fixture(root, "1.26.6")
        if check(root):
            print("go-version-align selftest: aligned fixture failed", file=sys.stderr)
            return 1
        write_fixture(root, "1.26.6", docker="1.26.5")
        failures = check(root)
        if not any("Dockerfile: Go 1.26.5" in failure for failure in failures):
            print("go-version-align selftest: drift fixture passed", file=sys.stderr)
            return 1
    print("go-version-align selftest: OK")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=ROOT)
    parser.add_argument("--quiet", action="store_true")
    parser.add_argument("--selftest", action="store_true")
    args = parser.parse_args()

    if args.selftest:
        return selftest()

    try:
        failures = check(args.root.resolve())
    except (OSError, ValueError) as exc:
        print(f"go-version-align: {exc}", file=sys.stderr)
        return 2

    if failures:
        print("go-version-align: Go toolchain pins drifted from backend/go.mod:", file=sys.stderr)
        for failure in failures:
            print(f"  FAIL: {failure}", file=sys.stderr)
        return 1

    if not args.quiet:
        owner = one_version(args.root.resolve(), "backend/go.mod", OWNER_PATTERN)
        print(f"go-version-align: OK — all pins match Go {owner}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
