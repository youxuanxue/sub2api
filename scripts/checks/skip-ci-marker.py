#!/usr/bin/env python3
"""skip-ci-marker.py — single source of truth for the §9.2 bracketed skip-ci gate.

GitHub's CI-skip detector matches the literal substring `[skip ci]` / `[ci skip]`
anywhere in the message of the commit being tagged, ignoring context. A bracketed
marker that lands on main (via PR title, PR body, or a first-parent commit message
that becomes the squash-merge body) silently disables release.yml on any future
tag of that commit — see CLAUDE.md §9.2 (v1.3.0 incident, PR #312 re-occurrence).

This script is the ONE matcher shared by:
  - .github/workflows/main-ancestry-guard.yml       (PR title + body + commits)
  - .github/workflows/upstream-merge-pr-shape.yml   (first-parent commits)
  - scripts/preflight.sh                            (local origin/main..HEAD commits)
so the two workflows can no longer drift on grep engine (-F vs -E) or scan scope.

Only the BRACKETED forms are matched. Discussing the marker in unbracketed prose
(`skip-ci`, `ci-skip`, `skip ci`, `ci skip`) is allowed (CLAUDE.md §9.2).

Commit scans use --first-parent so imported upstream history (the second parent of
a merge/upstream-* merge commit) does NOT count — upstream's own
`chore: sync VERSION ... [skip ci]` commits are preserved by contract.

Exit codes
----------
  0 — no bracketed marker in any scanned source
  1 — a bracketed marker was found
  2 — git / environment failure (or nothing to scan)

Usage
-----
  python3 scripts/checks/skip-ci-marker.py \
      [--title TEXT] [--body TEXT] [--commits-range BASE..HEAD] [--quiet]
"""
from __future__ import annotations

import argparse
import subprocess
import sys

MARKERS = ("[skip ci]", "[ci skip]")


def first_parent_commit_bodies(commit_range: str) -> str:
    return subprocess.check_output(
        ["git", "log", "--first-parent", "--format=%B", commit_range],
        text=True,
    )


def scan(label: str, text: str, failures: list[str]) -> None:
    hit = next((m for m in MARKERS if m in text), None)
    if hit:
        failures.append(f"{label}: contains bracketed '{hit}'")


def main() -> int:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("--title", default=None, help="PR title to scan")
    ap.add_argument("--body", default=None, help="PR body to scan")
    ap.add_argument(
        "--commits-range",
        default=None,
        help="BASE..HEAD; scans first-parent commit messages in the range",
    )
    ap.add_argument(
        "--quiet", action="store_true", help="suppress success output (preflight wrapper)"
    )
    args = ap.parse_args()

    if args.title is None and args.body is None and args.commits_range is None:
        print(
            "FAIL: nothing to scan; pass at least one of --title / --body / --commits-range",
            file=sys.stderr,
        )
        return 2

    failures: list[str] = []
    if args.title is not None:
        scan("PR title", args.title, failures)
    if args.body is not None:
        scan("PR body", args.body, failures)
    if args.commits_range is not None:
        try:
            bodies = first_parent_commit_bodies(args.commits_range)
        except subprocess.CalledProcessError as exc:
            print(f"FAIL: git log {args.commits_range} failed: {exc}", file=sys.stderr)
            return 2
        scan(f"first-parent commits ({args.commits_range})", bodies, failures)

    if failures:
        print(
            "[check_skip_ci_marker] FAIL: bracketed skip-ci / ci-skip marker found:",
            file=sys.stderr,
        )
        for f in failures:
            print(f"  {f}", file=sys.stderr)
        print("", file=sys.stderr)
        print(
            "Per CLAUDE.md §9.2 a title/body/commit that may land on main and later be",
            file=sys.stderr,
        )
        print(
            "tagged must NOT carry the bracketed form. When DISCUSSING the marker use an",
            file=sys.stderr,
        )
        print(
            "unbracketed form: skip-ci (hyphen), ci-skip (hyphen), or skip ci / ci skip.",
            file=sys.stderr,
        )
        return 1

    if not args.quiet:
        scanned = []
        if args.title is not None:
            scanned.append("title")
        if args.body is not None:
            scanned.append("body")
        if args.commits_range is not None:
            scanned.append(f"commits({args.commits_range})")
        print(f"[check_skip_ci_marker] clean: {', '.join(scanned)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
