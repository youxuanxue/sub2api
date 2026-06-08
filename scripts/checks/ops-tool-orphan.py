#!/usr/bin/env python3
"""Gate: ops/ tool orphan check.

Every tool under ops/ must be *wired* — referenced from at least one skill,
workflow, preflight check, sibling ops script, deploy asset, or doc. An orphan
tool (referenced nowhere) is dead weight: the next operator/agent never
discovers it and re-hand-writes the same SQL / SSM glue, defeating the dev-rules
determinism baseline ("mechanizable steps must be script-borne AND invoked").
A god-view audit in PR #663 found 7 such orphans and wired them into their
owning skills; this check stops new ones from accreting.

A tool is "referenced" if its basename appears (substring) in any file under the
search roots other than the tool itself. Substring matching is deliberately
lenient — it biases toward false-negatives (counting something as wired), so the
gate never blocks a legitimate PR over an incidental name; it only fires on a
tool nothing mentions at all.

Legit zero-reference entry tools go in EXEMPT with a reason (forced
classification, like ops-sql-coverage's SELF_CHECK_EXEMPT): a stale EXEMPT key
(tool now referenced, or no longer an ops tool) fails the check too, so the
registry cannot rot.

Exit codes: 0 ok · 1 orphan / stale-exempt found · 2 git/IO error.
stdlib-only. Run --selftest to verify the scan logic without touching git.
"""
from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]

# Files under these roots may legitimately reference an ops tool. Backend/
# frontend never call ops glue, so they are intentionally excluded (keeps the
# corpus small and fast).
CORPUS_ROOTS = [".cursor/skills", ".github/workflows", "scripts", "ops", "deploy", "docs"]
CORPUS_ROOT_GLOBS = ["*.md"]  # repo-root docs (CLAUDE.md, README*.md)

# Tools are enumerated from here.
TOOL_GLOBS = ["ops/**/*.sh", "ops/**/*.py"]
_TEST_MARKERS = ("test_", "_test.")

# Legit zero-reference tools (basename -> reason). Forced classification:
# a stale key (now referenced, or no longer an ops tool) fails the check.
EXEMPT: dict[str, str] = {}


def _git(args: list[str]) -> list[str]:
    out = subprocess.run(["git", *args], cwd=REPO_ROOT, capture_output=True, text=True)
    if out.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)} failed: {out.stderr.strip()}")
    return [ln for ln in out.stdout.splitlines() if ln.strip()]


def is_tool(path: str) -> bool:
    b = os.path.basename(path)
    if any(m in b for m in _TEST_MARKERS):
        return False
    if "/tests/" in path or "/test/" in path:
        return False
    return True


def list_tools() -> list[str]:
    return sorted(p for p in _git(["ls-files", *TOOL_GLOBS]) if is_tool(p))


def collect_corpus() -> dict[str, str]:
    files = set(_git(["ls-files", *CORPUS_ROOTS]))
    for g in CORPUS_ROOT_GLOBS:
        files.update(_git(["ls-files", g]))
    corpus: dict[str, str] = {}
    for f in files:
        try:
            corpus[f] = (REPO_ROOT / f).read_text(encoding="utf-8", errors="ignore")
        except OSError:
            corpus[f] = ""
    return corpus


def scan(tools: list[str], corpus: dict[str, str], exempt: dict[str, str]):
    """Pure core: returns (orphans, stale_exempt). corpus maps path -> content."""
    orphans: list[str] = []
    referenced: set[str] = set()
    for t in tools:
        b = os.path.basename(t)
        if any(b in content for path, content in corpus.items() if path != t):
            referenced.add(b)
        elif b not in exempt:
            orphans.append(t)
    tool_basenames = {os.path.basename(t) for t in tools}
    stale = sorted(k for k in exempt if k not in tool_basenames or k in referenced)
    return orphans, stale


def main() -> int:
    try:
        tools = list_tools()
        corpus = collect_corpus()
    except RuntimeError as exc:
        print(f"::error::{exc}", file=sys.stderr)
        return 2
    orphans, stale = scan(tools, corpus, EXEMPT)
    if orphans or stale:
        print("FAIL: ops/ tool orphan check", file=sys.stderr)
        for t in orphans:
            print(
                f"  - ORPHAN: {t} — referenced nowhere. Wire it into the owning "
                f"skill tool-table / workflow / preflight / sibling script, or add "
                f"its basename to EXEMPT with a reason.",
                file=sys.stderr,
            )
        for k in stale:
            print(
                f"  - STALE EXEMPT: '{k}' is now referenced or no longer an ops "
                f"tool; drop it from EXEMPT.",
                file=sys.stderr,
            )
        return 1
    print(f"ok: all {len(tools)} ops/ tool(s) wired (skill/workflow/script/deploy/doc); 0 orphans")
    return 0


def _selftest() -> int:
    tools = ["demo/a/foo.sh", "demo/b/bar.py", "demo/c/baz.sh"]
    corpus = {
        "skills/s.md": "see demo/a/foo.sh for caps",   # foo wired via skill
        "demo/b/bar.py": "self mention bar.py ignored",  # self-ref must NOT count
        "demo/x/caller.sh": "python3 bar.py --go",       # bar wired via sibling
        # baz.sh referenced nowhere -> orphan
    }
    cases = []
    o, s = scan(tools, corpus, {})
    cases.append(("baz is orphan", o == ["demo/c/baz.sh"]))
    cases.append(("no stale by default", s == []))
    o2, _ = scan(tools, corpus, {"baz.sh": "entry tool"})
    cases.append(("exempt clears orphan", o2 == []))
    _, s3 = scan(tools, corpus, {"foo.sh": "x"})
    cases.append(("referenced-yet-exempt is stale", s3 == ["foo.sh"]))
    _, s4 = scan(tools, corpus, {"ghost.sh": "x"})
    cases.append(("nonexistent exempt is stale", s4 == ["ghost.sh"]))
    ok = True
    for name, passed in cases:
        print(f"  {'PASS' if passed else 'FAIL'} {name}")
        ok = ok and passed
    if ok:
        print("ok: ops-tool-orphan self-test (5/5 cases passed)")
        return 0
    print("FAIL: ops-tool-orphan self-test", file=sys.stderr)
    return 1


if __name__ == "__main__":
    if "--selftest" in sys.argv:
        raise SystemExit(_selftest())
    raise SystemExit(main())
