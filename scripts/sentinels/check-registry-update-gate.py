#!/usr/bin/env python3
"""
check-registry-update-gate.py — require sentinel registry updates when
load-bearing TK/NewAPI surfaces change.

This is the hard gate behind the recurring review note: "补充必要的 upstream
merge 覆写防护门禁". Existing sentinel checkers prove that current literals are
still present; this checker proves that PRs changing known hotspot files also
update the relevant registry in the same branch.

Two trigger shapes:

  1. EDIT with deletions — a hotspot/sentinel-covered file changed and at
     least one line was deleted (an anchored literal may have been dropped).
     Pure-insertion edits of EXISTING files stay exempt: they cannot remove
     an anchor.
  2. NEW load-bearing surface — a newly ADDED file matches a hotspot pattern
     (new `*_tk_*.go` companion, new bridge file, new TK composable /
     constants module). New files are pure-insertion by definition, so
     without this trigger they sail through every marker gate with zero
     sentinel coverage — exactly the recurring post-review ask "增加 upstream
     merge 覆写防护门禁". The author must either pin anchors for the new
     surface (and ideally its injection point in the upstream file) in a
     sentinel registry, or acknowledge with the `sentinel-registry-reviewed`
     marker in the PR description.

Exit codes:
  0 — no hotspot/sentinel-covered source changed, or matching registry changed.
  1 — source changed without a matching sentinel registry update.
  2 — git/registry state is malformed or comparison base cannot be resolved.
"""
from __future__ import annotations

import argparse
import fnmatch
import json
import os
import re
import subprocess
import sys
from pathlib import Path
from typing import Iterable

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
REGISTRY_GLOB = "scripts/sentinels/*.json"
DEFAULT_BASE = "origin/main"

# i18n locale dictionaries are append-heavy (TK adds keys on nearly every
# feature PR). Requiring a registry update / review marker on every touch is
# pure ceremony; the independent sentinel anchor verifiers still prove any
# anchored literal survives. Excluded from THIS update-gate outright.
I18N_LOCALE_RE = re.compile(r"^frontend/src/i18n/locales/.*\.ts$")

# Commit-message markers that count as explicit author assertions that the
# author reviewed the touched hotspot files against their guarding sentinel
# registries and decided no anchor change is required.
#
# Why these exist: without a marker, the only way for an author to pass this
# gate when no sentinel literal needs to change is to vandalize the registry
# rationale text (the gate accepts "file appeared in diff" as proof). That
# led to "Reviewed during merge/upstream-XXXX — anchors verified intact"
# paragraphs accumulating on every upstream merge. Markers replace that with
# a single line in the commit message, leaving the registry text clean.
#
# Markers (any one is sufficient, and they accumulate across all commits in
# the PR's `base..head` range):
#   sentinel-registry-reviewed — author reviewed the touched hotspots, no
#                                anchor literal needs to change in this PR.
#   upstream-merge             — this is an upstream-merge PR (matches the
#                                shape gate used by upstream-override-marker
#                                / upstream-merge-pr-shape).
SENTINEL_GATE_MARKERS = (
    "sentinel-registry-reviewed",
    "upstream-merge",
)

