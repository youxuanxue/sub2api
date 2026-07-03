#!/usr/bin/env python3
"""upstream-deletion-ledger — every deleted upstream file must be documented.

Mechanizes CLAUDE.md §5.x ("Deletion discipline — default = keep, override;
never silent-delete"). Deleting an upstream-owned file (handler, middleware,
service, test, migration) is the highest-risk form of divergence: it silently
regresses functionality, guarantees recurring merge conflicts, and drops
upstream's tests. §5.x already demands documentation for every such deletion;
this check makes that demand mechanical by requiring the deleted path to
appear **verbatim** in the ledger `docs/DEPRECATIONS.md`.

How it decides
--------------

    git diff --diff-filter=D --name-only <upstream-ref>...HEAD -- backend/ frontend/

Three-dot (merge-base) semantics on purpose: it compares HEAD against the
last-merged upstream commit, so upstream files *added after* the merge-base
(pending, not-yet-merged upstream content) do NOT false-positive as TK
deletions — unlike the two-dot tree diff quoted in CLAUDE.md §5.x, which
flags them (see the redeem_service_redeem_test.go entry in the ledger).

Every reported path must occur as an exact substring of the ledger file.
Paths are unambiguous (repo-relative, unique), so verbatim substring match is
deterministic and needs no markup convention in the ledger.

Environments without the upstream ref (plain CI clones have no `upstream`
remote) are skipped with exit 0 — the gate only has meaning where upstream
history is present.

Exit codes
----------

  0 — no undocumented deletions, or upstream ref absent (SKIP)
  1 — deleted upstream path(s) missing from the ledger, or ledger file missing
      while deletions exist
  2 — git / environment failure (no merge base, git not on PATH, ...)

Usage
-----

  python3 scripts/checks/upstream-deletion-ledger.py \
      [--upstream-ref upstream/main] [--ledger docs/DEPRECATIONS.md] \
      [--root <repo-root>] [--quiet]
"""
from __future__ import annotations

import argparse
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
DIFF_SCOPE = ["backend/", "frontend/"]


def git(root: Path, *args: str) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["git", "-C", str(root), *args],
        capture_output=True,
        text=True,
    )


def upstream_ref_exists(root: Path, ref: str) -> bool:
    return git(root, "rev-parse", "--verify", "--quiet", f"{ref}^{{commit}}").returncode == 0


def deleted_upstream_paths(root: Path, ref: str) -> list[str]:
    """Upstream files deleted in HEAD relative to merge-base(ref, HEAD)."""
    proc = git(
        root,
        "diff", "--diff-filter=D", "--name-only", f"{ref}...HEAD", "--", *DIFF_SCOPE,
    )
    if proc.returncode != 0:
        msg = proc.stderr.strip() or f"git diff {ref}...HEAD failed"
        raise RuntimeError(msg)
    return [line.strip() for line in proc.stdout.splitlines() if line.strip()]


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--upstream-ref", default="upstream/main",
                    help="upstream ref to diff against (default: upstream/main)")
    ap.add_argument("--ledger", default="docs/DEPRECATIONS.md",
                    help="ledger file, relative to --root (default: docs/DEPRECATIONS.md)")
    ap.add_argument("--root", default=str(REPO_ROOT),
                    help="repo root to run in (default: this script's repo)")
    ap.add_argument("--quiet", action="store_true",
                    help="suppress success output (used by preflight wrapper)")
    args = ap.parse_args()

    root = Path(args.root).resolve()
    if not (root / ".git").exists():
        print(f"FAIL: --root {root} is not a git repository", file=sys.stderr)
        return 2

    if not upstream_ref_exists(root, args.upstream_ref):
        # No upstream remote/ref (plain CI clone) — the diff has no meaning
        # here; the gate runs wherever upstream history is fetched.
        print(f"[upstream-deletion-ledger] SKIP: ref '{args.upstream_ref}' not found "
              "(no upstream remote in this environment)")
        return 0

    try:
        deleted = deleted_upstream_paths(root, args.upstream_ref)
    except RuntimeError as e:
        print(f"FAIL: {e}", file=sys.stderr)
        return 2

    if not deleted:
        if not args.quiet:
            print(f"[upstream-deletion-ledger] ok: no upstream files deleted "
                  f"relative to {args.upstream_ref} (merge-base diff, scope: "
                  + " ".join(DIFF_SCOPE) + ")")
        return 0

    ledger_path = root / args.ledger
    if not ledger_path.is_file():
        print(f"FAIL: {len(deleted)} upstream file(s) deleted but ledger "
              f"'{args.ledger}' does not exist (CLAUDE.md §5.x):", file=sys.stderr)
        for p in deleted:
            print(f"  {p}", file=sys.stderr)
        return 1

    ledger_text = ledger_path.read_text(encoding="utf-8")
    missing = [p for p in deleted if p not in ledger_text]
    if missing:
        print(f"FAIL: upstream file(s) deleted without a ledger entry in "
              f"'{args.ledger}' (CLAUDE.md §5.x — never silent-delete):", file=sys.stderr)
        for p in missing:
            print(f"  {p}", file=sys.stderr)
        print("", file=sys.stderr)
        print("Fix: add a section to the ledger containing the path verbatim, plus "
              "deletion commit + PR link, reason, regression cost, upstream tests "
              "lost, and re-adoption conditions. Or restore the file (§5.x default "
              "= keep: override the default / add a setting / comment out the "
              "registration instead of deleting).", file=sys.stderr)
        return 1

    if not args.quiet:
        print(f"[upstream-deletion-ledger] ok: {len(deleted)} deletion(s) all "
              f"documented in {args.ledger}: " + ", ".join(deleted))
    return 0


if __name__ == "__main__":
    sys.exit(main())
