#!/usr/bin/env python3
"""sub2api: ops/deploy SQL soft-delete filter gate.

Hand-written operational SQL (psql embedded in ops/ and deploy/ shell, python and
.sql templates) bypasses Ent's soft-delete interceptor. The interceptor auto-adds
`deleted_at IS NULL` to every Go query, but a hand-written query that omits it
silently resurrects soft-deleted *ghost* rows — and soft-delete does NOT reset
`status` / `schedulable`, so a deleted row still reads `status=active
schedulable=true`. This has repeatedly misled operators (e.g. a duplicate account
soft-deleted a month ago still showing as a live schedulable account in a caps
probe).

This gate scans the operational dirs and flags every `FROM`/`JOIN` over a
soft-delete table whose SQL statement contains no `deleted_at` filter at all.
Recall-oriented: it only fires when `deleted_at` is entirely absent from the
statement (the exact failure mode seen in practice), so a query that already
filters is never touched.

Escape hatch: if a query genuinely must include soft-deleted rows (reaper /
audit / restore / orphan-finder / soft-delete verify), put the marker
`ops-allow-soft-deleted` in a comment inside the statement; the gate then skips it.

Scope: SCAN_DIRS (ops/ + deploy/) × SCAN_EXTS (.sh/.py/.sql) — every place that
runs hand-written SQL against a live edge/prod DB. Deliberately excluded: backend/
(covered by the Ent interceptor), backend/migrations/ (DDL/backfill follow other
rules), and scripts/ (CI/static helpers, and this gate's own selftest fixtures
embed `FROM accounts ...` strings that must not self-trip).

Usage:
  ops-sql-soft-delete.py [--quiet]   # exit 1 if any unfiltered query found
  ops-sql-soft-delete.py --selftest  # run embedded fixtures
"""
import argparse
import os
import re
import sys

SOFT_DELETE_TABLES = (
    "accounts",
    "users",
    "groups",
    "proxies",
    "api_keys",
    "user_subscriptions",
    "user_attribute_definitions",
    "user_platform_quotas",
)
MARKER = "ops-allow-soft-deleted"
WINDOW = 40  # max lines of a multi-line statement to scan forward for deleted_at

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SCAN_DIRS = ("ops", "deploy")
SCAN_EXTS = (".sh", ".py", ".sql")

# Longest table names first so `user_subscriptions` wins over `users`.
_TABLE_ALT = "|".join(sorted(SOFT_DELETE_TABLES, key=len, reverse=True))
FROM_RE = re.compile(r'\b(?:FROM|JOIN)\s+"?(' + _TABLE_ALT + r')"?\b', re.IGNORECASE)


def _statement_window(lines, idx):
    """Join lines[idx:] up to and including the first line with ';' (statement end),
    capped at WINDOW lines. SQL here ends with ';' (heredoc/psql -c) or is a single
    line (f-string fragments), so this bounds each statement without bleeding into
    the next one."""
    out = []
    for j in range(idx, min(idx + WINDOW, len(lines))):
        out.append(lines[j])
        if ";" in lines[j]:
            break
    return "\n".join(out)


def scan_text(text):
    """Return list of (lineno, table, snippet) for unfiltered soft-delete-table queries."""
    lines = text.splitlines()
    findings = []
    for i, line in enumerate(lines):
        for m in FROM_RE.finditer(line):
            window = _statement_window(lines, i).lower()
            if "deleted_at" in window or MARKER in window:
                continue
            findings.append((i + 1, m.group(1).lower(), line.strip()))
    return findings


def _iter_target_files():
    for scan_dir in SCAN_DIRS:
        base = os.path.join(ROOT, scan_dir)
        if not os.path.isdir(base):
            continue
        for dirpath, dirnames, filenames in os.walk(base):
            dirnames[:] = [d for d in dirnames if d != "__pycache__"]
            for fn in sorted(filenames):
                if not fn.endswith(SCAN_EXTS):
                    continue
                if fn.startswith("test_") or fn.endswith("_test.py") or fn.endswith("_test.sh"):
                    continue
                yield os.path.join(dirpath, fn)


