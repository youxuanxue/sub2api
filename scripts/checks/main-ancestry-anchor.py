#!/usr/bin/env python3
"""main-ancestry-anchor — fail if main's history was orphan-reset.

Mechanizes CLAUDE.md §5.y ("`main` is immutable history once pushed").
The previous enforcement stack (upstream-merge-pr-shape, upstream-override-
marker, sentinel registries) all worked on diff content. None of them noticed
when PR #307 squash-merged an orphan branch into `main`, severing the chain
to release tag v1.7.37 (commit 5a3c120d became unreachable from HEAD).

This check pins a SHA in the repo-root file `.main-ancestry-anchor`. Every
preflight run verifies the anchor SHA is still reachable from HEAD via
`git merge-base --is-ancestor`. If it isn't, main has been rewritten and
preflight fails immediately — locally, in pre-commit hooks, and in CI.

The companion gate `.github/workflows/main-ancestry-guard.yml` catches the
same failure mode earlier (PR-merge time) by verifying PR.base is an
ancestor of PR.head. Together they cover both prevention and detection.

Anchor advancement: see CLAUDE.md §5.y.1. The anchor is a one-way ratchet;
moving it requires a dedicated PR whose commit message contains the literal
marker `main-ancestry-anchor-advance`.

Exit codes:
  0 — anchor SHA is reachable from HEAD
  1 — anchor SHA is NOT reachable (main has been orphan-reset)
  2 — anchor file missing/malformed, or git/IO failure
"""
from __future__ import annotations

import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
ANCHOR_FILE = REPO_ROOT / ".main-ancestry-anchor"


def read_anchor() -> str:
    if not ANCHOR_FILE.is_file():
        print(f"  err: anchor file missing: {ANCHOR_FILE.relative_to(REPO_ROOT)}")
        print(f"  err: see CLAUDE.md §5.y.1 — this file pins the main-ancestry baseline")
        sys.exit(2)
    raw = ANCHOR_FILE.read_text(encoding="utf-8").strip()
    if not raw:
        print(f"  err: anchor file is empty: {ANCHOR_FILE.relative_to(REPO_ROOT)}")
        sys.exit(2)
    # Single-line SHA only; reject anything else so a corrupted edit fails loud.
    if "\n" in raw or len(raw) < 7 or any(c not in "0123456789abcdefABCDEF" for c in raw):
        print(f"  err: anchor file must contain exactly one git SHA on one line")
        print(f"  err: got: {raw!r}")
        sys.exit(2)
    return raw


def main() -> int:
    anchor = read_anchor()
    short = anchor[:12]

    # `git cat-file -e <sha>` exits non-zero if the object is not in the local
    # store. We check this first to give a clearer message than merge-base.
    res = subprocess.run(
        ["git", "cat-file", "-e", anchor],
        cwd=REPO_ROOT,
        capture_output=True,
    )
    if res.returncode != 0:
        print(f"  err: anchor commit {short} not found in local git store")
        print(f"  err: run `git fetch origin` and `git fetch --tags` to populate it")
        print(f"  err: if the anchor SHA itself is wrong, see CLAUDE.md §5.y.1")
        return 2

    res = subprocess.run(
        ["git", "merge-base", "--is-ancestor", anchor, "HEAD"],
        cwd=REPO_ROOT,
        capture_output=True,
    )
    if res.returncode == 0:
        print(f"  ok: main-ancestry anchor {short} reachable from HEAD")
        return 0
    if res.returncode == 1:
        print(f"  err: main-ancestry anchor {short} is NOT reachable from HEAD")
        print(f"  err: main has been orphan-reset since the anchor was set.")
        print(f"  err: This violates CLAUDE.md §5.y (no history rewrites on main).")
        print(f"  err:")
        print(f"  err: If this rewrite is intentional and approved by the team,")
        print(f"  err: advance the anchor in a dedicated PR whose commit message")
        print(f"  err: contains the literal marker `main-ancestry-anchor-advance`")
        print(f"  err: and update .main-ancestry-anchor to the new baseline SHA")
        print(f"  err: in the same commit. See CLAUDE.md §5.y.1.")
        return 1
    # Any other exit code is a git / IO failure.
    print(f"  err: `git merge-base --is-ancestor` failed (exit {res.returncode})")
    if res.stderr:
        print(f"  err: stderr: {res.stderr.decode('utf-8', errors='replace').strip()}")
    return 2


if __name__ == "__main__":
    sys.exit(main())
