#!/usr/bin/env python3
"""Guard the candidate owner table and its navigation/evidence boundaries.

Only §Implementation/Owners declares production candidate owners. Mentions in
examples, prose or historical evidence cannot satisfy a missing owner row.
Navigation files link the table; dated release and acceptance evidence lives in
US-050 instead of being copied into the policy contract.
"""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

CONTRACT_REL = "docs/approved/candidate-eligibility-ssot.md"
STORY_REL = ".testing/user-stories/stories/US-050-candidate-eligibility-ssot.md"
OWNER_DIR = "backend/internal/service"
OWNER_GLOB = "candidate_*.go"
POINTER_FILES = ("CLAUDE.md", "AGENTS.md", "docs/approved/README.md")
MAX_INLINE_OWNERS = 1
OWNER_MENTION_RE = re.compile(r"\b(candidate_[a-z0-9_]+\.go)\b")
RELEASE_HEADING_RE = re.compile(r"^#{2,4}\s+Release status\b.*$", re.MULTILINE)


def section(text: str, title: str, level: int) -> str:
    match = re.search(rf"^#{{{level}}}\s+{re.escape(title)}\s*$", text, re.MULTILINE)
    if not match:
        return ""
    body = text[match.end():]
    end = re.search(rf"^#{{1,{level}}}\s+\S", body, re.MULTILINE)
    return body[:end.start()] if end else body


def production_owners(root: Path) -> set[str]:
    return {
        path.name for path in (root / OWNER_DIR).glob(OWNER_GLOB)
        if not path.name.endswith("_test.go")
    }


def check(root: Path) -> list[str]:
    contract = root / CONTRACT_REL
    if not contract.is_file():
        return [f"missing candidate SSOT contract: {CONTRACT_REL}"]
    text = contract.read_text(encoding="utf-8")
    owners = production_owners(root)
    if not owners:
        return [f"no {OWNER_GLOB} production files found under {OWNER_DIR}"]

    errors: list[str] = []
    table = section(section(text, "Implementation", 2), "Owners", 3)
    declared: set[str] = set()
    for line in table.splitlines():
        cells = [cell.strip() for cell in line.strip().strip("|").split("|")]
        if not line.strip().startswith("|") or len(cells) != 3:
            continue
        names = set(OWNER_MENTION_RE.findall(cells[1]))
        if not names:
            continue
        if not cells[0] or not cells[2]:
            errors.append(f"{CONTRACT_REL}: owner row must state its fact and consumer contract: {line}")
            continue
        duplicates = names & declared
        if duplicates:
            errors.append(f"{CONTRACT_REL}: duplicate owner rows: {', '.join(sorted(duplicates))}")
        declared.update(names)

    for owner in sorted(owners - declared):
        errors.append(
            f"{CONTRACT_REL}: candidate owner `{owner}` is not declared in "
            "§Implementation/Owners. Add its fact and consumer contract to that table."
        )
    for owner in sorted(declared - owners):
        errors.append(f"{CONTRACT_REL}: owner table references non-production owner `{owner}`")

    # An evidence pointer replaces mutable deployment claims in the contract.
    if RELEASE_HEADING_RE.search(text):
        errors.append(f"{CONTRACT_REL}: move Release status evidence to {STORY_REL}#status")
    for anchor in ("status", "coverage-boundaries"):
        if f"{STORY_REL}#{anchor}" not in text:
            errors.append(f"{CONTRACT_REL}: must link {STORY_REL}#{anchor}")
    story = root / STORY_REL
    story_text = story.read_text(encoding="utf-8") if story.is_file() else ""
    for title, level in (("Status", 2), ("Coverage Boundaries", 3)):
        if not section(story_text, title, level).strip():
            errors.append(f"{STORY_REL}: missing or empty {title} evidence section")

    for relative in POINTER_FILES:
        path = root / relative
        if not path.is_file():
            continue
        nav = path.read_text(encoding="utf-8")
        # The approved index links sibling contracts; root files use full paths.
        target = Path(CONTRACT_REL).name if relative == "docs/approved/README.md" else CONTRACT_REL
        if target not in nav:
            errors.append(f"{relative}: must link {CONTRACT_REL} as the candidate owner table")
        mentioned = {name for name in OWNER_MENTION_RE.findall(nav) if not name.endswith("_test.go")}
        if len(mentioned) > MAX_INLINE_OWNERS:
            errors.append(f"{relative}: re-enumerates candidate owners; keep the roster only in {CONTRACT_REL}")
    return errors


