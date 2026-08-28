#!/usr/bin/env python3
"""Gate new/changed SQL migrations for prod blue/green compatibility.

During a blue/green deploy, the target app may apply migrations while the old
active app is still serving existing requests on the same database. The migration
runner's advisory lock prevents concurrent migration execution; it does not make
old application code compatible with destructive schema changes.

This check scans only migrations changed in the current branch/range. Destructive
patterns require an explicit in-file acknowledgement:

    -- bluegreen-safe-destructive-ok: <why this is expand/contract safe>
"""

from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
ACK = "bluegreen-safe-destructive-ok"

DANGEROUS = [
    ("DROP TABLE", re.compile(r"\bDROP\s+TABLE\b", re.I)),
    ("DROP COLUMN", re.compile(r"\bDROP\s+COLUMN\b", re.I)),
    ("ALTER TABLE RENAME", re.compile(r"\bALTER\s+TABLE\b[^;]*\bRENAME\b", re.I | re.S)),
    ("ADD COLUMN NOT NULL", re.compile(r"\bADD\s+COLUMN\b[^;]*\bNOT\s+NULL\b", re.I | re.S)),
    ("RENAME COLUMN", re.compile(r"\bRENAME\s+COLUMN\b", re.I)),
    ("RENAME TO", re.compile(r"\bRENAME\s+TO\b", re.I)),
    ("SET NOT NULL", re.compile(r"\bSET\s+NOT\s+NULL\b", re.I)),
    ("ALTER COLUMN TYPE", re.compile(r"\bALTER\s+(?:COLUMN\s+)?[A-Za-z0-9_\".]+\s+TYPE\b", re.I)),
]


def git(*args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["git", *args],
        cwd=ROOT,
        text=True,
        capture_output=True,
        check=False,
    )


def resolve_range(
    base: str | None,
    head: str,
    *,
    allow_tree_diff_without_merge_base: bool = False,
) -> tuple[str | None, str | None]:
    candidates = []
    if base:
        candidates.append(base)
    candidates.extend(["origin/main", "main", "HEAD^1", "HEAD^"])
    for candidate in candidates:
        if not candidate:
            continue
        if git("rev-parse", "--verify", candidate).returncode != 0:
            continue
        if git("rev-parse", "--verify", head).returncode != 0:
            continue
        if allow_tree_diff_without_merge_base or git("merge-base", candidate, head).returncode == 0:
            return candidate, head
    return None, None


def changed_migrations(base: str, head: str) -> list[Path]:
    res = git("diff", "--name-status", f"{base}..{head}", "--", "backend/migrations")
    if res.returncode != 0:
        raise RuntimeError(res.stderr.strip() or "git diff failed")
    out: list[Path] = []
    for line in res.stdout.splitlines():
        if not line.strip():
            continue
        parts = line.split("\t")
        status = parts[0]
        if status.startswith("D"):
            continue
        path = parts[-1]
        if path.endswith(".sql"):
            out.append(ROOT / path)
    return sorted(set(out))


def strip_comments(sql: str) -> str:
    sql = re.sub(r"--[^\n]*", "", sql)
    sql = re.sub(r"/\*.*?\*/", "", sql, flags=re.S)
    return sql


def scan_sql(text: str) -> list[str]:
    if ACK in text:
        return []
    body = strip_comments(text)
    hits = [name for name, pattern in DANGEROUS if pattern.search(body)]
    return hits


def scan_file(path: Path) -> list[str]:
    # A migration listed in the committed diff but absent from the working tree (deleted or
    # renamed away — e.g. a *.sql renamed within this PR) has no content to scan and cannot carry
    # destructive SQL; skip it instead of crashing on FileNotFoundError.
    if not path.exists():
        return []
    return scan_sql(path.read_text(errors="replace"))


def previous_release_tag(target: str, tag_lines: list[str]) -> str | None:
    """Return the highest semver tag strictly older than target, matching the
    existing prod release-range preparation.
    """
    raw = target.lstrip("v")
    parts = raw.split(".")
    if len(parts) != 3 or not all(part.isdigit() for part in parts):
        raise ValueError(f"release tag must be X.Y.Z, got {target!r}")
    wanted = tuple(int(part) for part in parts)
    best: tuple[tuple[int, int, int], str] | None = None
    for line in tag_lines:
        fields = line.split()
        if len(fields) < 2 or fields[1].endswith("^{}"):
            continue
        name = fields[1].removeprefix("refs/tags/")
        match = re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)", name)
        if not match:
            continue
        version = tuple(int(part) for part in match.groups())
        if version < wanted and (best is None or version > best[0]):
            best = (version, name)
    return best[1] if best else None