def run(quiet):
    findings = []
    for path in _iter_target_files():
        with open(path, encoding="utf-8", errors="replace") as fh:
            text = fh.read()
        for lineno, table, snippet in scan_text(text):
            findings.append((os.path.relpath(path, ROOT), lineno, table, snippet))

    if findings:
        print("ops/deploy SQL soft-delete filter gate: FAIL")
        print("  Hand-written ops/deploy SQL over a soft-delete table is missing a `deleted_at` filter.")
        print("  Soft-deleted rows keep status=active/schedulable=true and will leak into the result.")
        print("  Fix: add `AND deleted_at IS NULL` (alias-qualified in joins), or if the query")
        print(f"  intentionally needs soft-deleted rows, add a `{MARKER}` comment in the statement.")
        for rel, lineno, table, snippet in findings:
            print(f"  - {rel}:{lineno}  table={table}")
            print(f"      {snippet}")
        return 1

    if not quiet:
        scanned = " + ".join(SCAN_DIRS)
        print(f"ops/deploy SQL soft-delete filter gate: PASS (all soft-delete-table queries in {scanned} filtered)")
    return 0


# --- self-test ---------------------------------------------------------------

_SELFTEST = [
    ("single line, no filter -> flag", "psql -c \"SELECT id FROM accounts WHERE platform='x';\"", 1),
    ("single line, filtered -> ok", "psql -c \"SELECT id FROM accounts WHERE platform='x' AND deleted_at IS NULL;\"", 0),
    (
        "multi-line heredoc, no filter -> flag",
        "SELECT row_to_json(t) FROM (\n  SELECT id FROM accounts a\n  LEFT JOIN account_groups ag ON ag.account_id=a.id\n  WHERE a.platform='x'\n  ORDER BY a.id\n) t;",
        1,
    ),
    (
        "multi-line heredoc, filtered -> ok",
        "SELECT row_to_json(t) FROM (\n  SELECT id FROM accounts a\n  WHERE a.platform='x' AND a.deleted_at IS NULL\n  ORDER BY a.id\n) t;",
        0,
    ),
    ("junction table account_groups -> ok (not soft-delete)", "SELECT * FROM account_groups b WHERE b.account_id IN (1,2);", 0),
    ("information_schema reference -> ok", "SELECT * FROM information_schema.columns WHERE table_name='accounts';", 0),
    ("explicit marker -> ok", "SELECT id FROM users WHERE id=1; -- ops-allow-soft-deleted reaper sweep", 0),
    ("groups by id set, no filter -> flag", "SELECT * FROM groups g WHERE g.id IN (SELECT group_id FROM account_groups);", 1),
    ("user_subscriptions, no filter -> flag", "SELECT * FROM user_subscriptions WHERE user_id=1;", 1),
    ("non-soft-delete usage_logs -> ok", "SELECT count(*) FROM usage_logs WHERE account_id=1;", 0),
]


def selftest():
    failures = 0
    for name, text, want in _SELFTEST:
        got = len(scan_text(text))
        ok = got == want
        print(f"  {'PASS' if ok else 'FAIL'} {name} (flagged={got} want={want})")
        if not ok:
            failures += 1
    if failures:
        print(f"ops-sql-soft-delete selftest: FAIL ({failures} case(s))")
        return 1
    print(f"ops-sql-soft-delete selftest: PASS ({len(_SELFTEST)}/{len(_SELFTEST)})")
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--quiet", action="store_true", help="print only on failure")
    ap.add_argument("--selftest", action="store_true", help="run embedded fixtures and exit")
    args = ap.parse_args()
    if args.selftest:
        sys.exit(selftest())
    sys.exit(run(args.quiet))


if __name__ == "__main__":
    main()
