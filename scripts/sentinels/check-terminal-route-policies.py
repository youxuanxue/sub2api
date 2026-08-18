#!/usr/bin/env python3
from __future__ import annotations

import argparse
import pathlib
import re
import sys


ROOT = pathlib.Path(__file__).resolve().parents[2]
OWNERS = (
    ROOT / "backend/internal/server/routes/gateway.go",
    ROOT / "backend/internal/server/routes/gateway_tk_openai_compat_handlers.go",
)
DIRECT_VERB = re.compile(r"\.(?:GET|POST|PUT|PATCH|DELETE|OPTIONS|HEAD|Any)\s*\(")
METHOD = re.compile(r"^http\.Method(?:Get|Post|Put|Patch|Delete|Options|Head)$")
PATH = re.compile(r'^"(?:[^"\\]|\\.)*"$')
POLICY = re.compile(r"^(?:SyncInference|StreamInference|WebSocketTurn|AsyncSubmission)$")
EXCLUDED = re.compile(r'^Excluded\("([^"\\]*(?:\\.[^"\\]*)*)"\)$')


def balanced_call(source: str, start: int) -> tuple[str, int]:
    depth = 1
    i = start
    quote = ""
    escaped = False
    while i < len(source):
        ch = source[i]
        if quote:
            if escaped:
                escaped = False
            elif ch == "\\" and quote != "`":
                escaped = True
            elif ch == quote:
                quote = ""
        elif ch in {'"', "'", "`"}:
            quote = ch
        elif source.startswith("//", i):
            newline = source.find("\n", i + 2)
            i = len(source) if newline < 0 else newline
            continue
        elif source.startswith("/*", i):
            end = source.find("*/", i + 2)
            i = len(source) if end < 0 else end + 2
            continue
        elif ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
            if depth == 0:
                return source[start:i], i + 1
        i += 1
    raise ValueError("unterminated Register call")


def split_top_level(arguments: str) -> list[str]:
    parts: list[str] = []
    start = 0
    depth = 0
    quote = ""
    escaped = False
    for i, ch in enumerate(arguments):
        if quote:
            if escaped:
                escaped = False
            elif ch == "\\" and quote != "`":
                escaped = True
            elif ch == quote:
                quote = ""
            continue
        if ch in {'"', "'", "`"}:
            quote = ch
        elif ch in "([{":
            depth += 1
        elif ch in ")]}":
            depth -= 1
        elif ch == "," and depth == 0:
            parts.append(arguments[start:i].strip())
            start = i + 1
    parts.append(arguments[start:].strip())
    return parts


def check_source(path: pathlib.Path, source: str) -> list[str]:
    errors: list[str] = []
    for match in DIRECT_VERB.finditer(source):
        line = source.count("\n", 0, match.start()) + 1
        errors.append(f"{path.relative_to(ROOT)}:{line}: direct Gin verb registration is forbidden")

    count = 0
    cursor = 0
    marker = ".Register("
    while True:
        found = source.find(marker, cursor)
        if found < 0:
            break
        line = source.count("\n", 0, found) + 1
        try:
            body, cursor = balanced_call(source, found + len(marker))
        except ValueError as exc:
            errors.append(f"{path.relative_to(ROOT)}:{line}: {exc}")
            break
        args = split_top_level(body)
        count += 1
        if len(args) < 4:
            errors.append(f"{path.relative_to(ROOT)}:{line}: Register requires method, path, policy, and handler")
            continue
        method, route_path, policy = args[:3]
        if not METHOD.fullmatch(method):
            errors.append(f"{path.relative_to(ROOT)}:{line}: method must be an http.Method* constant")
        if not PATH.fullmatch(route_path):
            errors.append(f"{path.relative_to(ROOT)}:{line}: route path must be a string literal")
        excluded = EXCLUDED.fullmatch(policy)
        if POLICY.fullmatch(policy):
            continue
        if excluded and excluded.group(1).strip():
            continue
        errors.append(f"{path.relative_to(ROOT)}:{line}: invalid or empty terminal policy {policy!r}")
    if count == 0:
        errors.append(f"{path.relative_to(ROOT)}: no terminal route registrations found")
    return errors


def selftest() -> int:
    good = 'routes.Register(http.MethodPost, "/messages", StreamInference, handler)\n'
    bad_direct = 'group.POST("/messages", handler)\n'
    bad_excluded = 'routes.Register(http.MethodGet, "/models", Excluded(""), handler)\n'
    fake = ROOT / "gateway.go"
    if check_source(fake, good):
        return 1
    if not check_source(fake, bad_direct):
        return 1
    if not check_source(fake, bad_excluded):
        return 1
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--selftest", action="store_true")
    args = parser.parse_args()
    if args.selftest:
        return selftest()

    errors: list[str] = []
    for path in OWNERS:
        errors.extend(check_source(path, path.read_text(encoding="utf-8")))
    if errors:
        for error in errors:
            print(f"terminal-route-policy: {error}", file=sys.stderr)
        return 1
    print("terminal-route-policy: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
