#!/usr/bin/env python3
"""node-version-align — CI Node major must match the release image's Node major.

The release Docker image builds the frontend inside `FROM ${NODE_IMAGE}`
(Dockerfile `ARG NODE_IMAGE=node:24-alpine`), but CI's frontend jobs pin their
own `node-version:` in `actions/setup-node`. When the two drift, CI validates
the frontend build/tests on a DIFFERENT Node major than the one that actually
produces the shipped artifact — a green CI stops implying the release image
builds (exactly the drift this gate mechanizes away: CI was on Node 20 while
the Dockerfile shipped Node 24).

Scope: only the two workflows whose setup-node steps build/test the frontend
product — `.github/workflows/backend-ci.yml` (frontend, frontend-security,
compile-smoke jobs) and `.github/workflows/release.yml` (build-frontend job).
Agent/watchdog workflows run CLI runtimes, not the shipped frontend, and are
deliberately NOT scanned. If a future job in a scanned workflow legitimately
needs a different Node (non-frontend runtime), append the marker
`# node-version-align: ignore` on its `node-version:` (or
`node-version-file:`) line.

Same doctrine as scripts/checks/release-cache-key-parity.py: a soft "keep
these files in sync" rule, hardened into a mechanical gate (global CLAUDE.md
§5). Deliberately line-based (no YAML dependency) so it runs anywhere
python3 exists.

Usage:
  python3 scripts/checks/node-version-align.py [--root <repo_root>] [--quiet]

Exit codes:
  0 — every scanned node-version major matches Dockerfile NODE_IMAGE major
  1 — at least one node-version drifts from the Dockerfile
  2 — parse failure (missing files / no NODE_IMAGE ARG / no node-version
      entries found / a node-version value this gate cannot resolve to a
      leading integer major, e.g. `lts/*`, `${{ ... }}` expressions, or
      `node-version-file:`) — fails loud instead of passing vacuously.
      Unresolvable forms float over time or hide the pin elsewhere, which is
      exactly the drift class this gate exists to kill; either pin a numeric
      major or mark the line `# node-version-align: ignore` deliberately.
"""

from __future__ import annotations

import argparse
import pathlib
import re
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]

DOCKERFILE_REL = "Dockerfile"
WORKFLOWS_REL = (
    ".github/workflows/backend-ci.yml",
    ".github/workflows/release.yml",
)

# ARG NODE_IMAGE=node:24-alpine  /  node:24.1-alpine  /  node:24
NODE_IMAGE_RE = re.compile(r"^\s*ARG\s+NODE_IMAGE\s*=\s*node:(\d+)")
# Anchor: EVERY setup-node version key, including forms this gate cannot
# resolve (node-version-file:, lts/*, ${{ ... }}). Anchoring on the key —
# not on "key with a numeric value" — is what keeps unresolvable values from
# being silently skipped.
NODE_KEY_RE = re.compile(r"^\s*(node-version(?:-file)?)\s*:\s*(.*?)\s*$")
# Resolvable node-version value: '20'  /  "20"  /  20  /  20.x  /  20.11.0
# (leading integer major, optional quotes, optional trailing comment).
NODE_VALUE_RE = re.compile(r"""^['"]?(\d+)[^\s'"#]*['"]?\s*(?:#.*)?$""")
IGNORE_MARKER = "node-version-align: ignore"


def parse_dockerfile(root: pathlib.Path) -> tuple[int, str]:
    """Return (major, 'Dockerfile:<line>') for the NODE_IMAGE ARG."""
    path = root / DOCKERFILE_REL
    if not path.is_file():
        print(f"  err: missing {DOCKERFILE_REL} under root {root}", file=sys.stderr)
        sys.exit(2)
    for lineno, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        m = NODE_IMAGE_RE.match(line)
        if m:
            return int(m.group(1)), f"{DOCKERFILE_REL}:{lineno}"
    print(
        f"  err: no `ARG NODE_IMAGE=node:<major>...` line found in {DOCKERFILE_REL};"
        " parser anchor drifted — update scripts/checks/node-version-align.py",
        file=sys.stderr,
    )
    sys.exit(2)