def selftest() -> int:
    import tempfile

    failures: list[str] = []

    def expect(condition: bool, label: str) -> None:
        if not condition:
            failures.append(label)

    with tempfile.TemporaryDirectory() as raw:
        root = Path(raw)
        service = root / OWNER_DIR
        service.mkdir(parents=True)
        for name in ("candidate_request_tk.go", "candidate_ingress_tk.go", "candidate_request_tk_test.go"):
            (service / name).write_text("package service\n", encoding="utf-8")
        contract = root / CONTRACT_REL
        contract.parent.mkdir(parents=True)
        story = root / STORY_REL
        story.parent.mkdir(parents=True)
        story.write_text("### Coverage Boundaries\n\nLive acceptance pending.\n\n## Status\n\nInTest.\n", encoding="utf-8")
        for relative in POINTER_FILES:
            (root / relative).write_text(f"See [contract]({CONTRACT_REL}).\n", encoding="utf-8")
        rows = (
            "| Selection | `candidate_request_tk.go` | Shared request state |\n"
            "| Ingress | `candidate_ingress_tk.go` | Shared parsing |\n"
        )
        prefix = f"See [status]({STORY_REL}#status) and [coverage]({STORY_REL}#coverage-boundaries).\n"
        heading = "\n## Implementation\n\n### Owners\n\n"
        valid = prefix + heading + rows
        contract.write_text(valid, encoding="utf-8")
        expect(check(root) == [], "complete owner table passes; tests are exempt")

        # This was the real false green: a release table could hide a missing row.
        contract.write_text(prefix + heading + rows.splitlines()[0] + "\n\n### History\n\n" + rows, encoding="utf-8")
        expect(any("candidate_ingress_tk.go` is not declared" in e for e in check(root)), "history cannot satisfy owner registration")
        contract.write_text(prefix + heading + "Prose mentions candidate_request_tk.go and candidate_ingress_tk.go.\n", encoding="utf-8")
        expect(sum("is not declared" in e for e in check(root)) == 2, "prose is not an owner row")
        contract.write_text(prefix + "\n## Other\n\n### Owners\n\n" + rows, encoding="utf-8")
        expect(sum("is not declared" in e for e in check(root)) == 2, "only Implementation owns the table")
        contract.write_text(valid.replace("| Shared parsing |", "| |"), encoding="utf-8")
        expect(any("fact and consumer" in e for e in check(root)), "empty consumer contract fails")
        contract.write_text(valid + rows, encoding="utf-8")
        expect(any("duplicate owner" in e for e in check(root)), "duplicate registration fails")
        contract.write_text(valid + "| Retired | `candidate_retired.go` | Removed |\n", encoding="utf-8")
        expect(any("non-production owner" in e for e in check(root)), "retired owner fails")
        contract.write_text(valid + "\n### Release status\n\nAll owners shipped.\n", encoding="utf-8")
        expect(any("move Release status" in e for e in check(root)), "release status cannot be copied into contract")
        contract.write_text(heading + rows, encoding="utf-8")
        expect(sum("must link" in e for e in check(root)) == 2, "both evidence pointers are required")
        contract.write_text(valid, encoding="utf-8")
        story.write_text("## Status\n\nInTest.\n", encoding="utf-8")
        expect(any("Coverage Boundaries" in e for e in check(root)), "dangling evidence pointer fails")
        story.write_text("### Coverage Boundaries\n\nPending.\n\n## Status\n\nInTest.\n", encoding="utf-8")
        for relative in POINTER_FILES:
            nav = root / relative
            nav.write_text(f"See {CONTRACT_REL}. candidate_request_tk.go, candidate_ingress_tk.go.\n", encoding="utf-8")
            expect(any(relative in e and "re-enumerates" in e for e in check(root)), f"{relative} cannot copy roster")
            nav.write_text("No pointer.\n", encoding="utf-8")
            expect(any(relative in e and "must link" in e for e in check(root)), f"{relative} must link table")
            nav.write_text(f"See {CONTRACT_REL}.\n", encoding="utf-8")
        expect(check(root) == [], "restoring canonical ownership passes")

    for failure in failures:
        print(f"FAIL selftest: {failure}", file=sys.stderr)
    if not failures:
        print("ok: candidate-owner-table selftests pass")
    return int(bool(failures))


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--quiet", action="store_true")
    parser.add_argument("--selftest", action="store_true")
    args = parser.parse_args()
    if args.selftest:
        return selftest()
    try:
        errors = check(args.root.resolve())
    except OSError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2
    for error in errors:
        print(f"FAIL: {error}", file=sys.stderr)
    if not errors and not args.quiet:
        print("ok: candidate table, navigation and acceptance evidence have separate owners")
    return int(bool(errors))


if __name__ == "__main__":
    raise SystemExit(main())
