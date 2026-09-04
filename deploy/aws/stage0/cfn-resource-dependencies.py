#!/usr/bin/env python3
"""Fail when CloudFormation DependsOn names an undefined resource.

CloudFormation accepts scalar, inline-list, and block-list DependsOn forms.
This check deliberately validates only that contract, using the stable
top-level indentation of repository templates so preflight remains stdlib-only.
"""
from __future__ import annotations

import pathlib
import re
import sys


REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
CFN_DIR = REPO_ROOT / "deploy/aws/cloudformation"
SECTION_RE = re.compile(r"^([A-Za-z][A-Za-z0-9]*):(?:\s.*)?$")
RESOURCE_RE = re.compile(r"^  ([A-Za-z][A-Za-z0-9]*):(?:\s.*)?$")
DEPENDS_ON_RE = re.compile(r"^(\s+)DependsOn:\s*(.*?)\s*$")
BLOCK_ITEM_RE = re.compile(r"^\s+-\s+(['\"]?)([A-Za-z][A-Za-z0-9]*)\1\s*$")


def _resource_ids(lines: list[str]) -> set[str]:
    resources: set[str] = set()
    in_resources = False
    for line in lines:
        section = SECTION_RE.match(line)
        if section:
            in_resources = section.group(1) == "Resources"
            continue
        if in_resources:
            match = RESOURCE_RE.match(line)
            if match:
                resources.add(match.group(1))
    return resources


def _inline_values(raw: str) -> list[str]:
    raw = raw.strip()
    if raw.startswith("[") and raw.endswith("]"):
        raw = raw[1:-1]
        return [item.strip().strip("'\"") for item in raw.split(",") if item.strip()]
    return [raw.strip("'\"")] if raw else []


def _dependencies(lines: list[str]) -> list[tuple[int, str]]:
    dependencies: list[tuple[int, str]] = []
    for index, line in enumerate(lines):
        match = DEPENDS_ON_RE.match(line)
        if not match:
            continue
        indent = len(match.group(1))
        raw = match.group(2)
        if raw:
            dependencies.extend((index + 1, value) for value in _inline_values(raw))
            continue
        for nested_index in range(index + 1, len(lines)):
            nested = lines[nested_index]
            if not nested.strip() or nested.lstrip().startswith("#"):
                continue
            nested_indent = len(nested) - len(nested.lstrip())
            if nested_indent <= indent:
                break
            item = BLOCK_ITEM_RE.match(nested)
            if item:
                dependencies.append((nested_index + 1, item.group(2)))
    return dependencies


def check(path: pathlib.Path) -> list[str]:
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        return [f"cannot read: {exc}"]
    resources = _resource_ids(lines)
    if not resources:
        return []
    return [
        f"line {line}: DependsOn references undefined resource {dependency!r}"
        for line, dependency in _dependencies(lines)
        if dependency not in resources
    ]


def main(argv: list[str]) -> int:
    paths = [pathlib.Path(value) for value in argv]
    if not paths and CFN_DIR.is_dir():
        paths = sorted((*CFN_DIR.rglob("*.yaml"), *CFN_DIR.rglob("*.yml")))
    failures: list[tuple[pathlib.Path, str]] = []
    for path in paths:
        failures.extend((path, message) for message in check(path))
    if failures:
        print("FAIL: invalid CloudFormation resource dependencies:", file=sys.stderr)
        for path, message in failures:
            try:
                display = path.resolve().relative_to(REPO_ROOT)
            except ValueError:
                display = path
            print(f"  - {display}: {message}", file=sys.stderr)
        return 1
    print(f"ok: {len(paths)} CloudFormation template(s) have resolvable DependsOn resources")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
