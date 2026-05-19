#!/usr/bin/env python3
"""script-ref-existence.py — verify literal scripts/|ops/|tools/ refs resolve.

Catches the "moved a script, forgot to update a reference" failure mode that
broke Dockerfile + frontend/package.json + .goreleaser*.yaml in PR #307. The
sed pass during that refactor walked .github/ .cursor/ deploy/ docs/ CLAUDE.md
scripts/ ops/ tools/ backend/ — but missed the repo-root Dockerfile, the
frontend/ subtree, and .goreleaser*.yaml entirely. Each miss was a CI failure
waiting to happen.

This check runs at preflight time and fails fast on any literal path of the
form (scripts|ops|tools)/<path>.<ext> that does not resolve to an existing
file in the repo.

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

# Match (scripts|ops|tools)/<path>.<ext>, preceded by a non-identifier
# character so 'foo/scripts/bar.py' inside a longer token doesn't match.
# Note: the path body intentionally excludes shell-variable / regex chars so
# patterns like 'scripts/${name}.sh' or 'scripts/.*\\.json$' are skipped
# by the should_check filter below rather than mis-extracted.
# Extension alternatives must be longest-first so 'js' does not short-match
# 'json'. Trailing lookahead pins the boundary so partial matches don't slip.
PATH_RE = re.compile(
    r"(?<![A-Za-z0-9_./-])"
    r"((?:scripts|ops|tools)/[A-Za-z0-9_\-./]+"
    r"\.(?:json|yaml|mdc|mod|sum|vue|sh|py|js|yml|go|ts))"
    r"(?![A-Za-z0-9])"
)

GLOB_OR_REGEX = set("*?[]{}$\\")

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

# Path prefixes whose tracked files we never scan. dev-rules is a submodule
# with its own scripts/ namespace; generated Ent code is huge and only
# contains stale refs if our schema hand-source has them (scanned separately
# via backend/ent/schema/); built dist is opaque to humans.
EXCLUDE_PREFIXES = (
    "dev-rules/",
    "backend/ent/",          # generated; backend/ent/schema/ is scanned separately if needed
    "backend/internal/web/dist/",
    "node_modules/",
    ".cache/",               # historical fact records (upstream issue cache); paths
                             # frozen at scan-time, not live source references
)


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


def is_container_internal(line: str, start: int) -> bool:
    """Reject /app/scripts/... or /usr/local/scripts/... — container paths."""
    return start > 0 and line[start - 1] == "/"


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
                raw = m.group(1)
                if not is_literal_path(raw):
                    continue
                if is_container_internal(line, m.start(1)):
                    continue
                if (REPO_ROOT / raw).exists():
                    continue
                bad.append((relpath, lineno, raw, line.strip()))

    if not bad:
        print("ok: all literal scripts/|ops/|tools/ refs resolve")
        return 0

    for relpath, lineno, raw, ctx in bad:
        print(
            f"::error file={relpath},line={lineno}::stale ref '{raw}' (file not found)\n"
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
