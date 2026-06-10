#!/usr/bin/env python3
"""check-upstream-override-marker.py — sub2api/newapi upstream-override guard.

TokenKey is a fork of Wei-Shaw/sub2api (which itself imports QuantumNous/
new-api). Edits to upstream-shaped files can be silently reverted by a
future `git merge upstream/main`. This check forces the author to handle
that risk **on every PR** that touches upstream-shaped paths, by either:

  1. Updating one of the sentinel registries under scripts/sentinels/*.json
     (preferred — the anchors are then independently verified by
     gateway-tk-sentinel / brand-sentinel / frontend-tk-sentinel /
     newapi-sentinel / pricing-availability-sentinel / etc.).
  2. Carrying an explicit marker token in any commit message of the PR:
       upstream-touch-guarded  — anchors live in a sentinel registry already
       upstream-touch-trivial  — change is pure delete/rename/comment-only with no revert risk
       upstream-merge          — this is the upstream-merge PR itself
       no-upstream-touch       — paths were misclassified; author asserts no upstream surface

Same shape as `no-web-impact`: force the author to think about a specific
cross-cutting concern (here: upstream merge drift) before merge.

Path classification
-------------------

Upstream-shaped paths are the directories where Wei-Shaw/sub2api and
QuantumNous/new-api own the file shape. TK-only files inside those
directories follow naming conventions that are explicitly excluded:

  - `*_tk_*.go` and `*_tk.go` companions in Go packages
  - `*.tk.ts` / `*.tk.vue` TokenKey-only frontend modules
  - test files (`*_test.go`, `*.spec.ts`)
  - TokenKey-only subpackages: `backend/internal/integration/newapi/`,
    `backend/internal/relay/bridge/`, `backend/internal/pkg/`,
    `frontend/src/components/admin/tk/`, `frontend/src/composables/useTk*.ts`,
    `frontend/src/constants/*.tk.ts`

Exit codes
----------

  0 — no upstream-shaped paths touched, or sentinel updated, or marker present
  1 — upstream-shaped paths touched without sentinel update and without marker
  2 — git / environment failure

Usage
-----

  python3 scripts/checks/upstream-override-marker.py [--base origin/main] [--quiet]
"""
from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys

UPSTREAM_SHAPED_INCLUDE = [
    # Backend Go — every upstream-owned subpackage explicitly. Listing each
    # subdir keeps generated trees (backend/ent/* outside schema/, vendor,
    # observability TK spine, etc.) and the worktree caches out of scope.
    re.compile(r"^backend/internal/handler/.*\.go$"),
    re.compile(r"^backend/internal/service/.*\.go$"),
    re.compile(r"^backend/internal/repository/.*\.go$"),
    re.compile(r"^backend/internal/middleware/.*\.go$"),
    re.compile(r"^backend/internal/relay/.*\.go$"),
    re.compile(r"^backend/internal/server/.*\.go$"),
    re.compile(r"^backend/internal/model/.*\.go$"),
    re.compile(r"^backend/internal/domain/.*\.go$"),
    re.compile(r"^backend/internal/config/.*\.go$"),
    re.compile(r"^backend/internal/setup/.*\.go$"),
    re.compile(r"^backend/internal/util/.*\.go$"),
    re.compile(r"^backend/internal/web/.*\.go$"),
    re.compile(r"^backend/cmd/.*\.go$"),
    re.compile(r"^backend/ent/schema/.*\.go$"),
    re.compile(r"^backend/migrations/(?!.*tk_).*\.sql$"),
    # Frontend — single broad rule covers every src/ subdir (views,
    # components, api, stores, router, composables, utils, i18n, types,
    # styles, plus top-level main.ts / App.vue / env.d.ts).  Upstream-owned
    # source files anywhere under src/ are caught; TK-only conventions are
    # handled by the EXCLUDE list.
    re.compile(r"^frontend/src/.*\.(?:vue|ts|tsx)$"),
]

UPSTREAM_SHAPED_EXCLUDE = [
    re.compile(r".*_tk_.*\.go$"),
    re.compile(r".*_tk\.go$"),
    re.compile(r".*\.tk\.(?:ts|vue)$"),
    re.compile(r".*_tk\.(?:ts|vue)$"),
    re.compile(r".*_test\.go$"),
    re.compile(r".*\.spec\.(?:ts|js)$"),
    re.compile(r"^backend/internal/integration/newapi/"),
    re.compile(r"^backend/internal/relay/bridge/"),
    re.compile(r"^backend/internal/pkg/"),
    re.compile(r"^backend/internal/observability/"),
    re.compile(r"^backend/internal/testutil/"),
    re.compile(r"^frontend/src/components/admin/tk/"),
    re.compile(r"^frontend/src/composables/useTk.*\.ts$"),
    re.compile(r"^frontend/src/constants/.*\.tk\.ts$"),
    re.compile(r"^frontend/src/api/admin/tk/"),
    re.compile(r"^frontend/src/__tests__/"),
    # i18n locale dictionaries are append-heavy by nature: TK adds keys on
    # nearly every feature PR, so requiring a marker on every touch is pure
    # ceremony. Upstream-merge conflicts there are surfaced by the 3-way
    # merge itself, not by this acknowledgement gate. Excluded outright.
    re.compile(r"^frontend/src/i18n/locales/.*\.ts$"),
]