# Hotspots that repeatedly need explicit upstream-overwrite guards. Exact files
# already listed in a sentinel registry are also guarded automatically; these
# patterns catch newly introduced or still-unregistered TK/NewAPI surfaces.
HOTSPOT_PATTERNS: dict[str, list[str]] = {
    "frontend/src/components/account/CreateAccountModal.vue": ["scripts/sentinels/newapi.json", "scripts/sentinels/frontend-tk.json"],
    "frontend/src/components/account/EditAccountModal.vue": ["scripts/sentinels/newapi.json", "scripts/sentinels/frontend-tk.json"],
    "frontend/src/components/account/AccountNewApiPlatformFields.vue": ["scripts/sentinels/newapi.json"],
    "frontend/src/components/account/ModelWhitelistSelector.vue": ["scripts/sentinels/newapi.json"],
    "frontend/src/components/account/QuotaLimitCard.vue": ["scripts/sentinels/frontend-tk.json"],
    "frontend/src/components/common/ModelIcon.vue": ["scripts/sentinels/newapi.json"],
    "frontend/src/composables/useTkAccountNewApiPlatform.ts": ["scripts/sentinels/newapi.json"],
    "frontend/src/composables/useModelWhitelist.ts": ["scripts/sentinels/newapi.json"],
    "frontend/src/constants/gatewayPlatforms.ts": ["scripts/sentinels/newapi.json"],
    "frontend/src/composables/usePlatformOptions.ts": ["scripts/sentinels/newapi.json"],
    "frontend/src/views/auth/LoginView.vue": ["scripts/sentinels/frontend-tk.json"],
    "frontend/tailwind.config.js": ["scripts/sentinels/frontend-tk.json"],
    "frontend/src/style.css": ["scripts/sentinels/frontend-tk.json"],
    "backend/internal/integration/newapi/*.go": ["scripts/sentinels/newapi.json"],
    "backend/internal/integration/newapi/**/*.go": ["scripts/sentinels/newapi.json"],
    "backend/internal/**/*_tk_*.go": ["scripts/sentinels/newapi.json", "scripts/sentinels/gateway-tk.json"],
    "backend/internal/relay/bridge/*.go": ["scripts/sentinels/newapi.json"],
    # Generic TK-only frontend conventions (mirror of the override-marker
    # EXCLUDE list): these files are invisible to the upstream-override gate
    # by design, so THIS gate is the only thing forcing sentinel coverage for
    # a brand-new TK frontend surface. fnmatch `*` crosses `/`, so the
    # admin/tk pattern covers nested files too.
    "frontend/src/composables/useTk*.ts": ["scripts/sentinels/frontend-tk.json"],
    "frontend/src/constants/*.tk.ts": ["scripts/sentinels/frontend-tk.json"],
    "frontend/src/components/admin/tk/*.vue": ["scripts/sentinels/frontend-tk.json"],
    "frontend/src/api/admin/tk/*.ts": ["scripts/sentinels/frontend-tk.json"],
}


def run_git(args: list[str], check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["git", *args],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=check,
    )


def resolve_base(explicit_base: str | None) -> str | None:
    base = explicit_base or os.environ.get("SENTINEL_GATE_BASE_REF")
    if not base:
        github_base = os.environ.get("GITHUB_BASE_REF")
        base = f"origin/{github_base}" if github_base else DEFAULT_BASE

    current_branch = run_git(["branch", "--show-current"], check=False).stdout.strip()
    if not explicit_base and not os.environ.get("SENTINEL_GATE_BASE_REF") and not os.environ.get("GITHUB_BASE_REF"):
        if current_branch in {"main", "master"}:
            return None

    if run_git(["rev-parse", "--verify", f"{base}^{{commit}}"], check=False).returncode == 0:
        return base

    # CI checkout may only have refs/remotes/origin/main after fetch-depth:0;
    # local checkouts may have main but not origin/main if remote setup is unusual.
    fallback = "main" if base == DEFAULT_BASE else None
    if fallback and run_git(["rev-parse", "--verify", f"{fallback}^{{commit}}"], check=False).returncode == 0:
        return fallback

    print(
        f"FATAL: cannot resolve comparison base `{base}`. Fetch origin/main or set SENTINEL_GATE_BASE_REF.",
        file=sys.stderr,
    )
    return "__UNRESOLVED__"


def changed_files(base: str, head: str) -> set[str]:
    proc = run_git(["diff", "--name-only", "--diff-filter=ACMRTUXB", f"{base}...{head}"])
    return {line.strip() for line in proc.stdout.splitlines() if line.strip()}


