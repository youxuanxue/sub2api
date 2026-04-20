#!/usr/bin/env python3
"""Audit the agent-facing HTTP contract for sub2api.

Required by `dev-rules/agent-contract-enforcement.mdc` and the
`docs/approved/newapi-as-fifth-platform.md` §5.3 acceptance gate.

# Why this is an *audit* tool, not a *generator* (yet)

`docs/agent_integration.md` claims to be "Generated from live Gin route
registrations". A naive literal-path extractor over `routes/*.go` works
for routes registered on the top-level `r`, but Gin's nested
`router.Group("/admin").Group("/accounts")` pattern means many endpoints
appear in source as bare paths like `/:id` — a generator that does not
resolve group prefixes would *replace* the existing curated paths with
truncated ones, regressing the doc.

Doing prefix resolution properly requires either:

  1. A Go AST walker that follows `<grp> := <parent>.Group("/x")` chains
     across helper functions (`registerXxxRoutes(admin, h)`); or
  2. A runtime route dump from `gin.Engine.Routes()` after wiring the
     real handlers — needs Wire DI + stubs for every dependency.

Both are larger tasks than this PR is scoped for, and are tracked in
`docs/preflight-debt.md` (M7 follow-up: Go AST or runtime route dump).

# What this script DOES enforce today

Two cheap, high-signal contract guards that catch the regressions we
have actually seen:

  A) **Notes-section coverage**: every TokenKey first-class platform
     (the four upstream platforms + `newapi`) must be mentioned in the
     hand-maintained `# Agent Contract Notes` tail of the doc. This is
     the test that catches "we shipped a fifth platform but forgot to
     tell agents about it".

  B) **Route-count drift sanity**: count the literal `<ident>.METHOD(`
     registrations under `backend/internal/server/routes/*.go` and
     compare against the count of bulleted lines in the existing doc. Any large delta (default ±10%) prints a warning so
     the next maintainer regenerates the doc by hand. This is a
     soft signal, not a hard fail — the prefix-resolution debt makes
     hard-fail premature.

Usage::

    python3 scripts/export_agent_contract.py            # human report
    python3 scripts/export_agent_contract.py --check    # CI gate (exit 1
                                                        #   on Notes
                                                        #   coverage gap)

`--check` exits 1 only on the Notes coverage check (A); the count
warning (B) never blocks. This is intentional: contract docs lag by a
few PRs in healthy projects, and we do not want the gate so strict that
it becomes the thing devs route around. We reserve hard-fail for "doc
forgot a whole platform".
"""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path
from typing import Iterable

REPO_ROOT = Path(__file__).resolve().parent.parent
ROUTES_DIR = REPO_ROOT / "backend" / "internal" / "server" / "routes"
DOC_PATH = REPO_ROOT / "docs" / "agent_integration.md"

HTTP_VERBS = ("GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS")
ROUTE_PATTERN = re.compile(
    r"\b\w+\.(?:" + "|".join(HTTP_VERBS) + r")\("
)
HANDLE_PATTERN = re.compile(
    r'\b\w+\.Handle\(\s*"(?:' + "|".join(HTTP_VERBS) + r')"\s*,'
)
DOC_BULLET = re.compile(r"^- `(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS) ", re.MULTILINE)
NOTES_MARKER = "# Agent Contract Notes"

# TokenKey first-class platforms — the doc Notes section MUST mention each
# one so an agent reading the contract knows what gateway surface exists.
# Source of truth for the canonical names:
#   backend/internal/domain/constants.go (PlatformOpenAI, PlatformAnthropic,
#   PlatformGemini, PlatformAntigravity, PlatformNewAPI).
REQUIRED_PLATFORMS = ("openai", "anthropic", "gemini", "antigravity", "newapi")

COUNT_TOLERANCE = 0.10  # ±10% considered noise


def count_source_registrations() -> int:
    n = 0
    for go_file in sorted(ROUTES_DIR.rglob("*.go")):
        if go_file.name.endswith("_test.go"):
            continue
        text = go_file.read_text(encoding="utf-8")
        n += len(ROUTE_PATTERN.findall(text))
        n += len(HANDLE_PATTERN.findall(text))
    return n


def count_doc_bullets(doc: str) -> int:
    return len(DOC_BULLET.findall(doc))


def check_notes_coverage(doc: str, required: Iterable[str]) -> list[str]:
    """Return platforms missing from the `# Agent Contract Notes` tail.

    Pure substring match (case-insensitive) — we only require that the
    word appears somewhere in the Notes section. The point is "did the
    author at least acknowledge this platform exists in the contract?";
    deeper structure can come later.
    """
    idx = doc.find(NOTES_MARKER)
    notes = doc[idx:].lower() if idx >= 0 else ""
    missing: list[str] = []
    for name in required:
        if name.lower() not in notes:
            missing.append(name)
    return missing


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--check",
        action="store_true",
        help="exit 1 if any required platform is missing from the Notes section",
    )
    args = parser.parse_args()

    if not DOC_PATH.exists():
        sys.stderr.write(f"FAIL: {DOC_PATH.relative_to(REPO_ROOT)} not found\n")
        return 1

    doc = DOC_PATH.read_text(encoding="utf-8")
    src_count = count_source_registrations()
    doc_count = count_doc_bullets(doc)
    missing = check_notes_coverage(doc, REQUIRED_PLATFORMS)

    print(f"agent_integration.md  : {doc_count} HTTP route bullets")
    print(f"routes/*.go (source)  : {src_count} <ident>.METHOD(...) registrations")
    if doc_count == 0:
        delta_pct = 100.0
    else:
        delta_pct = abs(doc_count - src_count) / max(doc_count, 1) * 100
    if delta_pct > COUNT_TOLERANCE * 100:
        sys.stderr.write(
            f"WARN: doc/source route-count drift = {delta_pct:.1f}% "
            f"(>{COUNT_TOLERANCE*100:.0f}% tolerance). The Go-AST or runtime "
            f"route-dump generator (see docs/preflight-debt.md M7 follow-up) "
            f"would resolve this — for now, audit by hand if you added or "
            f"removed routes.\n"
        )

    if missing:
        sys.stderr.write(
            "FAIL: Notes section is missing required TokenKey platforms: "
            f"{', '.join(missing)}.\n"
            f"Edit {DOC_PATH.relative_to(REPO_ROOT)} (the `{NOTES_MARKER}` "
            "tail) so every first-class platform is acknowledged.\n"
        )
        return 1

    if args.check:
        print(f"OK: every required platform ({', '.join(REQUIRED_PLATFORMS)}) is in Notes.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
