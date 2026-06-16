#!/usr/bin/env python3
"""check-upstream-override-marker.py — sub2api/newapi upstream-override guard.

TokenKey is a fork of Wei-Shaw/sub2api (which itself imports QuantumNous/
new-api). Edits to upstream-shaped files can be silently reverted by a
future `git merge upstream/main`. This check forces the author to handle
that risk **on every PR** that touches upstream-shaped paths.

The only thing that *actually* prevents silent clobber is a **sentinel
anchor**: a pinned literal that fails CI if a merge drops it. So the gate is
coverage-first — it verifies real protection rather than trusting a label:

  1. Pure-insertion diff (no deleted lines) — cannot drop an upstream symbol,
     so no revert risk. Auto-pass.
  2. **Verified coverage** — every revert-risk (deletion-bearing) upstream file
     is pinned by a `path` entry in some scripts/sentinels/*.json (pre-existing
     OR added in this PR). Auto-pass, no marker needed. This is the protected,
     honest path: real anchors → silence.
  3. A sentinel registry was edited in this PR (lenient back-compat pass).
  4. Otherwise an explicit marker token in any commit message OR the PR body:
       upstream-touch-guarded  — VERIFIED, NOT trusted: asserts the revert-risk
                                 files are already pinned. If they are NOT, the
                                 claim is false and the gate FAILS (this is the
                                 teeth — the marker can no longer be a free
                                 bypass; covered files already passed at step 2).
       upstream-touch-trivial  — change is pure delete/rename/comment-only with
                                 no revert risk (reviewer-audited human judgment)
       upstream-merge          — this is the upstream-merge PR itself
       no-upstream-touch       — paths were misclassified; author asserts no
                                 upstream surface

`upstream-touch-guarded` is the only marker that makes a *checkable claim about
repo state* (anchors exist), so it is the only one verified. The other three
assert protection is *not needed* — genuine judgment that stays an honest,
reviewer-visible opt-out. Same shape as `no-web-impact`: force the author to
confront upstream-merge drift before merge.

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
import json
import os
import re
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent

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


def files_with_deletions(base: str, files: list[str]) -> set[str]:
    """Return the subset of `files` whose diff against base deletes >=1 line —
    the *revert-risk* set a future `git merge upstream/main` could clobber.
    Pure-insertion files (added lines only) carry no clobber risk and are NOT
    returned, so an empty result means the whole change is pure-insertion.

    Conservative: a file that is binary (`-` numstat), renamed, or missing from
    the numstat output is treated as risky (can't prove it's insertion-only).
    """
    if not files:
        return set()
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
    risky: set[str] = set()
    for f in files:
        if f not in stat:
            risky.add(f)  # renamed / not found — be conservative
            continue
        added, deleted = stat[f]
        if added == "-" or deleted == "-":
            risky.add(f)  # binary — can't tell
            continue
        if int(deleted) != 0:
            risky.add(f)
    return risky


def covered_sentinel_paths() -> set[str]:
    """File paths pinned by a `path` entry in any scripts/sentinels/*.json.

    A path here is independently anchor-verified by the per-registry sentinel
    checkers (gateway-tk / newapi / frontend-tk / ...), so a deletion-bearing
    upstream edit to such a file is protected from silent upstream-merge
    clobber. Mirrors check-registry-update-gate.py's covered_paths_by_registry()
    (same JSON shape: {version, rationale, sentinels: [{path, must_contain}]}).
    Read from disk = read HEAD, so anchors ADDED in this PR count as coverage.
    """
    covered: set[str] = set()
    for registry in sorted(REPO_ROOT.glob("scripts/sentinels/*.json")):
        try:
            data = json.loads(registry.read_text(encoding="utf-8"))
        except (json.JSONDecodeError, OSError):
            continue
        sentinels = data.get("sentinels")
        if not isinstance(sentinels, list):
            continue
        for entry in sentinels:
            if isinstance(entry, dict) and isinstance(entry.get("path"), str):
                covered.add(entry["path"])
    return covered


def decide(
    upstream_files: list[str],
    sentinel_touched: list[str],
    risky_files: list[str],
    covered_paths: set[str],
    marker_text: str,
) -> tuple[bool, str]:
    """Pure verdict function (no git / IO) so the acceptance logic is unit
    testable. `risky_files` = upstream-shaped files with deletions (a merge can
    clobber them); `covered_paths` = files pinned by some sentinel registry.
    Returns (ok, reason). Coverage-first acceptance order:
      1. no upstream-shaped paths touched
      2. pure-insertion (no risky files) — cannot drop an upstream symbol
      3. verified coverage — every risky file is sentinel-pinned (no marker
         needed; this is the protected honest path)
      4. a sentinel registry was edited in this PR (lenient back-compat)
      5. an explicit marker token in commit messages OR the PR body, where
         `upstream-touch-guarded` is VERIFIED against coverage (a false claim
         fails — covered files already passed at step 3, so reaching the marker
         loop means `uncovered` is non-empty and the claim cannot be true)
    """
    if not upstream_files:
        return True, "no upstream-shaped paths changed"
    if not risky_files:
        return True, "pure-insertion diff (no deletions) — no upstream-merge revert risk"
    uncovered = sorted(f for f in risky_files if f not in covered_paths)
    if not uncovered:
        return True, (
            "verified coverage — all revert-risk upstream files are pinned in "
            "scripts/sentinels/*.json: " + ", ".join(sorted(risky_files))
        )
    if sentinel_touched:
        return True, "sentinel registry updated: " + ", ".join(sorted(sentinel_touched))
    for marker in MARKERS:
        if marker in marker_text:
            if marker == "upstream-touch-guarded":
                return False, (
                    "marker 'upstream-touch-guarded' claims existing sentinel coverage, but these "
                    "revert-risk upstream files are pinned by NO scripts/sentinels/*.json entry: "
                    + ", ".join(uncovered)
                    + ". Add an anchor (pin the injection line in the upstream file), or use "
                    "'upstream-touch-trivial' if they truly carry no revert risk."
                )
            return True, f"marker '{marker}' present"
    return False, "upstream-shaped paths changed without sentinel coverage / marker / pure-insertion"


def _print_failure(upstream_files: list[str], stream, uncovered: list[str] | None = None) -> None:
    print("Upstream-shaped files in this PR:", file=stream)
    for f in upstream_files[:30]:
        print(f"  {f}", file=stream)
    if len(upstream_files) > 30:
        print(f"  ... and {len(upstream_files) - 30} more", file=stream)
    print("", file=stream)
    if uncovered:
        print("Revert-risk upstream files pinned by NO sentinel (a future upstream merge "
              "could silently clobber these):", file=stream)
        for f in uncovered[:30]:
            print(f"  {f}", file=stream)
        if len(uncovered) > 30:
            print(f"  ... and {len(uncovered) - 30} more", file=stream)
        print("", file=stream)
    print("Fix (any one):", file=stream)
    print("  (a) add anchor(s) in scripts/sentinels/*.json for the revert-risk file(s) — "
          "pin the injection line; this is REAL overwrite protection (preferred), OR", file=stream)
    print("  (b) make the change pure-insertion (no deleted lines), OR", file=stream)
    print("  (c) add an honest opt-out token to the PR description (mutable — no commit rewrite): "
          + ", ".join(m for m in MARKERS if m != "upstream-touch-guarded") + ".", file=stream)
    print("", file=stream)
    print("Marker semantics:", file=stream)
    print("  upstream-touch-guarded — VERIFIED, not trusted: the revert-risk files must already be "
          "pinned in a sentinel registry; a false claim fails this gate", file=stream)
    print("  upstream-touch-trivial — change has no upstream-merge revert risk (reviewer-audited)", file=stream)
    print("  upstream-merge         — this is the upstream-merge PR itself", file=stream)
    print("  no-upstream-touch      — paths were misclassified; assert no upstream surface", file=stream)


def run_selftest() -> int:
    SVC = "backend/internal/service/x.go"
    HDL = "backend/internal/handler/x.go"
    VIEW = "frontend/src/views/admin/AccountsView.vue"
    cases = [
        # (upstream_files, sentinel_touched, risky_files, covered_paths, marker_text, expect_ok)
        ([], [], [], set(), "", True),                               # nothing upstream
        ([VIEW], [], [], set(), "", True),                           # pure insertion (no risky)
        # verified coverage: risky file IS pinned, NO marker → auto-pass (the honest path)
        ([SVC], [], [SVC], {SVC}, "", True),
        # guarded + risky file IS covered → passes at coverage step before marker is read
        ([SVC], [], [SVC], {SVC}, "feat: ... upstream-touch-guarded ...", True),
        # guarded + risky file NOT covered → FAIL (the new teeth — false claim)
        ([SVC], [], [SVC], set(), "feat: ... upstream-touch-guarded ...", False),
        # sentinel edited this PR but risky still uncovered → lenient back-compat pass
        ([HDL], ["scripts/sentinels/newapi.json"], [HDL], set(), "", True),
        # honest opt-outs on an uncovered risky file → pass
        ([SVC], [], [SVC], set(), "no-upstream-touch in PR body", True),
        ([SVC], [], [SVC], set(), "... upstream-touch-trivial ...", True),
        # uncovered risky file, no marker → fail
        ([SVC], [], [SVC], set(), "", False),
        # near-miss marker text → fail
        ([SVC], [], [SVC], set(), "guarded but not the token", False),
    ]
    failed = 0
    for i, (uf, st, risky, covered, txt, expect) in enumerate(cases):
        ok, _ = decide(uf, st, risky, covered, txt)
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

    risky_files: list[str] = []
    covered_paths: set[str] = set()
    if upstream_files:
        covered_paths = covered_sentinel_paths()
        try:
            risky_files = sorted(files_with_deletions(args.base, upstream_files))
        except subprocess.CalledProcessError as e:
            print(f"FAIL: git diff --numstat failed: {e}", file=sys.stderr)
            return 2

    uncovered = [f for f in risky_files if f not in covered_paths]

    # A marker is only consulted when there ARE uncovered revert-risk files and
    # no registry was edited this PR (every other branch passes/fails without
    # reading the marker), so only fetch commit messages in that case.
    marker_text = args.pr_body or ""
    if uncovered and not sentinel_touched:
        try:
            marker_text += "\n" + commit_messages(args.base)
        except subprocess.CalledProcessError as e:
            print(f"FAIL: git log failed: {e}", file=sys.stderr)
            return 2

    ok, reason = decide(upstream_files, sentinel_touched, risky_files, covered_paths, marker_text)
    if ok:
        if not args.quiet:
            print(f"[check_upstream_override_marker] {reason}")
        return 0

    stream = sys.stdout if advisory else sys.stderr
    prefix = "advisory (not blocking — CI enforces on PR body)" if advisory else "FAIL"
    print(f"[check_upstream_override_marker] {prefix}: {reason}", file=stream)
    _print_failure(upstream_files, stream, uncovered=uncovered)
    return 0 if advisory else 1


if __name__ == "__main__":
    sys.exit(main())