MARKERS = [
    "upstream-touch-guarded",
    "upstream-touch-trivial",
    "upstream-merge",
    "no-upstream-touch",
]

SENTINEL_REGISTRY_RE = re.compile(r"^scripts/sentinels/.*\.json$")


def changed_paths(base: str) -> list[str]:
    out = subprocess.check_output(
        ["git", "diff", "--name-only", f"{base}...HEAD"],
        text=True,
    )
    return [line.strip() for line in out.splitlines() if line.strip()]


def commit_messages(base: str) -> str:
    out = subprocess.check_output(
        ["git", "log", f"{base}..HEAD", "--pretty=%B"],
        text=True,
    )
    return out


def is_upstream_shaped(path: str) -> bool:
    for rx in UPSTREAM_SHAPED_EXCLUDE:
        if rx.search(path):
            return False
    for rx in UPSTREAM_SHAPED_INCLUDE:
        if rx.match(path):
            return True
    return False


def is_pure_insertion(base: str, files: list[str]) -> bool:
    """True when EVERY given file's diff against base adds lines but deletes
    none (`git diff --numstat` deleted-count == 0). A pure-insertion diff
    cannot delete or rewrite an upstream symbol, so it carries no
    upstream-merge revert risk and is treated as an implicit
    `upstream-touch-trivial`.

    Conservative: any file that is binary (`-` numstat), renamed (path not
    matched verbatim), or missing from the numstat output fails the test and
    falls through to the marker requirement.
    """
    if not files:
        return False
    out = subprocess.check_output(
        ["git", "diff", "--numstat", f"{base}...HEAD", "--", *files],
        text=True,
    )
    stat: dict[str, tuple[str, str]] = {}
    for line in out.splitlines():
        parts = line.split("\t")
        if len(parts) != 3:
            continue
        added, deleted, path = parts
        stat[path] = (added, deleted)
    for f in files:
        if f not in stat:
            return False  # renamed / not found — be conservative
        added, deleted = stat[f]
        if added == "-" or deleted == "-":
            return False  # binary — can't tell
        if int(deleted) != 0:
            return False
    return True


def decide(
    upstream_files: list[str],
    sentinel_touched: list[str],
    pure_insertion: bool,
    marker_text: str,
) -> tuple[bool, str]:
    """Pure verdict function (no git / IO) so the acceptance logic is unit
    testable. Returns (ok, reason). Acceptance order:
      1. no upstream-shaped paths touched
      2. a sentinel registry was updated in the same PR
      3. the diff is pure-insertion (implicit upstream-touch-trivial)
      4. an explicit marker token appears in commit messages OR the PR body
    """
    if not upstream_files:
        return True, "no upstream-shaped paths changed"
    if sentinel_touched:
        return True, "sentinel registry updated: " + ", ".join(sorted(sentinel_touched))
    if pure_insertion:
        return True, "pure-insertion diff (no deletions) — implicit upstream-touch-trivial"
    for marker in MARKERS:
        if marker in marker_text:
            return True, f"marker '{marker}' present"
    return False, "upstream-shaped paths changed without sentinel / marker / pure-insertion"


def _print_failure(upstream_files: list[str], stream) -> None:
    print("Upstream-shaped files in this PR:", file=stream)
    for f in upstream_files[:30]:
        print(f"  {f}", file=stream)
    if len(upstream_files) > 30:
        print(f"  ... and {len(upstream_files) - 30} more", file=stream)
    print("", file=stream)
    print("Fix (any one):", file=stream)
    print("  (a) make the change pure-insertion (no deleted lines), OR", file=stream)
    print("  (b) add one of these tokens to the PR description (mutable — no commit rewrite): "
          + ", ".join(MARKERS) + ", OR", file=stream)
    print("  (c) update one of scripts/sentinels/*.json with anchor(s) for the change.", file=stream)
    print("", file=stream)
    print("Marker semantics:", file=stream)
    print("  upstream-touch-guarded — anchors are pinned in a sentinel registry", file=stream)
    print("  upstream-touch-trivial — change has no upstream-merge revert risk", file=stream)
    print("  upstream-merge         — this is the upstream-merge PR itself", file=stream)
    print("  no-upstream-touch      — paths were misclassified; assert no upstream surface", file=stream)


