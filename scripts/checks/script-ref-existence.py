#!/usr/bin/env python3
"""script-ref-existence — verify literal scripts/|ops/|tools/ refs resolve.

Closes the OPC gap surfaced by PR #307: when scripts/ files moved, the
refactor's sed pass walked only .github/ .cursor/ deploy/ docs/ CLAUDE.md
scripts/ ops/ tools/ backend/ and silently missed the repo-root Dockerfile,
the frontend/ subtree, and .goreleaser*.yaml. Each miss was a CI build
failure waiting to happen.

This check runs at preflight time and fails fast on any literal path of
shape (scripts|ops|tools)/<path>.<ext> that cannot be resolved to an
existing file — trying multiple resolutions per match to handle Docker
build context (sub2api/scripts/...), relative invocation (../scripts/...),
and submodule-nested refs (dev-rules/scripts/...).

Exit codes:
  0 — every literal ref resolves
  1 — at least one stale ref
  2 — scan failure (git ls-files unavailable, etc.)
"""
from __future__ import annotations

import re
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]

# Match (scripts|ops|tools)/<path>.<ext>.
#
# Lookbehind excludes only identifier characters. '.' and '/' MUST stay
# allowed as preceding chars, otherwise we silently miss the exact PR #307
# bug patterns:
#   ../scripts/<F>.sh        (frontend/package.json shape)
#   sub2api/scripts/<F>.sh   (Dockerfile COPY build-context shape)
#   ./scripts/<F>.sh         (explicit relative)
#   dev-rules/scripts/<F>.py (submodule-nested)
# Still rejected: 'myscripts/<F>.sh' (preceded by letter).
#
# Extensions are deliberately narrow to actual ops/script orchestration
# file types. Vue/TS/JS module paths under @aliases or webpack resolution
# (e.g. 'admin/ops/OpsDashboard.vue') are NOT script orchestration and
# would only generate false positives.
#
# Trailing non-word lookahead pins the boundary so partial matches don't
# slip even if the extension list grows.
PATH_RE = re.compile(
    r"(?<![A-Za-z0-9_-])"
    r"((?:scripts|ops|tools)/[A-Za-z0-9_\-./]+"
    r"\.(?:yaml|mdc|json|sh|py|go|yml))"
    r"(?![A-Za-z0-9])"
)

GLOB_OR_REGEX = set("*?[]{}$\\")

# File extensions whose contents we scan.
SCAN_EXTS = {
    ".sh", ".py", ".js", ".ts", ".vue",
    ".yml", ".yaml", ".json", ".md", ".mdc",
    ".go", ".mod", ".sum",
}
SCAN_BASENAMES = {
    "Dockerfile", "Dockerfile.goreleaser", "Makefile",
    ".gitignore", ".goreleaser.yaml", ".goreleaser.simple.yaml",
    "CLAUDE.md",
}

# Path prefixes whose tracked files we never scan. Each excluded path is
# justified by a class of false positives:
#   dev-rules/        submodule with its own scripts/ namespace
#   .cursor/rules/    synced from dev-rules; references other consumers'
#                     scripts that TK doesn't implement (per the rule's
#                     own "Each project must maintain its own ..." escape)
#   backend/ent/      generated; backend/ent/schema/ is scanned via SCAN_EXTS
#   backend/internal/web/dist/   built artifact, opaque to humans
#   node_modules/     vendored
#   .cache/           historical fact records (upstream issue cache); paths
#                     frozen at scan-time, not live source references
EXCLUDE_PREFIXES = (
    "dev-rules/",
    ".cursor/rules/",
    "backend/ent/",
    "backend/internal/web/dist/",
    "node_modules/",
    ".cache/",
)

# Characters that terminate the "path-like token" walk-back used by
# is_container_internal() and resolves().
_TOKEN_BOUNDARIES = frozenset(" \t\"'`():,;|=>")


def tracked_files() -> list[Path]:
    res = subprocess.run(
        ["git", "ls-files"],
        cwd=REPO_ROOT,
        text=True,
        capture_output=True,
    )
    if res.returncode != 0:
        print(f"::error::git ls-files failed: {res.stderr.strip()}", file=sys.stderr)
        sys.exit(2)
    out: list[Path] = []
    for line in res.stdout.splitlines():
        rel = line.strip()
        if not rel:
            continue
        if any(rel.startswith(prefix) for prefix in EXCLUDE_PREFIXES):
            continue
        p = Path(rel)
        full = REPO_ROOT / p
        if not full.is_file():
            continue
        if full.suffix in SCAN_EXTS or full.name in SCAN_BASENAMES:
            out.append(p)
    return out


def is_literal_path(raw: str) -> bool:
    """Reject glob/regex/template-shaped strings."""
    return not any(c in raw for c in GLOB_OR_REGEX)


def full_token(line: str, match_start: int, match_end: int) -> str:
    """Walk back from match_start to the nearest token boundary."""
    i = match_start - 1
    while i >= 0 and line[i] not in _TOKEN_BOUNDARIES:
        i -= 1
    return line[i + 1:match_end]


def is_container_internal(token: str) -> bool:
    """Absolute container paths like /app/scripts/... — not real repo refs."""
    return token.startswith("/")


def resolves(captured: str, token: str, file_path: Path) -> bool:
    """Return True if any plausible resolution of the reference exists.

    Three resolutions tried in order:
      1. The captured suffix as a repo-rooted path. Handles Docker COPY
         build-context prefixes like sub2api/scripts/... where the file
         actually lives at scripts/...
      2. The full token as a repo-rooted path. Handles submodule-nested
         refs like dev-rules/scripts/check_approved_docs.py.
      3. The full token resolved relative to the scanning file's directory.
         Handles ../scripts/... invocations like the one in
         frontend/package.json.
    """
    if (REPO_ROOT / captured).exists():
        return True
    if token != captured and (REPO_ROOT / token).exists():
        return True
    if token.startswith(("./", "../")):
        anchored = (REPO_ROOT / file_path).parent / token
        try:
            if anchored.resolve().exists():
                return True
        except OSError:
            pass
    return False


def main() -> int:
    bad: list[tuple[Path, int, str, str]] = []
    for relpath in tracked_files():
        try:
            text = (REPO_ROOT / relpath).read_text(encoding="utf-8", errors="replace")
        except OSError as exc:
            print(f"::warning::could not read {relpath}: {exc}", file=sys.stderr)
            continue
        for lineno, line in enumerate(text.splitlines(), start=1):
            for m in PATH_RE.finditer(line):
                captured = m.group(1)
                if not is_literal_path(captured):
                    continue
                token = full_token(line, m.start(1), m.end(1))
                if is_container_internal(token):
                    continue
                if resolves(captured, token, relpath):
                    continue
                bad.append((relpath, lineno, token, line.strip()))

    if not bad:
        print("ok: all literal scripts/|ops/|tools/ refs resolve")
        return 0

    for relpath, lineno, token, ctx in bad:
        print(
            f"::error file={relpath},line={lineno}::stale ref '{token}' (file not found)\n"
            f"    context: {ctx}",
            file=sys.stderr,
        )
    print(
        f"\nFAIL: {len(bad)} stale script reference(s) found. "
        "Update the reference or restore the file.",
        file=sys.stderr,
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())
