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


def resolve_range(base: str | None, head: str) -> tuple[str | None, str | None]:
    candidates = []
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


def scan_file(path: Path) -> list[str]:
    text = path.read_text(errors="replace")
    if ACK in text:
        return []
    body = strip_comments(text)
    hits = [name for name, pattern in DANGEROUS if pattern.search(body)]
    return hits


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--base", default=os.environ.get("PREFLIGHT_BASE"))
    ap.add_argument("--head", default="HEAD")
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args()

    base, head = resolve_range(args.base, args.head)
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