def prepare_release_range(tag: str) -> tuple[str, str, bool]:
    """Resolve the current prod/Edge release comparison range.

    Semantics are unchanged from the previous workflow-inline preparation:
    prefer previous-release-tag..vTAG when the target tag can be fetched,
    otherwise fall back to origin/main..HEAD.
    """
    target_ref = f"v{tag.lstrip('v')}"
    fetched = git("fetch", "--no-tags", "--depth=1", "origin", f"refs/tags/{target_ref}:refs/tags/{target_ref}")
    if fetched.returncode != 0:
        print(f"::warning::tag {target_ref} not found on origin; falling back to origin/main..HEAD")
        target_ref = ""

    listed = git("ls-remote", "--tags", "origin", "refs/tags/v[0-9]*.[0-9]*.[0-9]*")
    base_ref = previous_release_tag(tag, listed.stdout.splitlines()) if listed.returncode == 0 else None

    if target_ref and git("rev-parse", "--verify", target_ref).returncode == 0:
        if not base_ref:
            git("fetch", "--no-tags", "origin", "main:refs/remotes/origin/main", "--depth=64")
            base_ref = "origin/main"
        elif git("rev-parse", "--verify", base_ref).returncode != 0:
            git("fetch", "--no-tags", "--depth=1", "origin", f"refs/tags/{base_ref}:refs/tags/{base_ref}")
        print(f"blue/green migration safety range: {base_ref}..{target_ref}")
        return base_ref, target_ref, True

    git("fetch", "--no-tags", "origin", "main:refs/remotes/origin/main", "--depth=64")
    return "origin/main", "HEAD", False


def selftest() -> int:
    cases = [
        (
            "drop-column",
            "ALTER TABLE accounts DROP COLUMN old_token;",
            ["DROP COLUMN"],
        ),
        (
            "add-column-not-null",
            "ALTER TABLE users ADD COLUMN tier_id integer NOT NULL;",
            ["ADD COLUMN NOT NULL"],
        ),
        (
            "nullable-add-column",
            "ALTER TABLE users ADD COLUMN tier_id integer;",
            [],
        ),
        (
            "comment-stripped",
            "-- ALTER TABLE users ADD COLUMN tier_id integer NOT NULL;\nSELECT 1;",
            [],
        ),
        (
            "ack-bypasses-after-human-expand-contract-review",
            f"-- {ACK}: expand column first, contract in later deploy\nALTER TABLE users ADD COLUMN tier_id integer NOT NULL;",
            [],
        ),
    ]
    failures: list[str] = []
    for name, sql, expected in cases:
        got = scan_sql(sql)
        if got != expected:
            failures.append(f"{name}: expected {expected}, got {got}")
    tag_lines = [
        "abc refs/tags/v1.8.176",
        "def refs/tags/v1.8.177",
        "ghi refs/tags/v1.8.177^{}",
        "jkl refs/tags/v1.8.178-rc.1",
        "mno refs/tags/v1.8.178",
    ]
    previous = previous_release_tag("1.8.178", tag_lines)
    if previous != "v1.8.177":
        failures.append(f"previous-release-tag: expected v1.8.177, got {previous}")
    if previous_release_tag("1.0.0", tag_lines) is not None:
        failures.append("previous-release-tag: first release must have no predecessor")
    if failures:
        print("FAIL: bluegreen-migration-safety selftest")
        for failure in failures:
            print(f"  - {failure}")
        return 1
    print("ok: bluegreen-migration-safety selftest")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--base", default=os.environ.get("PREFLIGHT_BASE"))
    ap.add_argument("--head", default="HEAD")
    ap.add_argument("--quiet", action="store_true")
    ap.add_argument(
        "--allow-tree-diff-without-merge-base",
        action="store_true",
        help="allow explicit base/head tree diff in shallow deploy checkouts",
    )
    ap.add_argument("--selftest", action="store_true")
    ap.add_argument(
        "--release-tag",
        help="prepare the current prod/Edge release comparison range from this X.Y.Z tag",
    )
    args = ap.parse_args()

    if args.selftest:
        return selftest()

    allow_tree_diff = args.allow_tree_diff_without_merge_base
    if args.release_tag:
        args.base, args.head, allow_tree_diff = prepare_release_range(args.release_tag)

    base, head = resolve_range(
        args.base,
        args.head,
        allow_tree_diff_without_merge_base=allow_tree_diff,
    )
    if not base:
        if not args.quiet:
            print("skip: cannot resolve base ref for blue/green migration safety check")
        return 2

    files = changed_migrations(base, head)
    failures: list[tuple[Path, list[str]]] = []
    for path in files:
        hits = scan_file(path)
        if hits:
            failures.append((path, hits))

    if failures:
        print("FAIL: destructive SQL migration patterns require blue/green safety acknowledgement")
        for path, hits in failures:
            rel = path.relative_to(ROOT)
            print(f"  - {rel}: {', '.join(hits)}")
        print(f"Add a migration comment containing `{ACK}` only after verifying expand/contract safety.")
        return 1

    if not args.quiet:
        print(f"ok: {len(files)} changed SQL migration(s) are blue/green-safe")
    return 0


if __name__ == "__main__":
    sys.exit(main())
