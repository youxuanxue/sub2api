#!/usr/bin/env python3
"""upstream-insertion-invasiveness.py — advisory recon: HOW invasively does a
change touch upstream-shaped Go files?

The blocking gates answer WHETHER a PR raises upstream-merge risk
(upstream-override-marker.py: deletion-bearing edits; upstream-conflict-
surface.sh: new merge-tree conflict files). This script answers HOW INVASIVE
each touch is, per diff hunk, so the merge-cost shape is visible at PR time
instead of at the next `git merge upstream/main`:

  eof_append       — pure insertion at end of file (cheapest to merge)
  top_level_decl   — new top-level func/const/var/type/import block inserted
                     between existing declarations (usually auto-merges)
  other_insert     — pure insertion inside a non-func declaration block
                     (struct field, const/import entry; usually fine)
  func_body_insert — pure insertion INSIDE an upstream function body — the #1
                     future-merge-conflict driver; prefer a *_tk_*.go
                     companion + a 1-line call (CLAUDE.md §5 minimal-invasion)
  func_body_modify — deletion-bearing hunk inside a function body (revert risk
                     already gated by upstream-override-marker.py; reported
                     here for the full picture)
  other_modify     — deletion-bearing hunk outside any function context

Scope: files MODIFIED in base...head (merge-base to head), ending in `.go`,
not TK companions (`*_tk_*.go` / `*_tk.go`), and present in --upstream-ref.

Heuristics (deterministic, documented limits):
  * classification uses `git diff -U0` hunk headers; the "function context" is
    git's funcname line (text after the second `@@`), which shows the nearest
    PRECEDING top-level declaration. An insertion BETWEEN two funcs therefore
    also carries a `func ...` context — it is separated from true body
    insertions by checking whether the added block's first non-blank line
    starts a new top-level declaration at column 0 (gofmt guarantees function
    bodies are indented, so column-0 `func`/`const`/`var`/`type`/comment
    means a new decl, not body code).
  * a hunk whose old-side range reaches past the last line of the merge-base
    blob is an EOF append.

Exit codes:
  0 — advisory run completed (default; never blocks)
  1 — git / environment failure
  2 — --strict and at least one func_body_insert hunk found

Usage:
  python3 scripts/checks/upstream-insertion-invasiveness.py \
      [--base origin/main] [--head HEAD] [--upstream-ref upstream/main] \
      [--root <repo-dir>] [--summary-file <markdown-file-to-append>] \
      [--strict] [--selftest]
"""
from __future__ import annotations

import argparse
import re
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]

CATEGORIES = [
    "eof_append",
    "top_level_decl",
    "other_insert",
    "func_body_insert",
    "func_body_modify",
    "other_modify",
]

HUNK_RE = re.compile(r"^@@ -(\d+)(?:,(\d+))? \+\d+(?:,\d+)? @@ ?(.*)$")
# Column-0 starters that open a NEW top-level Go declaration (or its doc
# comment / build tag). gofmt indents everything inside a body, so a pure
# insertion whose first non-blank line matches this is a new decl, not code
# injected into an existing body.
TOP_LEVEL_START_RE = re.compile(r"^(func\b|const\b|var\b|type\b|import\b|package\b|//|/\*)")
FUNC_CTX_RE = re.compile(r"^func\b")
TK_COMPANION_RE = re.compile(r"(_tk_[^/]*\.go|_tk\.go)$")


def classify_hunk(old_start: int, old_len: int, ctx: str, added: list[str], old_total: int) -> str:
    """Pure per-hunk classifier (no git / IO) — unit-tested by --selftest.

    old_start/old_len come from the `-U0` hunk header old-side range: for pure
    insertions old_len == 0 and old_start is the old-file line AFTER which the
    insertion lands (0 = before first line). old_total = merge-base blob line
    count. ctx = git funcname context. added = added-line payloads (no '+').
    """
    ctx_is_func = bool(FUNC_CTX_RE.match(ctx.strip()))
    if old_len > 0:
        return "func_body_modify" if ctx_is_func else "other_modify"
    if old_start >= old_total:
        return "eof_append"
    first = next((line for line in added if line.strip()), "")
    if TOP_LEVEL_START_RE.match(first):
        return "top_level_decl"
    return "func_body_insert" if ctx_is_func else "other_insert"


