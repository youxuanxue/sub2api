#!/usr/bin/env python3
"""
.testing/user-stories/verify_quality.py

Mechanically verifies user-story quality and Story <-> Test alignment per
dev-rules `test-philosophy.mdc` section 5.

Invoked by `dev-rules/templates/preflight.sh` check 5 (user story / test
alignment). Exits non-zero if any story violates structural or alignment
invariants. Satisfies `docs/preflight-debt.md` §5 (closed).

Output report:  .testing/user-stories/attachments/story-quality-report.md

Quality gates per test-philosophy.mdc section 5 ("Linked Tests rules"
and "drift detection"):

  * Required frontmatter fields: ID / Title / Priority / As a I want So that
    / Trace / Risk Focus.
  * Required sections:           Acceptance Criteria / Assertions /
                                 Linked Tests / Status.
  * Status must be one of:       Draft / Ready / InTest / Done / Archived.
  * Linked Tests must include >= 1 runnable command line
    (`运行命令:` / `Run command:` / `Run:` — matched anywhere in the block).
  * Status in {InTest, Done} additionally requires:
      - At least one concrete `path/to/file.go::TestFunc` reference;
      - Each non-`*(planned)*` reference points to a real file containing
        a `func TestFunc(` declaration;
      - Risk Focus declares >= 1 of the four risk classes
        (逻辑错误 / 行为回归 / 安全问题 / 运行时).

`*(planned)*` references are recognised as acknowledged gaps (see the
US-008/US-009/US-010 e2e tests tracked under preflight-debt.md section 4)
and are skipped from existence checks but counted in the report so they
don't quietly disappear.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path
from typing import List, Tuple

HERE = Path(__file__).resolve().parent              # .testing/user-stories/
PROJECT_ROOT = HERE.parent.parent                   # repo root
STORIES_DIR = HERE / "stories"
REPORT_PATH = HERE / "attachments" / "story-quality-report.md"

ALLOWED_STATUS = {"Draft", "Ready", "InTest", "Done", "Archived"}
STRICT_STATUS = {"InTest", "Done"}
PLANNED_MARKER = "*(planned)*"

REQUIRED_FIELDS = [
    "ID",
    "Title",
    "Priority",
    "As a / I want / So that",
    "Trace",
    "Risk Focus",
]
REQUIRED_SECTIONS = [
    "## Acceptance Criteria",
    "## Assertions",
    "## Linked Tests",
    "## Status",
]

# Accept Go test references (`path/to/file.go`::`TestFunc`) — Go func names
# are limited to identifier characters plus `/` (table-driven subtests like
# `TestSuite/TestCase`) and `*` (wildcard glob like `TestBackendMode*`).
LINKED_TEST_RE = re.compile(r"`([^`]+\.go)`::`([A-Za-z0-9_/*]+)`")
# Accept frontend Vitest references (`path/to/file.spec.ts`::`it/describe
# block name`). Vitest names are quoted strings and may contain spaces,
# punctuation, and Unicode — match anything inside the second
# backtick-pair as long as the file extension is `.ts`/`.tsx`.
LINKED_VITEST_RE = re.compile(r"`([^`]+\.tsx?)`::`([^`]+)`")
# Accept either list-item ("- 运行命令: ...") or section-header style
# ("运行命令：" followed by a code fence). Both convey "here is the command
# you can run to validate this story" — the format is cosmetic, presence
# is the semantic gate.
RUN_CMD_RE = re.compile(r"(?m)(运行命令|Run command|Run)\s*[:：]")
# Status line forms in the Status section:
#   "- Done" / "- Draft" (bare),
#   "- [x] Done" / "- [x] InTest" (checked task),
#   "- [ ] Draft" (unchecked task — e.g. US-018 prototype scope).
STATUS_LINE_RE = re.compile(
    r"^\s*-\s*(?:\[[^\]]*\]\s*)?([A-Za-z]+)",
    re.MULTILINE,
)
ID_RE = re.compile(r"^\s*-\s*ID:\s*(US-\d+)\s*$", re.MULTILINE)
GO_FUNC_RE_TEMPLATE = r"^func\s+{name}\s*\("


def parse_story(path: Path) -> dict:
    text = path.read_text(encoding="utf-8")
    issues: List[str] = []

    # Required field lines: `- <Field>:` anywhere
    for field in REQUIRED_FIELDS:
        pattern = rf"^\s*-\s*{re.escape(field)}\s*:"
        if not re.search(pattern, text, re.MULTILINE):
            issues.append(f"missing required field line: '- {field}:'")

    # Required sections
    for section in REQUIRED_SECTIONS:
        if section not in text:
            issues.append(f"missing required section: '{section}'")

    # Status (parse from the Status section only)
    status = None
    if "## Status" in text:
        status_block = text.split("## Status", 1)[1]
        m = STATUS_LINE_RE.search(status_block)
        if m:
            status = m.group(1)
    if not status:
        issues.append(
            "Status section missing or no status line "
            "(`- Draft`, `- [x] Done`, `- [ ] Draft`, …)"
        )
    elif status not in ALLOWED_STATUS:
        issues.append(
            f"invalid Status '{status}' (allowed: {sorted(ALLOWED_STATUS)})"
        )

    # ID
    id_match = ID_RE.search(text)
    story_id = id_match.group(1) if id_match else None

    # Linked Tests block
    linked_tests: List[Tuple[str, str, bool, str]] = []
    has_run_cmd = False
    if "## Linked Tests" in text:
        after = text.split("## Linked Tests", 1)[1]
        block = re.split(r"^## ", after, maxsplit=1, flags=re.MULTILINE)[0]
        has_run_cmd = bool(RUN_CMD_RE.search(block))
        for line in block.splitlines():
            m = LINKED_TEST_RE.search(line) or LINKED_VITEST_RE.search(line)
            if not m:
                continue
            file_path, func_name = m.group(1), m.group(2)
            planned = PLANNED_MARKER in line
            linked_tests.append((file_path, func_name, planned, line.strip()))

    # Risk Focus categories
    risk_block = ""
    risk_match = re.search(
        r"-\s*Risk Focus:\s*\n(.*?)(?=\n## |\n- [A-Z][A-Za-z ]+:|\Z)",
        text,
        re.DOTALL,
    )
    if risk_match:
        risk_block = risk_match.group(1)
    risk_categories = {
        "logic": "逻辑错误" in risk_block,
        "regression": "行为回归" in risk_block,
        "security": "安全问题" in risk_block,
        "runtime": "运行时" in risk_block,
    }

    return {
        "path": path,
        "id": story_id,
        "status": status,
        "issues": issues,
        "linked_tests": linked_tests,
        "has_run_cmd": has_run_cmd,
        "risk_categories": risk_categories,
    }


def func_exists_in_file(path: Path, func: str) -> bool:
    if not path.is_file():
        return False
    try:
        text = path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return False
    suffix = path.suffix.lower()
    if suffix in {".ts", ".tsx"}:
        # Vitest references quote the `it(...)` / `describe(...)` block name.
        # We do a literal substring match on the quoted text — the test name
        # is by definition a literal string, not a regex pattern.
        return func in text
    # Go: handle two notations beyond the bare identifier:
    #   - `TestSuite/TestCase` (table-driven subtest) — only the parent
    #     `func TestSuite(...)` is declared in source; the subtest name is
    #     a string passed to `t.Run`. Validate the parent exists.
    #   - `TestPrefix*` (wildcard glob over a family of tests) — validate
    #     at least one `func TestPrefix...` declaration exists.
    name = func
    if "/" in name:
        name = name.split("/", 1)[0]
    if name.endswith("*"):
        prefix = re.escape(name[:-1])
        pattern = rf"^func\s+{prefix}\w*\s*\("
    else:
        pattern = GO_FUNC_RE_TEMPLATE.format(name=re.escape(name))
    return re.search(pattern, text, re.MULTILINE) is not None


def verify_alignment(story: dict) -> List[str]:
    issues: List[str] = []
    status = story["status"]

    if any(
        msg == "missing required section: '## Linked Tests'"
        for msg in story["issues"]
    ):
        return issues  # parse_story already reported; skip duplicate alignment noise

    if not story["has_run_cmd"]:
        issues.append(
            "Linked Tests missing a runnable command line "
            "(expect '- 运行命令:' or '- Run command:' or '- Run:')"
        )

    if status in STRICT_STATUS:
        concrete = [t for t in story["linked_tests"] if not t[2]]
        if not concrete:
            issues.append(
                f"Status={status} requires at least one concrete "
                f"`file.go::Func` reference (excluding *(planned)* gaps)"
            )
        for file_path, func, planned, _raw in story["linked_tests"]:
            if planned:
                continue
            abs_path = PROJECT_ROOT / file_path
            if not abs_path.is_file():
                issues.append(f"linked test file not found: `{file_path}`")
                continue
            if not func_exists_in_file(abs_path, func):
                issues.append(
                    f"linked test function `{func}` not found in `{file_path}`"
                )
        if not any(story["risk_categories"].values()):
            issues.append(
                "Status=InTest/Done requires Risk Focus to declare >=1 of: "
                "逻辑错误/行为回归/安全问题/运行时"
            )

    return issues


def write_report(rows: List[Tuple[str, str, str]], total_issues: int, scanned: int) -> None:
    REPORT_PATH.parent.mkdir(parents=True, exist_ok=True)
    lines = [
        "# Story Quality Report",
        "",
        "Generated by `.testing/user-stories/verify_quality.py`",
        "(invoked from `dev-rules/templates/preflight.sh` check 5).",
        "",
        f"- Stories scanned: {scanned}",
        f"- Total issues:    {total_issues}",
        "",
        "| Story | Status | Issue |",
        "| ----- | ------ | ----- |",
    ]
    for name, status, msg in rows:
        msg_escaped = msg.replace("|", "\\|")
        lines.append(f"| `{name}` | {status} | {msg_escaped} |")
    REPORT_PATH.write_text("\n".join(lines) + "\n", encoding="utf-8")


def main() -> int:
    if not STORIES_DIR.is_dir():
        print(
            f"verify_quality.py: stories dir not found: {STORIES_DIR}",
            file=sys.stderr,
        )
        return 2

    stories = sorted(STORIES_DIR.glob("US-*.md"))
    if not stories:
        print(
            f"verify_quality.py: no stories found in {STORIES_DIR}",
            file=sys.stderr,
        )
        return 2

    rows: List[Tuple[str, str, str]] = []
    total_issues = 0
    for path in stories:
        try:
            story = parse_story(path)
        except Exception as exc:  # defensive: do not let one story crash all
            print(
                f"FAIL: {path.name}: parse error: {exc}", file=sys.stderr
            )
            total_issues += 1
            rows.append((path.name, "ERROR", f"parse error: {exc}"))
            continue
        issues = list(story["issues"]) + verify_alignment(story)
        status_label = story.get("status") or "?"
        if issues:
            total_issues += len(issues)
            for issue in issues:
                print(f"FAIL: {path.name}: {issue}", file=sys.stderr)
                rows.append((path.name, status_label, issue))
        else:
            rows.append((path.name, status_label, "ok"))

    write_report(rows, total_issues, len(stories))

    if total_issues:
        print(
            f"\nverify_quality.py: {total_issues} issue(s) across "
            f"{len(stories)} stor{'y' if len(stories)==1 else 'ies'}; "
            f"report at {REPORT_PATH.relative_to(PROJECT_ROOT)}",
            file=sys.stderr,
        )
        return 1
    print(
        f"verify_quality.py: PASS ({len(stories)} stories, 0 issues)"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
