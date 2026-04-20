#!/usr/bin/env python3
"""Validate frontmatter of every docs/approved/*.md.

Rules (Jobs minimalism + OPC automation; see dev-rules/product-dev.mdc §完成自检):
  R1. Frontmatter MUST exist.
  R2. status MUST be one of {draft, pending, shipped, archived}.
  R3. status == "pending" AND (related_prs OR related_commits) non-empty
      → "shipped under pending" smell. This is the same incident class as
      sticky-routing.md (created 2026-04-17, pending; commit a68dee5b shipped
      2026-04-18). Refuse to merge until status is flipped or PRs/commits
      removed from frontmatter.
  R4. status == "shipped" MUST list at least one of related_prs / related_commits.

Exit non-zero on any violation.
"""
from __future__ import annotations

import pathlib
import re
import sys

ALLOWED_STATUS = {"draft", "pending", "shipped", "archived"}
APPROVED_DIR = pathlib.Path("docs/approved")
FRONTMATTER_RE = re.compile(r"^---\s*\n(.*?\n)---\s*\n", re.DOTALL)


def parse_frontmatter(text: str) -> dict[str, str] | None:
    m = FRONTMATTER_RE.match(text)
    if not m:
        return None
    out: dict[str, str] = {}
    for line in m.group(1).splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if ":" not in line:
            continue
        k, _, v = line.partition(":")
        out[k.strip()] = v.strip()
    return out


def is_listish_nonempty(v: str | None) -> bool:
    if not v:
        return False
    s = v.strip()
    if s in ("[]", ""):
        return False
    return True


def check(path: pathlib.Path) -> list[str]:
    text = path.read_text(encoding="utf-8")
    fm = parse_frontmatter(text)
    if fm is None:
        return [f"{path}: missing frontmatter (--- ... ---) at file head"]
    errs: list[str] = []
    status = fm.get("status", "")
    if status not in ALLOWED_STATUS:
        errs.append(
            f"{path}: status='{status}' not in {sorted(ALLOWED_STATUS)}"
        )
    has_prs = is_listish_nonempty(fm.get("related_prs"))
    has_commits = is_listish_nonempty(fm.get("related_commits"))
    if status == "pending" and (has_prs or has_commits):
        errs.append(
            f"{path}: status=pending but related_prs/related_commits non-empty — "
            "did you ship code without flipping status to 'shipped'? "
            "See docs/preflight-debt.md for the 2026-04-18 sticky-routing incident."
        )
    if status == "shipped" and not (has_prs or has_commits):
        errs.append(
            f"{path}: status=shipped but no related_prs and no related_commits listed"
        )
    return errs


def main() -> int:
    if not APPROVED_DIR.exists():
        return 0
    errs: list[str] = []
    for p in sorted(APPROVED_DIR.glob("*.md")):
        errs.extend(check(p))
    if errs:
        sys.stderr.write("\n".join(errs) + "\n")
        sys.stderr.write(
            f"\n[preflight] approved-doc check FAILED ({len(errs)} issue(s))\n"
        )
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
