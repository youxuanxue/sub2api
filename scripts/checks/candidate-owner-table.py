#!/usr/bin/env python3
"""candidate-owner-table — one enumeration of the candidate-eligibility owners.

The candidate SSOT owner list existed in three places (root `CLAUDE.md`, root
`AGENTS.md`, and the approved contract). All three drifted: the two root files
named six owners while twenty `candidate_*.go` production files existed, and the
missing ones included the single HTTP ingress (`candidate_ingress_tk.go`) and the
profit gate. A copied list is the failure mode — every copy is a second source of
truth that ages independently — so this gate enforces two rules:

  1. SINGLE ENUMERATION. Only `docs/approved/candidate-eligibility-ssot.md` may
     enumerate the owners. The root navigation files must point at that table and
     name at most one owner file inline (a search anchor is fine, a roster is not).

  2. TABLE COMPLETENESS. Every production `backend/internal/service/candidate_*.go`
     must appear in that contract, so a new candidate entry cannot ship without
     declaring which fact it owns and what its consumers may assume.

Test files are excluded: they follow their owner and are pinned by
`scripts/sentinels/gateway-tk.json` plus US-050.

Exit: 0 ok, 1 gate fail, 2 error.
"""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

CONTRACT_REL = "docs/approved/candidate-eligibility-ssot.md"
OWNER_DIR = "backend/internal/service"
OWNER_GLOB = "candidate_*.go"

# Root navigation files must delegate rather than re-list. One inline owner name is
# allowed so prose can still cite a concrete search anchor.
POINTER_FILES = ("CLAUDE.md", "AGENTS.md")
MAX_INLINE_OWNERS = 1

OWNER_MENTION_RE = re.compile(r"`?(candidate_[a-z0-9_]+\.go)`?")


def production_owners(root: Path) -> list[str]:
    directory = root / OWNER_DIR
    if not directory.is_dir():
        return []
    return sorted(
        p.name for p in directory.glob(OWNER_GLOB) if not p.name.endswith("_test.go")
    )


def check(root: Path) -> list[str]:
    errors: list[str] = []
    contract = root / CONTRACT_REL
    if not contract.is_file():
        return [f"missing candidate SSOT contract: {CONTRACT_REL}"]
    contract_text = contract.read_text(encoding="utf-8")

    owners = production_owners(root)
    if not owners:
        return [f"no {OWNER_GLOB} production files found under {OWNER_DIR}"]

    for owner in owners:
        if owner not in contract_text:
            errors.append(
                f"{CONTRACT_REL}: candidate owner `{owner}` is not declared in the "
                "contract. Add a row to §Implementation/Owners stating the fact it "
                "owns and its consumer contract."
            )

    for relative in POINTER_FILES:
        path = root / relative
        if not path.is_file():
            continue
        text = path.read_text(encoding="utf-8")
        if CONTRACT_REL not in text:
            errors.append(
                f"{relative}: must link {CONTRACT_REL} as the candidate owner table."
            )
        mentioned = {
            name
            for name in OWNER_MENTION_RE.findall(text)
            if name.endswith(".go") and not name.endswith("_test.go")
        }
        if len(mentioned) > MAX_INLINE_OWNERS:
            errors.append(
                f"{relative}: re-enumerates {len(mentioned)} candidate owners "
                f"({', '.join(sorted(mentioned))}). Keep the roster only in "
                f"{CONTRACT_REL} and leave a pointer here — three copies of this "
                "list already drifted apart once."
            )
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
        (service / "candidate_request_tk.go").write_text("package service\n", encoding="utf-8")
        (service / "candidate_ingress_tk.go").write_text("package service\n", encoding="utf-8")
        (service / "candidate_request_tk_test.go").write_text("package service\n", encoding="utf-8")
        contract = root / CONTRACT_REL
        contract.parent.mkdir(parents=True, exist_ok=True)
        contract.write_text("| x | `candidate_request_tk.go` | y |\n", encoding="utf-8")
        for relative in POINTER_FILES:
            (root / relative).write_text(
                f"See [c]({CONTRACT_REL}); `candidate_request_tk.go` is the entry.\n",
                encoding="utf-8",
            )

        errors = check(root)
        expect(any("candidate_ingress_tk.go" in e for e in errors),
               "undeclared owner is reported")
        expect(not any("candidate_request_tk_test.go" in e for e in errors),
               "test files are exempt")

        contract.write_text(
            "| x | `candidate_request_tk.go`, `candidate_ingress_tk.go` | y |\n",
            encoding="utf-8",
        )
        expect(check(root) == [], "complete contract with pointer files passes")

        (root / "CLAUDE.md").write_text(
            f"See [c]({CONTRACT_REL}). Owners: `candidate_request_tk.go`, "
            "`candidate_ingress_tk.go`.\n",
            encoding="utf-8",
        )
        expect(any("re-enumerates" in e for e in check(root)),
               "duplicated roster is reported")

        (root / "CLAUDE.md").write_text("No pointer here.\n", encoding="utf-8")
        expect(any("must link" in e for e in check(root)), "missing pointer is reported")

    if failures:
        for failure in failures:
            print(f"FAIL selftest: {failure}", file=sys.stderr)
        return 1
    print("ok: candidate-owner-table selftests pass")
    return 0


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
    if errors:
        for error in errors:
            print(f"FAIL: {error}", file=sys.stderr)
        return 1
    if not args.quiet:
        print("ok: candidate owners are declared once in the approved contract")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