def parse_workflow(
    root: pathlib.Path, rel: str
) -> tuple[list[tuple[str, int]], list[str]]:
    """Scan rel for setup-node version keys.

    Returns (entries, unresolvable):
      entries      — [(<file:line>, major), ...] for numeric node-version values
      unresolvable — ["<file:line> <key>: <value>", ...] for anchor-matched
                     lines whose value has no leading integer major and no
                     ignore marker (lts/*, ${{ ... }}, node-version-file:, …)
    """
    path = root / rel
    if not path.is_file():
        print(f"  err: missing workflow {rel} under root {root}", file=sys.stderr)
        sys.exit(2)
    entries: list[tuple[str, int]] = []
    unresolvable: list[str] = []
    matched_any = False  # includes ignored lines: proves the parser anchor works
    for lineno, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if line.lstrip().startswith("#"):
            continue
        m = NODE_KEY_RE.match(line)
        if not m:
            continue
        matched_any = True
        if IGNORE_MARKER in line:
            continue
        key, value = m.group(1), m.group(2)
        vm = NODE_VALUE_RE.match(value) if key == "node-version" else None
        if not vm:
            unresolvable.append(f"{rel}:{lineno} `{key}: {value or '<empty>'}`")
            continue
        entries.append((f"{rel}:{lineno}", int(vm.group(1))))
    if not matched_any:
        print(
            f"  err: no node-version entries found in {rel}; either the frontend"
            " jobs moved (update WORKFLOWS_REL) or the parser anchor drifted",
            file=sys.stderr,
        )
        sys.exit(2)
    return entries, unresolvable


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Check CI setup-node majors match Dockerfile NODE_IMAGE major."
    )
    parser.add_argument(
        "--root",
        type=pathlib.Path,
        default=REPO_ROOT,
        help=f"repo root to scan (default: {REPO_ROOT})",
    )
    parser.add_argument(
        "--quiet",
        action="store_true",
        help="suppress the success line (failures always print) — preflight style",
    )
    args = parser.parse_args()
    root = args.root.resolve()

    docker_major, docker_loc = parse_dockerfile(root)

    failures: list[str] = []
    unresolvable: list[str] = []
    checked = 0
    for rel in WORKFLOWS_REL:
        entries, bad = parse_workflow(root, rel)
        unresolvable.extend(bad)
        for loc, major in entries:
            checked += 1
            if major != docker_major:
                failures.append(
                    f"  FAIL: {loc} node-version major {major}"
                    f" != {docker_loc} NODE_IMAGE major {docker_major}"
                )

    if unresolvable:
        print(
            "node-version-align: found node-version entries this gate cannot"
            " resolve to an integer major — refusing to pass vacuously:",
            file=sys.stderr,
        )
        for u in unresolvable:
            print(f"  FAIL: {u}", file=sys.stderr)
        print(
            f"  fix: pin a numeric major (e.g. node-version: '{docker_major}')"
            " so alignment with Dockerfile NODE_IMAGE stays checkable, or —"
            " only for a non-frontend job — append `# node-version-align:"
            " ignore` on that line",
            file=sys.stderr,
        )
        return 2

    if failures:
        print(
            "node-version-align: CI frontend jobs must use the same Node major"
            " as the release image (Dockerfile NODE_IMAGE):",
            file=sys.stderr,
        )
        for f in failures:
            print(f, file=sys.stderr)
        print(
            f"  fix: bump the workflow node-version lines to '{docker_major}'"
            f" (or bump {docker_loc} — then update BOTH sides together)",
            file=sys.stderr,
        )
        return 1

    if not args.quiet:
        print(
            f"node-version-align: OK — {checked} setup-node entr"
            f"{'y' if checked == 1 else 'ies'} match {docker_loc}"
            f" NODE_IMAGE major {docker_major}"
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
