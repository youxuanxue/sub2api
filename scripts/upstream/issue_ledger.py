#!/usr/bin/env python3
"""Read and validate curated watchdog evidence. Never writes generated snapshots."""
from __future__ import annotations

import argparse
import json
import re
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SOURCES = {
    "upstream": {"repo": "Wei-Shaw/sub2api", "trailer": "Upstream-Fixes",
                 "ledger": "ops/issue-watchdog/upstream.json"},
    "anthropic": {"repo": "anthropics/claude-code", "trailer": "Anthropic-Fixes",
                  "ledger": "ops/issue-watchdog/anthropic.json"},
}
IMPACTS = {"critical", "high", "needs_review", "needs_prod_validation", "medium", "fixed",
           "not_applicable", "low", "unknown_low_signal"}


def issue_refs(value: str, repo: str) -> set[str]:
    """Parse qualified refs and grouped shorthand, rejecting other repositories."""
    refs = set()
    for token in re.split(r"[,\s]+", value.strip()):
        match = re.fullmatch(r"(?:([\w.-]+/[\w.-]+)#|#?)([1-9][0-9]*)", token)
        if not match or (match[1] and match[1].lower() != repo.lower()):
            raise ValueError(f"invalid {repo} issue reference: {token!r}")
        refs.add(f"{repo}#{match[2]}")
    if not refs:
        raise ValueError("empty issue reference")
    return refs


def load_ledger(source: dict, root: Path = ROOT) -> list[dict]:
    path = root / source["ledger"]
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict) or data.get("version") != 1 or data.get("repo") != source["repo"]:
        raise ValueError(f"{path}: unsupported ledger version/repository")
    entries = data.get("entries")
    if not isinstance(entries, list):
        raise TypeError(f"{path}: entries must be a list")
    seen, judged = set(), set()
    for entry in entries:
        if not isinstance(entry, dict) or not isinstance(entry.get("upstream"), str):
            raise TypeError(f"{path}: each entry needs an upstream reference")
        ref = entry["upstream"]
        repo = re.escape(source["repo"])
        if not re.fullmatch(rf"{repo}#[1-9][0-9]*(?:,\s*(?:{repo})?#[1-9][0-9]*)*", ref):
            raise ValueError(f"{path}: upstream must start with a qualified ref; grouped refs need #numbers")
        refs = issue_refs(ref, source["repo"])
        if ref in seen:
            raise ValueError(f"{path}: duplicate entry {ref}")
        seen.add(ref)
        for key in ("summary", "tokenkey_pr", "severity", "status"):
            if key in entry and (not isinstance(entry[key], str) or not entry[key].strip()):
                raise ValueError(f"{ref}: {key} must be nonempty text")
        for key in ("fixed_by", "fixed_if_all_present"):
            if key in entry and (not isinstance(entry[key], list) or
                                 any(not isinstance(s, str) or not s.strip() for s in entry[key])):
                raise ValueError(f"{ref}: {key} must be a list of nonempty strings")
        for spec in entry.get("fixed_if_all_present", []):
            file, separator, needle = spec.partition(":")
            if not separator or not needle.strip() or Path(file).is_absolute() or ".." in Path(file).parts or not file:
                raise ValueError(f"{ref}: expected repository-relative path:needle: {spec!r}")
        if "judgment" in entry:
            judgment = entry["judgment"]
            if len(refs) != 1 or judged & refs:
                raise ValueError(f"{ref}: judgments must identify one unique issue")
            judged.update(refs)
            if (not isinstance(judgment, dict) or judgment.get("impact") not in IMPACTS or
                any(not isinstance(judgment.get(k), str) or not judgment[k].strip()
                    for k in ("tokenkey_status", "rationale"))):
                raise ValueError(f"{ref}: invalid judgment")
        if "history" in entry and (not isinstance(entry["history"], list) or
                                    any(not isinstance(h, dict) for h in entry["history"])):
            raise ValueError(f"{ref}: history must be a list of historical field differences")
    return entries


def declared_problems(entries: list[dict], source: dict, messages: str, root: Path) -> list[str]:
    declared = set()
    for match in re.finditer(rf"^\s*{source['trailer']}:([^\n]*)$", messages, re.IGNORECASE | re.MULTILINE):
        declared.update(issue_refs(match[1], source["repo"]))
    problems = []
    for ref in sorted(declared):
        matches = [entry for entry in entries if ref in issue_refs(entry["upstream"], source["repo"])
                   and "fixed_if_all_present" in entry]
        if not matches:
            problems.append(f"{ref}: no fix evidence in {source['ledger']}")
        for entry in matches:
            specs = entry["fixed_if_all_present"]
            if not specs:
                problems.append(f"{ref}: empty fixed_if_all_present")
            for spec in specs:
                file, needle = spec.split(":", 1)
                path = root / file
                if not path.is_file() or needle not in path.read_text(encoding="utf-8", errors="replace"):
                    problems.append(f"{ref}: missing anchor {spec}")
    return problems


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ledger", choices=SOURCES, help="default: both sources")
    parser.add_argument("--commits-range", help="also validate evidence declared by fix trailers")
    parser.add_argument("--root", type=Path, default=ROOT)
    parser.add_argument("--quiet", action="store_true")
    args = parser.parse_args()
    try:
        sources = [SOURCES[args.ledger]] if args.ledger else SOURCES.values()
        ledgers = [(source, load_ledger(source, args.root)) for source in sources]
        messages = ""
        if args.commits_range:
            result = subprocess.run(["git", "-C", str(args.root), "log", "--no-merges", "--format=%B",
                                     args.commits_range], capture_output=True, text=True, check=False)
            if result.returncode:
                return 2
            messages = result.stdout
        problems = [p for source, entries in ledgers
                    for p in declared_problems(entries, source, messages, args.root)]
        if problems:
            raise ValueError("\n".join(problems))
    except (OSError, ValueError, TypeError) as exc:
        print(f"FAIL: issue ledger: {exc}")
        return 1
    if not args.quiet:
        print("Issue ledgers valid; declared fix anchors resolve. No files written.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
