#!/usr/bin/env python3
"""Gate modifications to already-shipped SQL migrations.

Applied migrations record a SHA256 checksum in schema_migrations. Editing an
existing file after merge causes startup failures such as:

    migration tk_044_....sql checksum mismatch (db=... file=...)

This check scans backend/migrations/*.sql changed between the merge base and
HEAD. Modifications, deletions, and renames of files that already exist on the
base ref are rejected. Add a NEW numbered migration instead.

Emergency restore of a mistakenly edited migration is allowed only when the
restored file content exactly matches a checksum already shipped on a release
tag (typically reverting to the last good release).
"""

from __future__ import annotations

import argparse
import hashlib
import os
import re
import subprocess
import sys
from pathlib import Path
from typing import NamedTuple

ROOT = Path(__file__).resolve().parents[2]
RELEASE_TAG_RE = re.compile(r"^v\d+\.\d+\.\d+$")


class Violation(NamedTuple):
    kind: str
    path: str
    detail: str = ""


def git(*args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["git", *args],
        cwd=ROOT,
        text=True,
        capture_output=True,
        check=False,
    )


def migration_checksum(content: str) -> str:
    return hashlib.sha256(content.strip().encode()).hexdigest()


def resolve_range(base: str | None, head: str) -> tuple[str | None, str | None]:
    candidates: list[str] = []
    if base:
        candidates.append(base)
    candidates.extend(["origin/main", "main", "HEAD^1", "HEAD^"])
    for candidate in candidates:
        if not candidate:
            continue
        if git("rev-parse", "--verify", candidate).returncode != 0:
            continue
        if git("merge-base", candidate, head).returncode == 0:
            return candidate, head
    return None, head


def path_exists_at_ref(ref: str, rel_path: str) -> bool:
    return git("cat-file", "-e", f"{ref}:{rel_path}").returncode == 0


def list_release_tags() -> list[str]:
    res = git("tag", "--list", "v*", "--sort=-v:refname")
    if res.returncode != 0:
        return []
    return [tag for tag in res.stdout.splitlines() if RELEASE_TAG_RE.match(tag)]


def release_tags_ancestor_of(ref: str) -> list[str]:
    return [
        tag
        for tag in list_release_tags()
        if git("merge-base", "--is-ancestor", tag, ref).returncode == 0
    ]


def release_checksums_for(rel_path: str, tags: list[str]) -> set[str]:
    out: set[str] = set()
    for tag in tags:
        show = git("show", f"{tag}:{rel_path}")
        if show.returncode != 0:
            continue
        out.add(migration_checksum(show.stdout))
    return out


def changed_migration_paths(base: str, head: str) -> list[tuple[str, str, str]]:
    """Return tuples of (status, old_path, new_path)."""
    res = git("diff", "--name-status", f"{base}..{head}", "--", "backend/migrations")
    if res.returncode != 0:
        raise RuntimeError(res.stderr.strip() or "git diff failed")
    rows: list[tuple[str, str, str]] = []
    for line in res.stdout.splitlines():
        if not line.strip():
            continue
        parts = line.split("\t")
        status = parts[0]
        if status.startswith("R") and len(parts) >= 3:
            rows.append((status, parts[1], parts[2]))
        elif len(parts) >= 2:
            rows.append((status, parts[1], parts[1]))
    return rows

def scan(base: str, head: str) -> list[Violation]:
    violations: list[Violation] = []
    shipped_tags = release_tags_ancestor_of(base)
    for status, old_path, new_path in changed_migration_paths(base, head):
        if not old_path.endswith(".sql"):
            continue

        if status.startswith("A"):
            continue

        if status.startswith("D"):
            violations.append(
                Violation("deleted", old_path, "applied migrations must not be deleted"),
            )
            continue

        if status.startswith("R"):
            if path_exists_at_ref(base, old_path):
                violations.append(
                    Violation(
                        "renamed",
                        old_path,
                        f"renamed to {new_path}; create a new migration instead",
                    ),
                )
            continue

        if not status.startswith("M"):
            continue

        if not path_exists_at_ref(base, old_path):
            continue

        base_show = git("show", f"{base}:{old_path}")
        head_show = git("show", f"{head}:{new_path}")
        if base_show.returncode != 0 or head_show.returncode != 0:
            violations.append(
                Violation("modified", old_path, "could not read base/head contents"),
            )
            continue

        base_sum = migration_checksum(base_show.stdout)
        head_sum = migration_checksum(head_show.stdout)
        if base_sum == head_sum:
            continue

        shipped = release_checksums_for(old_path, shipped_tags)
        if head_sum in shipped:
            continue

        violations.append(
            Violation(
                "modified",
                old_path,
                f"checksum changed ({base_sum[:12]}… → {head_sum[:12]}…); add a new tk_NNN migration instead",
            ),
        )
    return violations


def selftest() -> int:
    failures: list[str] = []

    cases = [
        ("empty", "", ""),
        ("spaces", "  SELECT 1;\n", "SELECT 1;"),
        ("comment", "-- hi\nSELECT 1;", "-- hi\nSELECT 1;"),
    ]
    for name, a, b in cases:
        if migration_checksum(a) != migration_checksum(b):
            failures.append(f"checksum trim mismatch: {name}")

    if failures:
        print("FAIL: migration-immutability selftest")
        for failure in failures:
            print(f"  - {failure}")
        return 1
    print("ok: migration-immutability selftest")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--base", default=os.environ.get("PREFLIGHT_BASE"))
    ap.add_argument("--head", default="HEAD")
    ap.add_argument("--quiet", action="store_true")
    ap.add_argument("--selftest", action="store_true")
    args = ap.parse_args()

    if args.selftest:
        return selftest()

    base, head = resolve_range(args.base, args.head)
    if not base:
        if not args.quiet:
            print("skip: cannot resolve base ref for migration immutability check")
        return 2

    try:
        violations = scan(base, head)
    except RuntimeError as exc:
        print(f"FAIL: migration immutability check: {exc}")
        return 1

    if violations:
        print("FAIL: already-shipped SQL migrations must stay immutable")
        for item in violations:
            suffix = f" ({item.detail})" if item.detail else ""
            print(f"  - {item.kind}: {item.path}{suffix}")
        print(
            "Create a new backend/migrations/tk_NNN_*.sql migration instead of editing an existing file."
        )
        print(
            "Emergency restore: revert the file to content that matches a shipped release tag checksum."
        )
        return 1

    if not args.quiet:
        print(f"ok: no immutable migration edits in {base}..{head}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