def working_tree_changed_files() -> set[str]:
    changed: set[str] = set()
    for args in (["diff", "--name-only", "--diff-filter=ACMRTUXB"], ["diff", "--cached", "--name-only", "--diff-filter=ACMRTUXB"]):
        proc = run_git(args)
        changed.update(line.strip() for line in proc.stdout.splitlines() if line.strip())
    return changed


def added_files(base: str, head: str) -> set[str]:
    """Newly ADDED paths across the same three sources `changed_files` /
    `deletion_counts` read (committed range, working tree, index)."""
    added: set[str] = set()
    sources = (
        ["diff", "--name-only", "--diff-filter=A", f"{base}...{head}"],
        ["diff", "--name-only", "--diff-filter=A"],
        ["diff", "--cached", "--name-only", "--diff-filter=A"],
    )
    for args in sources:
        proc = run_git(args, check=False)
        if proc.returncode != 0:
            continue
        added.update(line.strip() for line in proc.stdout.splitlines() if line.strip())
    return added


def commit_messages(base: str, head: str) -> str:
    proc = run_git(["log", f"{base}..{head}", "--pretty=%B"], check=False)
    if proc.returncode != 0:
        return ""
    return proc.stdout


def has_review_marker(base: str, head: str, pr_body: str = "") -> tuple[bool, str | None]:
    """Return (matched, marker) — True when any SENTINEL_GATE_MARKERS appears
    verbatim in a commit message between base and head, OR in the PR body
    (the mutable surface CI passes via --pr-body)."""
    text = (pr_body or "") + "\n" + commit_messages(base, head)
    for marker in SENTINEL_GATE_MARKERS:
        if marker in text:
            return True, marker
    return False, None


def deletion_counts(base: str, head: str) -> dict[str, int]:
    """Aggregate per-path deleted-line counts across the committed range AND
    the working tree / index (the same three sources the gate reads for
    changed files). A path with zero deletions everywhere is a pure-insertion
    change that cannot remove a load-bearing symbol, so it carries no
    upstream-merge revert risk. Binary (`-`) is treated as a deletion (can't
    tell) to stay conservative."""
    counts: dict[str, int] = {}
    sources = (
        ["diff", "--numstat", f"{base}...{head}"],
        ["diff", "--numstat"],
        ["diff", "--cached", "--numstat"],
    )
    for args in sources:
        proc = run_git(args, check=False)
        if proc.returncode != 0:
            continue
        for line in proc.stdout.splitlines():
            parts = line.split("\t")
            if len(parts) != 3:
                continue
            added, deleted, path = parts
            if added == "-" or deleted == "-":
                counts[path] = counts.get(path, 0) + 1  # binary → conservative
                continue
            counts[path] = counts.get(path, 0) + int(deleted)
    return counts


def registry_paths() -> list[Path]:
    return sorted(REPO_ROOT.glob(REGISTRY_GLOB))


def load_standard_sentinels(path: Path) -> list[dict]:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise SystemExit(f"FATAL: invalid JSON in {path.relative_to(REPO_ROOT)}: {exc}")
    sentinels = data.get("sentinels")
    if not isinstance(sentinels, list):
        return []
    return [entry for entry in sentinels if isinstance(entry, dict)]


def covered_paths_by_registry() -> dict[str, set[str]]:
    out: dict[str, set[str]] = {}
    for registry in registry_paths():
        rel_registry = registry.relative_to(REPO_ROOT).as_posix()
        sentinels = load_standard_sentinels(registry)
        if not sentinels:
            continue
        out.setdefault(rel_registry, set())
        for entry in sentinels:
            if isinstance(entry.get("path"), str):
                out[rel_registry].add(entry["path"])
    return out


def matching_hotspot_registries(path: str) -> set[str]:
    registries: set[str] = set()
    for pattern, suggested in HOTSPOT_PATTERNS.items():
        if fnmatch.fnmatch(path, pattern):
            registries.update(suggested)
    return registries