def parse_hunks(diff_text: str) -> list[dict]:
    """Parse `git diff -U0` output into [{old_start, old_len, ctx, added}]."""
    hunks: list[dict] = []
    cur: dict | None = None
    for line in diff_text.splitlines():
        m = HUNK_RE.match(line)
        if m:
            cur = {
                "old_start": int(m.group(1)),
                "old_len": int(m.group(2)) if m.group(2) is not None else 1,
                "ctx": m.group(3).strip(),
                "added": [],
            }
            hunks.append(cur)
        elif cur is not None and line.startswith("+") and not line.startswith("+++"):
            cur["added"].append(line[1:])
    return hunks


def git_out(root: Path, *args: str) -> str:
    return subprocess.check_output(["git", "-C", str(root), *args], text=True)


def resolve_commit(root: Path, ref: str) -> str:
    res = subprocess.run(
        ["git", "-C", str(root), "rev-parse", "--verify", "--quiet", f"{ref}^{{commit}}"],
        capture_output=True,
        text=True,
    )
    if res.returncode != 0:
        print(f"FAIL: cannot resolve '{ref}' to a commit in {root}", file=sys.stderr)
        print("FAIL: (for upstream refs: run 'git fetch upstream main' first)", file=sys.stderr)
        sys.exit(1)
    return res.stdout.strip()


def exists_in_ref(root: Path, sha: str, path: str) -> bool:
    return (
        subprocess.run(
            ["git", "-C", str(root), "cat-file", "-e", f"{sha}:{path}"],
            capture_output=True,
        ).returncode
        == 0
    )


def blob_line_count(root: Path, sha: str, path: str) -> int:
    res = subprocess.run(
        ["git", "-C", str(root), "show", f"{sha}:{path}"],
        capture_output=True,
    )
    if res.returncode != 0:
        print(f"FAIL: git show {sha}:{path} failed", file=sys.stderr)
        sys.exit(1)
    return len(res.stdout.decode("utf-8", errors="replace").splitlines())


def analyze_file(root: Path, merge_base: str, head: str, path: str) -> tuple[dict, list[dict]]:
    """Return ({category: count}, [func-body-insert hunk details])."""
    diff_text = git_out(
        root,
        "-c", "core.quotepath=false",
        "diff", "--no-color", "--no-ext-diff", "-U0",
        merge_base, head, "--", path,
    )
    old_total = blob_line_count(root, merge_base, path)
    counts = {c: 0 for c in CATEGORIES}
    body_inserts: list[dict] = []
    for h in parse_hunks(diff_text):
        cat = classify_hunk(h["old_start"], h["old_len"], h["ctx"], h["added"], old_total)
        counts[cat] += 1
        if cat == "func_body_insert":
            body_inserts.append(h)
    return counts, body_inserts


