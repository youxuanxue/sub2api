#!/usr/bin/env python3
"""apply-fix-ledger.py — PR-time fix-ledger generator + drift gate.

Single deterministic tool shared by BOTH issue-watchdog ledgers:

  --ledger upstream    .cache/upstream/{triage,fixes,fact-checks}.json
                       (Wei-Shaw/sub2api,       trailer: Upstream-Fixes)
  --ledger anthropic   .cache/anthropic/cc-{triage,fixes,fact-checks}.json
                       (anthropics/claude-code, trailer: Anthropic-Fixes)

Why this exists
---------------
The daily `*-issue-watchdog.yml` already auto-propagates fact-checks.json into
fixes.json + triage.json (via the engine functions in issue-watchdog.py) and
opens a cache-refresh PR. The only step a human must do is author the
fact-check entry — the `fixed_if_all_present` code anchors, i.e. the
irreducible "which code facts prove this is fixed" judgment.

This tool lets that propagation happen INSIDE the fix PR instead of waiting for
the next daily watchdog run, and lets preflight GATE it so a fix can't merge
half-recorded:

  --apply   author runs once after adding a fact-check; reconciles
            fixes.json + triage.json from fact-checks.json. Uses the SAME
            engine functions as the daily watchdog, so the output is
            byte-identical — no dual-writer churn.

  --check --commits-range A..B
            preflight drift gate, scoped to the issues THIS PR declares it
            fixes via a commit trailer. For each `Upstream-Fixes:` /
            `Anthropic-Fixes: <ref>` trailer in A..B it requires:
              (1) a fact-check entry covering <ref> exists,
              (2) every one of its anchors resolves in the working tree,
              (3) fixes.json + triage.json already reflect it (author ran
                  --apply).
            Deliberately trailer-scoped: it never fails an unrelated PR over
            pre-existing cosmetic drift or anchor rot in OTHER ledger entries
            (that stays the daily watchdog's job to reconcile).

  --selftest  hermetic temp-dir fixtures; exercised by scripts/preflight.sh.

Exit codes (--check): 0 ok · 1 gate failed · 2 commit range unresolvable (skip).
"""
from __future__ import annotations

import argparse
import copy
import importlib.util
import re
import subprocess
import sys
from pathlib import Path
from typing import Any

_ENGINE_PATH = Path(__file__).resolve().parent / "issue-watchdog.py"

LEDGERS: dict[str, dict[str, str]] = {
    "upstream": {
        "triage": ".cache/upstream/triage.json",
        "fixes": ".cache/upstream/fixes.json",
        "fact_checks": ".cache/upstream/fact-checks.json",
        "trailer": "Upstream-Fixes",
        "repo": "Wei-Shaw/sub2api",
    },
    "anthropic": {
        "triage": ".cache/anthropic/cc-triage.json",
        "fixes": ".cache/anthropic/cc-fixes.json",
        "fact_checks": ".cache/anthropic/cc-fact-checks.json",
        "trailer": "Anthropic-Fixes",
        "repo": "anthropics/claude-code",
    },
}

_ISSUE_NUM_RE = re.compile(r"#(\d+)")


