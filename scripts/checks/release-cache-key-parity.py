#!/usr/bin/env python3
"""Guard the cross-arch Go build-cache contract between two workflows.

The release speed-up landed in PR #576 splits one cache across two workflow
files that MUST agree, or the optimization silently reverts (release goes back
to cold-compiling arm64 ~4m every time, with NO error — exactly the failure
mode that went unnoticed for weeks before #576):

  * .github/workflows/backend-ci.yml  — `warm-release-cache` job SAVES the cache
    on `main` (the default branch) with `actions/cache@v4`.
  * .github/workflows/release.yml     — RESTORES it on the tag ref with
    `actions/cache/restore@v4` (restore-only; saving here would re-create the
    dead tag-scoped caches #576 removed).

The two invariants that, if broken, silently kill the speed-up:

  1. KEY PREFIX PARITY — both files key on the identical prefix
     `<runner.os>-go-release-<hashFiles(backend/go.sum)>`. Rename it in one file
     only and release's restore-keys never match the warm cache again.
  2. DIRECTIONALITY — backend-ci's go-release step uses the SAVING action
     (`actions/cache@v4`); release's uses the RESTORE-ONLY action
     (`actions/cache/restore@v4`). Re-adding a saver to release.yml resurrects
     the per-tag-ref dead caches.

Same doctrine as scripts/checks/merge-gate-parity.py: a soft "keep these two in
sync" rule, hardened into a mechanical gate (global CLAUDE.md §5).
"""

from __future__ import annotations

import argparse
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]

BACKEND_CI = REPO_ROOT / ".github" / "workflows" / "backend-ci.yml"
RELEASE = REPO_ROOT / ".github" / "workflows" / "release.yml"

# The shared cache-key prefix both files must agree on, verbatim (GitHub
# expression syntax included — it is part of the literal key string).
SHARED_KEY_PREFIX = "${{ runner.os }}-go-release-${{ hashFiles('backend/go.sum') }}"

# Marker substring that identifies a go-release cache key line in either file.
KEY_MARKER = "go-release"

SAVE_ACTION = "actions/cache@v4"
RESTORE_ACTION = "actions/cache/restore@v4"


def _fail(quiet: bool, msg: str) -> None:
    if not quiet:
        sys.stderr.write(f"  FAIL: {msg}\n")


def _action_for_first_key(lines: list[str]) -> str | None:
    """Return the `uses:` action backing the FIRST go-release cache step.

    Scans upward from the first NON-COMMENT line mentioning the key marker to the
    nearest `uses:` line — that step's action is the one operating on the cache.
    Comment lines are skipped because the rationale blocks in both workflows
    reference `Linux-go-release-...` in prose, which would otherwise be matched
    before the real `key:` line.
    """
    key_idx = next(
        (i for i, ln in enumerate(lines)
         if KEY_MARKER in ln and not ln.lstrip().startswith("#")),
        None,
    )
    if key_idx is None:
        return None
    for i in range(key_idx, -1, -1):
        stripped = lines[i].strip()
        if stripped.startswith("uses:") or stripped.startswith("- uses:"):
            return stripped.split("uses:", 1)[1].strip()
    return None


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true", help="suppress non-error output")
    args = parser.parse_args()

    errors = 0

    for path in (BACKEND_CI, RELEASE):
        if not path.exists():
            _fail(args.quiet, f"{path.relative_to(REPO_ROOT)} not found")
            errors += 1

    if errors:
        return 1

    ci_text = BACKEND_CI.read_text()
    rel_text = RELEASE.read_text()

    # Invariant 1: shared key prefix present in BOTH files.
    if SHARED_KEY_PREFIX not in ci_text:
        _fail(
            args.quiet,
            f"backend-ci.yml missing the shared cache-key prefix '{SHARED_KEY_PREFIX}'. "
            "The warm-release-cache job must key on it so release.yml can restore the entry.",
        )
        errors += 1
    if SHARED_KEY_PREFIX not in rel_text:
        _fail(
            args.quiet,
            f"release.yml missing the shared cache-key prefix '{SHARED_KEY_PREFIX}'. "
            "Its restore-keys must match the prefix the warm-release-cache job saves under, "
            "or release silently cold-compiles arm64 again.",
        )
        errors += 1

    # Invariant 2: directionality — backend-ci SAVES, release RESTORE-ONLY.
    ci_action = _action_for_first_key(ci_text.splitlines())
    rel_action = _action_for_first_key(rel_text.splitlines())

    if ci_action != SAVE_ACTION:
        _fail(
            args.quiet,
            f"backend-ci.yml go-release cache step must use '{SAVE_ACTION}' (it SAVES the warm "
            f"cache on main); found '{ci_action}'.",
        )
        errors += 1
    if rel_action != RESTORE_ACTION:
        _fail(
            args.quiet,
            f"release.yml go-release cache step must use '{RESTORE_ACTION}' (restore-only); found "
            f"'{rel_action}'. A saving '{SAVE_ACTION}' here resurrects the dead per-tag-ref caches "
            "PR #576 removed — release saves on the tag ref, which no later tag can read.",
        )
        errors += 1

    if errors:
        return 1

    if not args.quiet:
        print("  ok: release/warm cache key prefix + directionality in sync")
    return 0


if __name__ == "__main__":
    sys.exit(main())
