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


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--base", default=os.environ.get("PREFLIGHT_BASE", "origin/main"))
    ap.add_argument("--quiet", action="store_true",
                    help="suppress success output (used by preflight wrapper)")
    args = ap.parse_args()

    try:
        paths = changed_paths(args.base)
    except subprocess.CalledProcessError as e:
        print(f"FAIL: git diff failed: {e}", file=sys.stderr)
        return 2

    upstream_files = [p for p in paths if is_upstream_shaped(p)]
    if not upstream_files:
        if not args.quiet:
            print("[check_upstream_override_marker] no upstream-shaped paths changed")
        return 0

    sentinel_touched = [p for p in paths if SENTINEL_REGISTRY_RE.match(p)]
    if sentinel_touched:
        if not args.quiet:
            print(
                "[check_upstream_override_marker] sentinel registry updated: "
                + ", ".join(sorted(sentinel_touched))
            )
        return 0

    try:
        msg = commit_messages(args.base)
    except subprocess.CalledProcessError as e:
        print(f"FAIL: git log failed: {e}", file=sys.stderr)
        return 2

    for marker in MARKERS:
        if marker in msg:
            if not args.quiet:
                print(f"[check_upstream_override_marker] marker '{marker}' present in commit message")
            return 0

    # Fail
    print(
        "[check_upstream_override_marker] FAIL: upstream-shaped paths changed "
        "without a sentinel-registry update and without an explicit marker.",
        file=sys.stderr,
    )
    print("Upstream-shaped files in this PR:", file=sys.stderr)
    for f in upstream_files[:30]:
        print(f"  {f}", file=sys.stderr)
    if len(upstream_files) > 30:
        print(f"  ... and {len(upstream_files) - 30} more", file=sys.stderr)
    print("", file=sys.stderr)
    print("Fix: either", file=sys.stderr)
    print("  (a) update one of scripts/sentinels/*.json with anchor(s) for the change, OR", file=sys.stderr)
    print(f"  (b) include one of these tokens in any commit message: {', '.join(MARKERS)}", file=sys.stderr)
    print("", file=sys.stderr)
    print("Marker semantics:", file=sys.stderr)
    print("  upstream-touch-guarded — anchors are pinned in a sentinel registry", file=sys.stderr)
    print("  upstream-touch-trivial — change is pure delete/rename/comment-only, no revert risk", file=sys.stderr)
    print("  upstream-merge         — this is the upstream-merge PR itself", file=sys.stderr)
    print("  no-upstream-touch      — paths were misclassified; assert no upstream surface", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
