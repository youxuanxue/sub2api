#!/usr/bin/env python3
"""Guard one-way ownership across the pricing/serving approved designs."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path


MAIN = "docs/approved/pricing-serving-single-source-of-truth.md"
AVAILABILITY = "docs/approved/pricing-availability-source-of-truth.md"
PROTOCOL = "docs/approved/protocol-routing-ssot.md"
UNIVERSAL_DISCOVERY = "docs/approved/universal-key-capability-discovery.md"

TASK_CONTINUATION_DOCS = (MAIN,)

TASK_CONTINUATION_ANTIPATTERNS = {
    PROTOCOL: (
        re.compile(
            r"video[\s\S]{0,80}submit[\s\S]{0,40}fetch/status"
            r"[\s\S]{0,80}(?:同一个|same)[\s\S]{0,30}(?:RequestPlan|Plan)",
            re.IGNORECASE,
        ),
        re.compile(
            r"异步任务记录[\s\S]{0,80}不拥有[\s\S]{0,60}(?:route|路由)[\s\S]{0,30}选择",
            re.IGNORECASE,
        ),
    ),
    UNIVERSAL_DISCOVERY: (
        re.compile(
            r"video\s+fetch/status[\s\S]{0,100}RequestPlan\s+dry[ -]?planning",
            re.IGNORECASE,
        ),
        re.compile(
            r"(?:image|video)[\s\S]{0,50}"
            r"(?:status/result|fetch/status|status|result)"
            r"[\s\S]{0,100}RequestPlan\s+dry[ -]?planning",
            re.IGNORECASE,
        ),
    ),
}

SECONDARY_TRUTH_GLOBS = (
    "CLAUDE.md",
    "docs/**/*.md",
    "ops/**/*.md",
    "ops/**/*.sh",
    ".cursor/skills/**/*.md",
    "backend/**/*.go",
    "frontend/src/**/*.ts",
    "frontend/src/**/*.vue",
    "scripts/checks/*.py",
    "scripts/sentinels/*.json",
)

SECONDARY_TRUTH_SKIP = {
    "scripts/checks/pricing-serving-docs.py",
    "scripts/checks/test_pricing_serving_docs.py",
}

SECONDARY_TRUTH_PATTERNS = (
    re.compile(r"SINGLE\s+client-facing\s+servable\s+truth", re.IGNORECASE),
    re.compile(r"same\s+servable\s+surface", re.IGNORECASE),
    re.compile(r"visible\s*⟹\s*reachable\s*∧\s*priced", re.IGNORECASE),
    re.compile(r"truthful\s+callable\s+menus", re.IGNORECASE),
    re.compile(r"same\s+capability\s+SSOT\s+as\s+mapped\s+discovery", re.IGNORECASE),
    re.compile(r"SERVING\s+is\s+owned\s+by", re.IGNORECASE),
    re.compile(r"SERVING\s+由\s+per-account", re.IGNORECASE),
    re.compile(r"SERVING\s+fact\s+owned\s+by", re.IGNORECASE),
    re.compile(r"one\s+entry,\s+four\s+facts", re.IGNORECASE),
    re.compile(r"四事实"),
    re.compile(r"priced\s*⟺\s*servable", re.IGNORECASE),
    re.compile(r"一个能力\s*SSOT", re.IGNORECASE),
    re.compile(r"列出即可调用"),
    re.compile(r"servable\s*↔\s*priced\s*↔\s*display", re.IGNORECASE),
    re.compile(r"native\s+OpenAI\s+serving\s+truth", re.IGNORECASE),
    re.compile(r"public\s+serving\s+triple", re.IGNORECASE),
    re.compile(r"priced\+servable", re.IGNORECASE),
    re.compile(r"servable\+priced", re.IGNORECASE),
    re.compile(r"priced\s+and\s+(?:probe-)?servable", re.IGNORECASE),
    re.compile(r"priced\s*[∩]\s*"),
    re.compile(r"¬unreachable"),
    re.compile(r"unified\s+servable\s+(?:truth|SSOT|source|set|candidate)", re.IGNORECASE),
    re.compile(r"unified\s+client-facing\s+servable", re.IGNORECASE),
    re.compile(r"unified\s+Antigravity\s+SSOT", re.IGNORECASE),
    re.compile(r"unpriced\s+never\s+blocks", re.IGNORECASE),
    re.compile(r"display\s+when\s+priced", re.IGNORECASE),
    re.compile(r"四层洋葱"),
    re.compile(r"有价\s*\+\s*可服务"),
    re.compile(r"PRICE\s+事实"),
    re.compile(r"只.{0,12}两个事实"),
    re.compile(r"served\s*\+\s*priced", re.IGNORECASE),
    re.compile(r"priced\+displayable", re.IGNORECASE),
    re.compile(r"§2\.4\s*/\s*R-002"),
    re.compile(r"同一\s*servable\s+surface"),
    re.compile(r"account\.go:639"),
)

# Cheap literal cover of SECONDARY_TRUTH_PATTERNS. A file that cannot contain
# any of these needles cannot match a secondary-truth regex; skip the 40-pattern
# scan. Keep this list conservative: every pattern must retain at least one
# needle in any string it can match.
SECONDARY_TRUTH_PREFILTER = (
    "servable",
    "priced",
    "serving",
    "ssot",
    "四事实",
    "列出即可调用",
    "truthful",
    "unreachable",
    "unpriced",
    "四层",
    "有价",
    "price 事实",
    "两个事实",
    "r-002",
    "account.go:639",
    "four facts",
)


def secondary_truth_candidate(text: str) -> bool:
    lowered = text.lower()
    return any(needle in lowered for needle in SECONDARY_TRUTH_PREFILTER)


def iter_secondary_truth_files(root: Path) -> list[Path]:
    seen: set[Path] = set()
    files: list[Path] = []
    for pattern in SECONDARY_TRUTH_GLOBS:
        for path in root.glob(pattern):
            if not path.is_file():
                continue
            relative = path.relative_to(root).as_posix()
            if relative in SECONDARY_TRUTH_SKIP:
                continue
            resolved = path.resolve()
            if resolved in seen:
                continue
            seen.add(resolved)
            files.append(path)
    return files


def frontmatter(path: Path) -> dict[str, str]:
    text = path.read_text(encoding="utf-8")
    lines = text.splitlines()
    if not lines or lines[0].strip() != "---":
        raise ValueError("missing opening frontmatter delimiter")
    fields: dict[str, str] = {}
    for line in lines[1:]:
        if line.strip() == "---":
            return fields
        match = re.match(r"^([a-z_]+):\s*(.*)$", line)
        if match:
            value = match.group(2).strip()
            if len(value) >= 2 and value[0] == value[-1] and value[0] in {'"', "'"}:
                value = value[1:-1]
            fields[match.group(1)] = value
    raise ValueError("missing closing frontmatter delimiter")


def check(root: Path) -> list[str]:
    errors: list[str] = []
    docs: dict[str, dict[str, str]] = {}
    for relative in (MAIN, AVAILABILITY, PROTOCOL):
        path = root / relative
        if not path.is_file():
            errors.append(f"missing approved SSOT document: {relative}")
            continue
        try:
            docs[relative] = frontmatter(path)
        except ValueError as exc:
            errors.append(f"{relative}: {exc}")

    if len(docs) != 3:
        return errors
    for relative, fields in docs.items():
        if fields.get("status") != "approved":
            errors.append(f"{relative}: status must remain approved")

    main = docs[MAIN]
    availability = docs[AVAILABILITY]
    protocol = docs[PROTOCOL]
    availability_name = Path(AVAILABILITY).name
    main_prs = {int(value) for value in re.findall(r"\d+", main.get("related_prs", ""))}
    if 1821 not in main_prs:
        errors.append(f"{MAIN}: related_prs must link #1821")
    if availability_name not in main.get("supersedes", ""):
        errors.append(f"{MAIN}: supersedes must name {availability_name}")
    if availability.get("superseded_by") != MAIN:
        errors.append(f"{AVAILABILITY}: superseded_by must be {MAIN}")
    related_design = main.get("related_design", "")
    for required in (AVAILABILITY, PROTOCOL):
        if required not in related_design:
            errors.append(f"{MAIN}: related_design must include {required}")
    protocol_text = (root / PROTOCOL).read_text(encoding="utf-8")
    if MAIN in protocol_text:
        errors.append(
            f"{PROTOCOL}: delivery-to-protocol design linkage must remain one-way from {MAIN}"
        )
    for term in ("CatalogPolicy", "RequestPlan", "RuntimeReadiness", "TaskContinuation"):
        if term in protocol_text:
            errors.append(f"{PROTOCOL}: must not own delivery term {term}; that vocabulary stays in {MAIN}")
    formula = re.compile(
        r"CatalogPolicy\s*\+\s*RequestPlan\s*\+\s*RuntimeReadiness",
        re.IGNORECASE,
    )
    if formula.search(protocol_text):
        errors.append(f"{PROTOCOL}: must not copy the delivery formula owned by {MAIN}")
    availability_text = (root / AVAILABILITY).read_text(encoding="utf-8")
    if formula.search(availability_text):
        errors.append(f"{AVAILABILITY}: must not copy the delivery formula owned by {MAIN}")
    claude_text = (root / "CLAUDE.md").read_text(encoding="utf-8") if (root / "CLAUDE.md").is_file() else ""
    if formula.search(claude_text):
        errors.append(f"CLAUDE.md: must not copy the delivery formula owned by {MAIN}")

    for relative, fields in ((MAIN, main), (PROTOCOL, protocol)):
        if fields.get("aligned_origin_main"):
            errors.append(
                f"{relative}: point-in-time aligned_origin_main is forbidden in a durable approved design"
            )

    for relative in TASK_CONTINUATION_DOCS:
        text = (root / relative).read_text(encoding="utf-8")
        if "TaskContinuation" not in text:
            errors.append(f"{relative}: must define the TaskContinuation lifecycle boundary")

    for relative, patterns in TASK_CONTINUATION_ANTIPATTERNS.items():
        path = root / relative
        if not path.is_file():
            errors.append(f"missing task-continuation contract document: {relative}")
            continue
        text = path.read_text(encoding="utf-8")
        for pattern in patterns:
            match = pattern.search(text)
            if match:
                line = text.count("\n", 0, match.start()) + 1
                errors.append(
                    f"{relative}:{line}: task continuation (including video continuation) "
                    "must consume the canonical task record"
                )

    for path in iter_secondary_truth_files(root):
        relative = path.relative_to(root).as_posix()
        text = path.read_text(encoding="utf-8")
        if not secondary_truth_candidate(text):
            continue
        for pattern in SECONDARY_TRUTH_PATTERNS:
            match = pattern.search(text)
            if match:
                line = text.count("\n", 0, match.start()) + 1
                errors.append(
                    f"{relative}:{line}: secondary delivery-truth claim is forbidden: {match.group(0)}"
                )
    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--quiet", action="store_true")
    args = parser.parse_args()
    errors = check(args.root.resolve())
    if errors:
        for error in errors:
            print(f"FAIL: {error}", file=sys.stderr)
        return 1
    if not args.quiet:
        print("ok: pricing/serving approved-doc ownership is one-way")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