def run_selftest() -> int:
    failed = 0

    def check(name: str, got, expect) -> None:
        nonlocal failed
        ok = got == expect
        if not ok:
            failed += 1
        print(f"  {'PASS' if ok else 'FAIL'} {name}: expect={expect!r} got={got!r}")

    # classify_hunk pure cases (old_total=18, mirrors the format probe fixture)
    check("struct field insert -> other_insert",
          classify_hunk(8, 0, "type Cfg struct {", ["\tTag  string"], 18), "other_insert")
    check("in-func insert -> func_body_insert",
          classify_hunk(12, 0, "func A() int {", ["\tx += 5 // injected"], 18), "func_body_insert")
    check("new top-level func between funcs -> top_level_decl",
          classify_hunk(15, 0, "func A() int {",
                        ["func Injected() int {", "\treturn 9", "}", ""], 18), "top_level_decl")
    check("append at EOF -> eof_append",
          classify_hunk(18, 0, "func B() int {",
                        ["", "func Appended() int {", "\treturn 42", "}"], 18), "eof_append")
    check("deletion-bearing hunk in func -> func_body_modify",
          classify_hunk(5, 2, "func A() int {", ["\treturn 3"], 18), "func_body_modify")
    check("deletion-bearing hunk at top level -> other_modify",
          classify_hunk(3, 1, "", ["const V = 2"], 18), "other_modify")
    check("gofmt column-0 label in func body -> func_body_insert",
          classify_hunk(12, 0, "func A() int {", ["loop:"], 18), "func_body_insert")
    check("method insert -> top_level_decl",
          classify_hunk(15, 0, "func A() int {",
                        ["func (s *S) M() int {", "\treturn 1", "}"], 18), "top_level_decl")
    check("doc comment + func insert -> top_level_decl",
          classify_hunk(15, 0, "func A() int {",
                        ["// Injected does things.", "func Injected() {}"], 18), "top_level_decl")
    check("insert before first line -> other_insert (no func ctx)",
          classify_hunk(0, 0, "", ["\tweird"], 18), "other_insert")

    # parse_hunks on a captured `git diff -U0` transcript (format probe, git 2.52)
    probe = """\
diff --git a/c.go b/c.go
index 2886d0f..ab698dd 100644
--- a/c.go
+++ b/c.go
@@ -8,0 +9 @@ type Cfg struct {
+\tTag  string
@@ -12,0 +14 @@ func A() int {
+\tx += 5 // injected
@@ -15,0 +18,4 @@ func A() int {
+func Injected() int {
+\treturn 9
+}
+
@@ -18,0 +25,4 @@ func B() int {
+
+func Appended() int {
+\treturn 42
+}
"""
    hunks = parse_hunks(probe)
    check("parse_hunks count", len(hunks), 4)
    if len(hunks) == 4:
        check("hunk0 old range", (hunks[0]["old_start"], hunks[0]["old_len"]), (8, 0))
        check("hunk0 ctx", hunks[0]["ctx"], "type Cfg struct {")
        check("hunk2 added lines", len(hunks[2]["added"]), 4)
        cats = [classify_hunk(h["old_start"], h["old_len"], h["ctx"], h["added"], 18) for h in hunks]
        check("probe classification",
              cats, ["other_insert", "func_body_insert", "top_level_decl", "eof_append"])

    # TK companion filter
    check("tk companion excluded",
          bool(TK_COMPANION_RE.search("backend/internal/handler/gateway_handler_tk_affinity.go")), True)
    check("tk suffix excluded",
          bool(TK_COMPANION_RE.search("backend/internal/service/setting_tk.go")), True)
    check("plain upstream file included",
          bool(TK_COMPANION_RE.search("backend/internal/service/gateway_service.go")), False)

    if failed:
        print(f"upstream-insertion-invasiveness selftest: {failed} case(s) FAILED", file=sys.stderr)
        return 1
    print("ok: upstream-insertion-invasiveness selftest passed")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--base", default="origin/main")
    ap.add_argument("--head", default="HEAD")
    ap.add_argument("--upstream-ref", default="upstream/main")
    ap.add_argument("--root", default=str(REPO_ROOT),
                    help="repo root to operate on (default: this checkout)")
    ap.add_argument("--summary-file", default="",
                    help="markdown file to APPEND a report table to (e.g. $GITHUB_STEP_SUMMARY)")
    ap.add_argument("--strict", action="store_true",
                    help="exit 2 if any func_body_insert hunk is found (default: advisory, exit 0)")
    ap.add_argument("--selftest", action="store_true",
                    help="run the pure-logic self-test and exit")
    args = ap.parse_args()

    if args.selftest:
        return run_selftest()

    root = Path(args.root).resolve()
    if not root.is_dir():
        print(f"FAIL: --root '{args.root}' is not a directory", file=sys.stderr)
        return 1

    base_sha = resolve_commit(root, args.base)
    head_sha = resolve_commit(root, args.head)
    upstream_sha = resolve_commit(root, args.upstream_ref)

    try:
        merge_base = git_out(root, "merge-base", base_sha, head_sha).strip()
        changed = git_out(root, "diff", "--name-only", "--diff-filter=M",
                          "--no-renames", merge_base, head_sha).splitlines()
    except subprocess.CalledProcessError as e:
        print(f"FAIL: git failed: {e}", file=sys.stderr)
        return 1

    candidates = [
        p for p in (line.strip() for line in changed)
        if p.endswith(".go") and not TK_COMPANION_RE.search(p)
    ]
    files = [p for p in candidates if exists_in_ref(root, upstream_sha, p)]

    print(f"[upstream-insertion-invasiveness] advisory — base={base_sha[:12]} "
          f"head={head_sha[:12]} merge-base={merge_base[:12]} upstream={args.upstream_ref}@{upstream_sha[:12]}")

    totals = {c: 0 for c in CATEGORIES}
    per_file: list[tuple[str, dict, list[dict]]] = []
    for path in files:
        try:
            counts, body_inserts = analyze_file(root, merge_base, head_sha, path)
        except subprocess.CalledProcessError as e:
            print(f"FAIL: git diff failed for {path}: {e}", file=sys.stderr)
            return 1
        per_file.append((path, counts, body_inserts))
        for c in CATEGORIES:
            totals[c] += counts[c]

    if not files:
        print("  no modified non-TK .go files shared with upstream — nothing to analyze")
    else:
        print(f"  analyzed {len(files)} upstream-shared modified Go file(s):")
        for path, counts, body_inserts in per_file:
            line = " ".join(f"{c}={counts[c]}" for c in CATEGORIES if counts[c])
            print(f"    {path}: {line or 'no hunks'}")
            for h in body_inserts:
                ctx = h["ctx"] or "<no function context>"
                print(f"      -> func-body insert after old line {h['old_start']} in: {ctx}")
        print("  totals: " + " ".join(f"{c}={totals[c]}" for c in CATEGORIES))

    n_body = totals["func_body_insert"]
    if n_body:
        print(f"  advisory: {n_body} hunk(s) insert INSIDE upstream function bodies — the #1 "
              "future-merge-conflict driver.")
        print("  prefer a *_tk_*.go companion file + a 1-line call site "
              "(CLAUDE.md §5 minimal-invasion patterns).")

    if args.summary_file:
        with open(args.summary_file, "a", encoding="utf-8") as fh:
            fh.write("### Upstream insertion invasiveness (advisory)\n\n")
            fh.write(f"base `{base_sha[:12]}` → head `{head_sha[:12]}` vs "
                     f"upstream `{args.upstream_ref}` @ `{upstream_sha[:12]}`\n\n")
            if not files:
                fh.write("No modified non-TK `.go` files shared with upstream — nothing to analyze.\n\n")
            else:
                fh.write("| file | EOF append | top-level decl | decl-block insert "
                         "| **func-body insert** | func-body modify | other modify |\n")
                fh.write("|---|---|---|---|---|---|---|\n")
                for path, counts, _ in per_file:
                    fh.write(f"| `{path}` | {counts['eof_append']} | {counts['top_level_decl']} "
                             f"| {counts['other_insert']} | **{counts['func_body_insert']}** "
                             f"| {counts['func_body_modify']} | {counts['other_modify']} |\n")
                fh.write(f"| **totals** | {totals['eof_append']} | {totals['top_level_decl']} "
                         f"| {totals['other_insert']} | **{totals['func_body_insert']}** "
                         f"| {totals['func_body_modify']} | {totals['other_modify']} |\n\n")
                if n_body:
                    fh.write(f"> :warning: {n_body} hunk(s) insert inside upstream function bodies — "
                             "the #1 future-merge-conflict driver. Prefer `*_tk_*.go` companion "
                             "files (CLAUDE.md §5). Advisory only — this job never blocks.\n\n")
                else:
                    fh.write("> :white_check_mark: No insertions inside upstream function bodies.\n\n")

    if args.strict and n_body > 0:
        print(f"FAIL(--strict): {n_body} func-body insertion(s) found", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
