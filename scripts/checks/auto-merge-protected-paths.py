#!/usr/bin/env python3
"""auto-merge-protected-paths — publish-on-push paths must be merge-protected.

A workflow that publishes when a push to main touches certain paths (an
image to ghcr.io, pricing to prod) makes those paths unsafe to merge without
a person. dev-rules' `auto-merge-low-risk` refuses a PR that touches a path
listed in `.preflight/high-risk-anchor.conf` under `[high_risk_paths]` or
`[auto_merge_protected_paths]`. This gate keeps that list in step with the
workflows: every `on.push.paths` entry of a workflow in PUBLISHING_WORKFLOWS
must be matched by one of those patterns.

Glob entries are checked by turning each into a sample path (`**` → `x/y`,
`*` → `x`) and testing it against the regexes. `.github/**` entries are
skipped: the generic list in `auto-merge-low-risk` already protects
`.github/**`.

A soft "keep these in step" comment, hardened into a gate (global CLAUDE.md
§5). Deliberately line-based (no YAML dependency) so it runs anywhere
python3 exists.

Usage:
  python3 scripts/checks/auto-merge-protected-paths.py [--root <repo_root>] [--quiet]

Exit codes:
  0 — every publish-on-push path is protected
  1 — at least one is not
  2 — parse failure (missing file, no push paths found, bad regex)
"""

from __future__ import annotations

import argparse
import pathlib
import re
import sys

# Workflows that publish on a push to main, by file name.
PUBLISHING_WORKFLOWS = (
    "gemini-web-worker.yml",
    "pricing-registry-publish.yml",
)
CONFIG = ".preflight/high-risk-anchor.conf"
SECTIONS = ("high_risk_paths", "auto_merge_protected_paths")


def protected_patterns(conf: str) -> list[re.Pattern[str]]:
    out, section = [], None
    for raw in conf.splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("[") and line.endswith("]"):
            section = line[1:-1]
            continue
        if section in SECTIONS:
            out.append(re.compile(line, re.IGNORECASE))
    return out


def push_paths(workflow: str) -> list[str]:
    """`on.push.paths` entries, read by indentation."""
    lines = workflow.splitlines()
    paths: list[str] = []
    in_on = in_push = in_paths = False
    push_indent = paths_indent = -1
    for raw in lines:
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        indent = len(raw) - len(raw.lstrip())
        text = raw.strip()
        if indent == 0:
            in_on = text in ("on:", "'on':", '"on":')
            in_push = in_paths = False
            continue
        if not in_on:
            continue
        if in_paths and indent > paths_indent and text.startswith("- "):
            paths.append(text[2:].strip().strip("'\""))
            continue
        if in_paths and indent <= paths_indent:
            in_paths = False
        if in_push and indent <= push_indent:
            in_push = False
        if text == "push:":
            in_push, push_indent = True, indent
        elif in_push and text == "paths:":
            in_paths, paths_indent = True, indent
    return paths


def sample(glob: str) -> str:
    return glob.replace("**", "x/y").replace("*", "x")


def unprotected(paths: list[str], patterns: list[re.Pattern[str]]) -> list[str]:
    return [
        p for p in paths
        if not p.startswith(".github/") and not any(r.search(sample(p)) for r in patterns)
    ]


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default=".")
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args(argv)
    root = pathlib.Path(args.root)
    try:
        patterns = protected_patterns((root / CONFIG).read_text(encoding="utf-8"))
    except (OSError, re.error) as e:
        print(f"[auto-merge-protected-paths] cannot read {CONFIG}: {e}", file=sys.stderr)
        return 2
    missing = []
    for name in PUBLISHING_WORKFLOWS:
        try:
            wf = (root / ".github/workflows" / name).read_text(encoding="utf-8")
        except OSError as e:
            print(f"[auto-merge-protected-paths] cannot read {name}: {e}", file=sys.stderr)
            return 2
        paths = push_paths(wf)
        if not paths:
            print(f"[auto-merge-protected-paths] {name}: no on.push.paths found", file=sys.stderr)
            return 2
        missing += [f"{name}: {p}" for p in unprotected(paths, patterns)]
    if missing:
        print("  FAIL: publish-on-push paths not in " + CONFIG + " [auto_merge_protected_paths]:")
        for m in missing:
            print(f"    - {m}")
        return 1
    if not args.quiet:
        print("[auto-merge-protected-paths] every publish-on-push path is protected")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