def run_selftest() -> int:
    cases = [
        # (upstream_files, sentinel_touched, pure_insertion, marker_text, expect_ok)
        ([], [], False, "", True),                                   # nothing upstream
        (["backend/internal/handler/x.go"], ["scripts/sentinels/newapi.json"], False, "", True),  # sentinel
        (["frontend/src/views/admin/AccountsView.vue"], [], True, "", True),  # pure insertion
        (["backend/internal/service/x.go"], [], False, "feat: ... upstream-touch-guarded ...", True),  # commit marker
        (["backend/internal/service/x.go"], [], False, "no-upstream-touch in PR body", True),  # pr-body marker (same text channel)
        (["backend/internal/service/x.go"], [], False, "", False),   # nothing → fail
        (["backend/internal/service/x.go"], [], False, "guarded but not the token", False),  # near-miss
    ]
    failed = 0
    for i, (uf, st, pure, txt, expect) in enumerate(cases):
        ok, _ = decide(uf, st, pure, txt)
        status = "PASS" if ok == expect else "FAIL"
        if ok != expect:
            failed += 1
        print(f"  {status} case {i}: expect_ok={expect} got_ok={ok}")
    # path classification: i18n excluded, view included, _tk_ excluded
    cls = [
        ("frontend/src/i18n/locales/en.ts", False),
        ("frontend/src/views/admin/AccountsView.vue", True),
        ("frontend/src/views/admin/EdgeAccountsView.vue", True),
        ("backend/internal/service/foo_tk_bar.go", False),
        ("backend/internal/service/foo.go", True),
    ]
    for path, expect in cls:
        got = is_upstream_shaped(path)
        status = "PASS" if got == expect else "FAIL"
        if got != expect:
            failed += 1
        print(f"  {status} classify {path}: expect={expect} got={got}")
    if failed:
        print(f"upstream-override-marker selftest: {failed} case(s) FAILED", file=sys.stderr)
        return 1
    print("ok: upstream-override-marker selftest passed")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--base", default=os.environ.get("PREFLIGHT_BASE", "origin/main"))
    ap.add_argument("--quiet", action="store_true",
                    help="suppress success output (used by preflight wrapper)")
    ap.add_argument("--pr-body", default="",
                    help="PR description text; marker tokens here satisfy the gate "
                         "(mutable surface — CI passes ${{ github.event.pull_request.body }})")
    ap.add_argument("--selftest", action="store_true",
                    help="run the pure-logic self-test and exit")
    args = ap.parse_args()

    if args.selftest:
        return run_selftest()

    # Advisory mode: local pre-commit/pre-push cannot see the in-flight commit
    # message or the PR body, so a hard block there is a structural false
    # deadlock. Preflight sets MARKER_GATE_ADVISORY=1 → we compute and print
    # guidance but never block. The hard gate runs in CI against the PR body.
    advisory = bool(os.environ.get("MARKER_GATE_ADVISORY"))

    try:
        paths = changed_paths(args.base)
    except subprocess.CalledProcessError as e:
        print(f"FAIL: git diff failed: {e}", file=sys.stderr)
        return 2

    upstream_files = [p for p in paths if is_upstream_shaped(p)]
    sentinel_touched = [p for p in paths if SENTINEL_REGISTRY_RE.match(p)]

    pure_insertion = False
    if upstream_files and not sentinel_touched:
        try:
            pure_insertion = is_pure_insertion(args.base, upstream_files)
        except subprocess.CalledProcessError as e:
            print(f"FAIL: git diff --numstat failed: {e}", file=sys.stderr)
            return 2

    marker_text = args.pr_body or ""
    if upstream_files and not sentinel_touched and not pure_insertion:
        try:
            marker_text += "\n" + commit_messages(args.base)
        except subprocess.CalledProcessError as e:
            print(f"FAIL: git log failed: {e}", file=sys.stderr)
            return 2

    ok, reason = decide(upstream_files, sentinel_touched, pure_insertion, marker_text)
    if ok:
        if not args.quiet:
            print(f"[check_upstream_override_marker] {reason}")
        return 0

    stream = sys.stdout if advisory else sys.stderr
    prefix = "advisory (not blocking — CI enforces on PR body)" if advisory else "FAIL"
    print(f"[check_upstream_override_marker] {prefix}: upstream-shaped paths "
          "changed without sentinel / marker / pure-insertion.", file=stream)
    _print_failure(upstream_files, stream)
    return 0 if advisory else 1


if __name__ == "__main__":
    sys.exit(main())
