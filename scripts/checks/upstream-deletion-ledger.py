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

    git diff --cached --diff-filter=D --name-only $(git merge-base <upstream-ref> HEAD) \
        -- backend/ frontend/

Two decisions, both load-bearing:

**Merge-base, not the ref itself.** Comparing against merge-base(ref, HEAD)
means upstream files *added after* that base (pending, not-yet-merged upstream
content) do NOT false-positive as TK deletions — unlike the two-dot tree diff
quoted in CLAUDE.md §5.x, which flags them (see the redeem_service_redeem_test.go
entry in the ledger).

**The index, not HEAD.** The gate runs as a pre-commit hook, and the index is
what the commit will contain. Diffing HEAD instead made the gate report every
deletion exactly one commit late: the commit that removed a file passed (its
deletion was only staged), and the NEXT unrelated commit failed. That is how
the deletion of ops_error_logger_attribution_test.go — which held upstream's
only test for the retained keyPrefix desensitizer — shipped unledgered in
5729fda5f and only surfaced during review of the following commit. The late
report was also unfixable: once the deleting commit was pushed, §5.x's own
remedy ("restore the file … instead of deleting") could not be committed,
because the gate kept reporting the HEAD deletion while §5.y bars amending
pushed history and `--no-verify` is forbidden. Diffing the index fixes both:
a deletion is reported when it is staged, and a staged restoration clears it.
Where the index matches HEAD (CI, manual runs, post-commit) both diffs agree.

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


def merge_base(root: Path, ref: str) -> str:
    proc = git(root, "merge-base", ref, "HEAD")
    if proc.returncode != 0:
        msg = proc.stderr.strip() or f"git merge-base {ref} HEAD failed"
        raise RuntimeError(msg)
    base = proc.stdout.strip()
    if not base:
        raise RuntimeError(f"no merge base between {ref} and HEAD")
    return base


def deleted_upstream_paths(root: Path, ref: str) -> list[str]:
    """Upstream files the INDEX deletes relative to merge-base(ref, HEAD).

    The index, not HEAD, is what a commit will contain, so it is the only
    comparison that reports a deletion at the moment it is made (see module
    docstring).
    """
    proc = git(
        root,
        "diff", "--cached", "--diff-filter=D", "--name-only",
        merge_base(root, ref), "--", *DIFF_SCOPE,
    )
    if proc.returncode != 0:
        msg = proc.stderr.strip() or f"git diff --cached {ref} failed"
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