def required_registries(path: str, deleted: int, is_added: bool, covered: set[str]) -> set[str]:
    """Pure per-path verdict (no git / IO, unit-tested by --selftest).
    Returns the registries this path obliges the PR to update (empty set =
    no obligation). Obligation triggers:
      - covered/hotspot file changed WITH deletions (an anchor may be gone), or
      - hotspot-matching file is NEWLY ADDED (a brand-new load-bearing TK
        surface with zero sentinel coverage — pure-insertion must NOT exempt
        it, that is the exact shape that previously sailed through).
    """
    if I18N_LOCALE_RE.match(path):
        return set()
    hotspot = matching_hotspot_registries(path)
    related = covered | hotspot
    if not related:
        return set()
    if deleted == 0 and not (is_added and hotspot):
        return set()  # pure-insertion edit of an existing file — cannot drop an anchor
    return related


def compact(items: Iterable[str]) -> str:
    values = sorted(set(items))
    return ", ".join(values) if values else "(none)"


def run_selftest() -> int:
    nj = "scripts/sentinels/newapi.json"
    gj = "scripts/sentinels/gateway-tk.json"
    fj = "scripts/sentinels/frontend-tk.json"
    cases = [
        # (path, deleted, is_added, covered, expected_required)
        # EDIT with deletions on a hotspot → obligation
        ("backend/internal/service/foo_tk_bar.go", 3, False, set(), {nj, gj}),
        # pure-insertion EDIT of an existing hotspot → exempt (unchanged behavior)
        ("backend/internal/service/foo_tk_bar.go", 0, False, set(), set()),
        # NEW hotspot file, pure-insertion → obligation (the new trigger)
        ("backend/internal/service/foo_tk_bar.go", 0, True, set(), {nj, gj}),
        ("backend/internal/relay/bridge/new_relay.go", 0, True, set(), {nj}),
        ("frontend/src/composables/useTkNewThing.ts", 0, True, set(), {fj}),
        ("frontend/src/constants/newThing.tk.ts", 0, True, set(), {fj}),
        ("frontend/src/components/admin/tk/NewPanel.vue", 0, True, set(), {fj}),
        # NEW file that matches no hotspot → exempt (override gate's domain)
        ("backend/internal/handler/plain.go", 0, True, set(), set()),
        # sentinel-covered (non-hotspot) path: deletions oblige, insertion doesn't
        ("backend/internal/server/routes/gateway.go", 2, False, {gj}, {gj}),
        ("backend/internal/server/routes/gateway.go", 0, False, {gj}, set()),
        # i18n always exempt
        ("frontend/src/i18n/locales/en.ts", 9, False, set(), set()),
    ]
    failed = 0
    for i, (path, deleted, is_added, covered, expect) in enumerate(cases):
        got = required_registries(path, deleted, is_added, covered)
        status = "PASS" if got == expect else "FAIL"
        if got != expect:
            failed += 1
        print(f"  {status} case {i}: {path} deleted={deleted} added={is_added} → {sorted(got)}")
    if failed:
        print(f"registry-update-gate selftest: {failed} case(s) FAILED", file=sys.stderr)
        return 1
    print("ok: registry-update-gate selftest passed")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", help="git ref to compare against (default: GITHUB_BASE_REF or origin/main)")
    parser.add_argument("--head", default="HEAD", help="git ref to compare as the change head (default: HEAD)")
    parser.add_argument("--quiet", action="store_true", help="only print failures")
    parser.add_argument("--pr-body", default="",
                        help="PR description text; a review marker here satisfies the gate "
                             "(mutable surface — CI passes the PR body)")
    parser.add_argument("--selftest", action="store_true",
                        help="run the pure-logic self-test and exit")
    args = parser.parse_args()

    if args.selftest:
        return run_selftest()

    # Advisory mode: local pre-commit/pre-push cannot see the in-flight commit
    # message or the PR body, so a hard block there is a structural false
    # deadlock. Preflight sets MARKER_GATE_ADVISORY=1 → compute + print but
    # never block. The hard gate runs in CI against the PR body.
    advisory = bool(os.environ.get("MARKER_GATE_ADVISORY"))

    base = resolve_base(args.base)
    if base is None:
        if not args.quiet:
            print("sentinel registry update gate: skip on main/master without explicit base")
        return 0
    if base == "__UNRESOLVED__":
        return 2

    if run_git(["rev-parse", "--verify", f"{args.head}^{{commit}}"], check=False).returncode != 0:
        print(f"FATAL: cannot resolve head `{args.head}`", file=sys.stderr)
        return 2

    changed = changed_files(base, args.head)
    changed.update(working_tree_changed_files())
    if not changed:
        if not args.quiet:
            print(f"sentinel registry update gate: no changes vs {base}...{args.head}")
        return 0

    coverage = covered_paths_by_registry()
    changed_registries = {p for p in changed if fnmatch.fnmatch(p, REGISTRY_GLOB)}
    # Pure-insertion paths (no deletions anywhere) cannot remove a load-bearing
    # symbol → no anchor can have been dropped → safe to auto-accept, EXCEPT
    # newly ADDED hotspot files: a brand-new load-bearing TK surface has zero
    # sentinel coverage, and "pure insertion" is its normal shape.
    deletions = deletion_counts(base, args.head)
    added = added_files(base, args.head)
    violations: list[tuple[str, set[str], set[str], bool]] = []

    for path in sorted(changed):
        if fnmatch.fnmatch(path, REGISTRY_GLOB):
            continue
        covered = {registry for registry, paths in coverage.items() if path in paths}
        related = required_registries(path, deletions.get(path, 0), path in added, covered)
        if not related:
            continue
        if changed_registries.isdisjoint(related):
            violations.append((path, related, changed_registries, path in added))

    if not violations:
        if not args.quiet:
            print(
                "sentinel registry update gate: ok "
                f"(changed registries: {compact(changed_registries)})"
            )
        return 0

    # Before declaring violation, accept an explicit commit-message marker as
    # proof the author reviewed the touched hotspots and decided no anchor
    # literal needs to change. This is the documented escape hatch for the
    # "touched a hotspot but the existing sentinels still hold" case — the
    # alternative is rationale-text vandalism every PR.
    matched, marker = has_review_marker(base, args.head, args.pr_body)
    if matched:
        if not args.quiet:
            print(
                "sentinel registry update gate: ok "
                f"(review marker '{marker}' present; hotspot review acknowledged)"
            )
        return 0

    stream = sys.stdout if advisory else sys.stderr
    prefix = "advisory (not blocking — CI enforces on PR body)" if advisory else "FAIL"
    print(f"sentinel registry update gate: {prefix}", file=stream)
    print(
        "  Load-bearing TK/NewAPI surfaces changed (with deletions, or newly added) "
        "without updating the matching sentinel registry.",
        file=stream,
    )
    for path, related, seen, is_new in violations:
        tag = " [NEW load-bearing surface]" if is_new else ""
        print(f"  - {path}{tag}", file=stream)
        print(f"      update one of: {compact(related)}", file=stream)
        print(f"      changed registries in this diff: {compact(seen)}", file=stream)
    print(
        "  Fix (any one): "
        "(a) for EDITED files: make the change pure-insertion (no deleted lines); "
        "(b) add sentinel anchor(s) for the surface in the same PR — for a NEW "
        "TK companion, anchor BOTH the new file and its injection line in the "
        "upstream file; "
        "(c) put one of these markers in the PR description: "
        + ", ".join(SENTINEL_GATE_MARKERS)
        + " — use marker only when you reviewed the surface and confirmed no anchor is required.",
        file=stream,
    )
    return 0 if advisory else 1


if __name__ == "__main__":
    sys.exit(main())