def load_engine():
    """Import scripts/upstream/issue-watchdog.py as a module (hyphenated name).

    The engine has a `__main__` guard and no import side effects, so this only
    binds its reusable functions (check_fact / ensure_fixed_entry /
    update_triage_fixed / load_json / write_json).
    """
    spec = importlib.util.spec_from_file_location("issue_watchdog_engine", _ENGINE_PATH)
    if spec is None or spec.loader is None:  # pragma: no cover - defensive
        raise RuntimeError(f"cannot load engine at {_ENGINE_PATH}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _issue_numbers(text: str) -> list[int]:
    """All `#NNNN` issue numbers in a string, in order."""
    return [int(m) for m in _ISSUE_NUM_RE.findall(text)]


def declared_issue_numbers(trailer_value: str) -> list[int]:
    """Issue numbers from a trailer value.

    Accepts `Wei-Shaw/sub2api#1723`, `#1723`, `1723`, and comma/space lists
    like `#60168, #63885 #64777`.
    """
    nums = _issue_numbers(trailer_value)
    if not nums:
        nums = [int(tok) for tok in re.findall(r"\b(\d+)\b", trailer_value)]
    seen: set[int] = set()
    out: list[int] = []
    for n in nums:
        if n not in seen:
            seen.add(n)
            out.append(n)
    return out


def parse_trailers(commit_text: str, trailer_key: str) -> list[str]:
    """Values of every `<trailer_key>: <value>` line (case-insensitive)."""
    pat = re.compile(rf"^\s*{re.escape(trailer_key)}\s*:\s*(.+?)\s*$", re.IGNORECASE)
    return [m.group(1) for line in commit_text.splitlines() if (m := pat.match(line))]


def check_covers_issue(check: dict[str, Any], repo: str, issue_num: int) -> bool:
    """True if a fact-check's `upstream` field covers <repo>#<issue_num>.

    Handles multi-issue fact-checks whose `upstream` is a comma-joined list
    (e.g. `anthropics/claude-code#60168, #63885, #64777`).
    """
    upstream = str(check.get("upstream", ""))
    return repo in upstream and issue_num in set(_issue_numbers(upstream))


def reconcile(engine, facts: dict[str, Any], fixes: dict[str, Any], triage: dict[str, Any]) -> dict[str, Any]:
    """Propagate every fully-anchored fact-check into fixes + triage in place.

    Returns {changed: bool, missing_anchor: [(upstream, [specs])]}. A
    fact-check whose anchors do not all resolve is left un-propagated and
    reported under missing_anchor (anchor rot / regressed fix).
    """
    changed = False
    missing_anchor: list[tuple[str, list[str]]] = []
    for check in facts.get("checks", []):
        specs = check.get("fixed_if_all_present", [])
        results = [engine.check_fact(spec) for spec in specs]
        if not (specs and all(r["ok"] for r in results)):
            unresolved = [r["spec"] for r in results if not r["ok"]] or ["<empty fixed_if_all_present>"]
            missing_anchor.append((str(check.get("upstream", "?")), unresolved))
            continue
        if engine.ensure_fixed_entry(fixes, check):
            changed = True
        if engine.update_triage_fixed(triage, check):
            changed = True
    return {"changed": changed, "missing_anchor": missing_anchor}


def _entries_for(issue_num: int, repo: str, issues: list[dict[str, Any]]) -> list[dict[str, Any]]:
    return [e for e in issues
            if repo in str(e.get("upstream", "")) and issue_num in set(_issue_numbers(str(e.get("upstream", ""))))]


def _issue_drifted(issue_num: int, repo: str, fixes_b, fixes_a, triage_b, triage_a) -> bool:
    """True if reconciling changed this issue's fixes/triage entries."""
    return (_entries_for(issue_num, repo, fixes_b.get("issues", [])) != _entries_for(issue_num, repo, fixes_a.get("issues", []))
            or _entries_for(issue_num, repo, triage_b.get("issues", [])) != _entries_for(issue_num, repo, triage_a.get("issues", [])))


def _ledger_name(cfg: dict[str, str]) -> str:
    for name, c in LEDGERS.items():
        if c["repo"] == cfg["repo"]:
            return name
    return "upstream"


def evaluate_declared(engine, cfg: dict[str, str], facts, fixes_before, triage_before, declared: list[int]) -> list[str]:
    """Return human-readable problems for the declared issues (empty == ok).

    Shared by the live gate (cmd_check, declared from git trailers) and the
    hermetic self-test (declared injected directly).
    """
    fixes_after = copy.deepcopy(fixes_before)
    triage_after = copy.deepcopy(triage_before)
    reconcile(engine, facts, fixes_after, triage_after)

    problems: list[str] = []
    for issue_num in declared:
        matches = [c for c in facts.get("checks", []) if check_covers_issue(c, cfg["repo"], issue_num)]
        if not matches:
            problems.append(
                f"{cfg['repo']}#{issue_num}: declared via {cfg['trailer']}: but no fact-check entry in {cfg['fact_checks']}"
            )
            continue
        for check in matches:
            specs = check.get("fixed_if_all_present", [])
            missing = [r["spec"] for r in (engine.check_fact(s) for s in specs) if not r["ok"]]
            if not specs:
                problems.append(f"{check['upstream']}: fact-check has empty fixed_if_all_present")
            elif missing:
                problems.append(f"{check['upstream']}: fact-check anchors do not resolve in the working tree: {missing}")
        if _issue_drifted(issue_num, cfg["repo"], fixes_before, fixes_after, triage_before, triage_after):
            problems.append(
                f"{cfg['repo']}#{issue_num}: fact-check not propagated to {cfg['fixes']} / {cfg['triage']} "
                f"(run: python3 scripts/upstream/apply-fix-ledger.py --ledger {_ledger_name(cfg)} --apply)"
            )
    return problems


def cmd_apply(engine, cfg: dict[str, str], root: Path) -> int:
    facts = engine.load_json(root / cfg["fact_checks"])
    fixes = engine.load_json(root / cfg["fixes"])
    triage = engine.load_json(root / cfg["triage"])
    summary = reconcile(engine, facts, fixes, triage)
    if summary["changed"]:
        engine.write_json(root / cfg["fixes"], fixes)
        engine.write_json(root / cfg["triage"], triage)
        print(f"apply[{cfg['repo']}]: fixes/triage reconciled from fact-checks")
    else:
        print(f"apply[{cfg['repo']}]: already consistent, nothing to write")
    for up, specs in summary["missing_anchor"]:
        print(f"  note: fact-check {up} not propagated — anchors unresolved: {specs}", file=sys.stderr)
    return 0


def _git_commit_messages(commits_range: str, root: Path) -> str | None:
    try:
        out = subprocess.run(
            ["git", "-C", str(root), "log", "--no-merges", "--format=%B%x00", commits_range],
            capture_output=True, text=True, check=True,
        )
    except (subprocess.CalledProcessError, FileNotFoundError):
        return None
    return out.stdout


def cmd_check(engine, cfg: dict[str, str], root: Path, commits_range: str, quiet: bool) -> int:
    """Trailer-scoped drift gate. 0 ok / 1 fail / 2 unresolvable range."""
    raw = _git_commit_messages(commits_range, root)
    if raw is None:
        return 2

    declared: list[int] = []
    for message in raw.split("\x00"):
        for value in parse_trailers(message, cfg["trailer"]):
            declared.extend(declared_issue_numbers(value))
    declared = sorted(set(declared))

    if not declared:
        if not quiet:
            print(f"  ok: no {cfg['trailer']}: trailers in {commits_range} (nothing to gate)")
        return 0

    facts = engine.load_json(root / cfg["fact_checks"])
    fixes = engine.load_json(root / cfg["fixes"])
    triage = engine.load_json(root / cfg["triage"])
    problems = evaluate_declared(engine, cfg, facts, fixes, triage, declared)

    if problems:
        print(f"  FAIL: {cfg['trailer']} gate ({len(problems)} problem(s)):", file=sys.stderr)
        for p in problems:
            print(f"    - {p}", file=sys.stderr)
        return 1
    if not quiet:
        print(f"  ok: {len(declared)} declared {cfg['trailer']} issue(s) have consistent fact-checks")
    return 0


# --------------------------------------------------------------------------- #
# Self-test (hermetic; no git, no network, no real ledger files).
# --------------------------------------------------------------------------- #
def run_selftest() -> int:
    import json
    import tempfile

    engine = load_engine()
    failures: list[str] = []

    def expect(name: str, cond: bool) -> None:
        print(f"{'PASS' if cond else 'FAIL'} {name}")
        if not cond:
            failures.append(name)

    expect("test_parse_trailers_single",
           parse_trailers("subj\n\nUpstream-Fixes: Wei-Shaw/sub2api#1723\n", "Upstream-Fixes") == ["Wei-Shaw/sub2api#1723"])
    expect("test_parse_trailers_case_insensitive",
           parse_trailers("upstream-fixes:  #42 ", "Upstream-Fixes") == ["#42"])
    expect("test_declared_numbers_list",
           declared_issue_numbers("#60168, #63885 #64777") == [60168, 63885, 64777])
    expect("test_declared_numbers_bare", declared_issue_numbers("1723") == [1723])
    expect("test_covers_multi_issue",
           check_covers_issue({"upstream": "anthropics/claude-code#60168, #63885"}, "anthropics/claude-code", 63885))
    expect("test_covers_wrong_repo",
           not check_covers_issue({"upstream": "Wei-Shaw/sub2api#1"}, "anthropics/claude-code", 1))

    cfg = {
        "triage": "triage.json", "fixes": "fixes.json", "fact_checks": "facts.json",
        "trailer": "Upstream-Fixes", "repo": "Wei-Shaw/sub2api",
    }

    def gate(root: Path, declared: list[int]) -> int:
        facts = engine.load_json(root / cfg["fact_checks"])
        fixes = engine.load_json(root / cfg["fixes"])
        triage = engine.load_json(root / cfg["triage"])
        return 1 if evaluate_declared(engine, cfg, facts, fixes, triage, declared) else 0

    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        anchored = root / "anchored.go"
        anchored.write_text("func X() { // PROVES-FIX-1723\n}\n", encoding="utf-8")
        engine.write_json(root / "triage.json", {"issues": [
            {"upstream": "Wei-Shaw/sub2api#1723", "impact": "high",
             "tokenkey_status": "candidate_unverified", "rationale": "x"}], "counts": {}})
        engine.write_json(root / "fixes.json", {"issues": []})
        engine.write_json(root / "facts.json", {"checks": [
            {"upstream": "Wei-Shaw/sub2api#1723", "tokenkey_pr": "x#9", "summary": "s",
             "fixed_by": ["anchored.go"], "fixed_if_all_present": [f"{anchored}:PROVES-FIX-1723"]}]})

        expect("test_gate_fails_before_apply", gate(root, [1723]) == 1)
        expect("test_apply_returns_0", cmd_apply(engine, cfg, root) == 0)
        fixes = json.load(open(root / "fixes.json"))
        triage = json.load(open(root / "triage.json"))
        expect("test_apply_adds_fixes_entry",
               any(i["upstream"] == "Wei-Shaw/sub2api#1723" for i in fixes["issues"]))
        expect("test_apply_reclassifies_triage",
               triage["issues"][0]["tokenkey_status"] == "fixed_in_tokenkey")
        expect("test_gate_passes_after_apply", gate(root, [1723]) == 0)
        expect("test_gate_fails_missing_factcheck", gate(root, [9999]) == 1)
        expect("test_no_trailer_passes", gate(root, []) == 0)

        anchored.write_text("func X() {}\n", encoding="utf-8")  # anchor rot
        expect("test_gate_fails_anchor_rot", gate(root, [1723]) == 1)

    total = 15
    print(f"apply-fix-ledger self-test ({total - len(failures)}/{total} cases passed)")
    return 1 if failures else 0


def main() -> int:
    parser = argparse.ArgumentParser(description="Reconcile / gate an issue-watchdog fix ledger.")
    parser.add_argument("--ledger", choices=sorted(LEDGERS.keys()))
    parser.add_argument("--apply", action="store_true", help="propagate fact-checks into fixes/triage")
    parser.add_argument("--check", action="store_true", help="trailer-scoped drift gate (needs --commits-range)")
    parser.add_argument("--commits-range", default="", help="git range, e.g. origin/main..HEAD")
    parser.add_argument("--root", type=Path, default=Path("."), help="repo root (default: .)")
    parser.add_argument("--quiet", action="store_true")
    parser.add_argument("--selftest", action="store_true")
    args = parser.parse_args()

    if args.selftest:
        return run_selftest()
    if not args.ledger:
        parser.error("--ledger is required unless --selftest")

    cfg = LEDGERS[args.ledger]
    engine = load_engine()
    root = args.root.resolve()

    if args.apply:
        return cmd_apply(engine, cfg, root)
    if args.check:
        if not args.commits_range:
            parser.error("--check requires --commits-range A..B")
        return cmd_check(engine, cfg, root, args.commits_range, args.quiet)
    parser.error("one of --apply / --check / --selftest is required")
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
