#!/usr/bin/env python3
"""approved-docs-index — keep the approved-doc index and its links honest.

Two recurring soft failures, hardened per CLAUDE.md §5 ("no soft rule without a
check"):

  1. INDEX COMPLETENESS. `docs/approved/README.md` is the entry point agents read
     to find the owning contract for a topic. A doc that exists but is not linked
     from the index is effectively invisible: the next agent re-derives the policy
     from code or from skill prose, which is how a second source of truth gets
     born. `dev-rules/scripts/check_approved_docs.py` validates the status
     vocabulary but not membership, so five approved docs had drifted out of the
     index before this gate existed.

  2. ANCHOR RESOLUTION. Cross-doc links carrying a `#fragment` silently rot when
     the target heading is reworded — CLAUDE.md pointed at a Model-serving-SSOT
     anchor for months after the heading changed. A dead anchor lands the reader
     at the top of a long reference file rather than at the owner section, so the
     pointer stops doing the one job it has.

Anchor slugs follow GitHub's algorithm: lowercase, strip everything that is not a
word character / space / hyphen (CJK is a word character), then spaces to hyphens.
Duplicate headings get GitHub's `-1`, `-2` … suffixes.

Exit: 0 ok, 1 gate fail, 2 error.
"""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

APPROVED_DIR = "docs/approved"
INDEX_REL = "docs/approved/README.md"

# Files whose intra-repo markdown links are checked for anchor resolution. These
# are the navigation surfaces agents actually follow; every approved doc is added
# on top of this list.
LINK_SOURCES = (
    "CLAUDE.md",
    "AGENTS.md",
    "docs/approved/README.md",
    "docs/global/agent-reference.md",
    "docs/global/upstream-merge-discipline.md",
)

# Index membership is about durable contracts. A doc parked as draft/pending is
# still being written and need not be linked yet; archived docs are deliberately
# out of the reading path.
INDEXED_STATUSES = {"approved", "shipped"}

LINK_RE = re.compile(r"\[[^\]]*\]\(([^)\s]+)\)")
HEADING_RE = re.compile(r"^(#{1,6})\s+(.*?)\s*$")
FENCE_RE = re.compile(r"^\s*(?:```|~~~)")
STATUS_RE = re.compile(r"^status:\s*(.*?)\s*$")


def status_of(path: Path) -> str:
    """Read the frontmatter `status:` field, or '' when there is no frontmatter."""
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError:
        return ""
    if not lines or lines[0].strip() != "---":
        return ""
    for line in lines[1:]:
        if line.strip() == "---":
            break
        match = STATUS_RE.match(line)
        if match:
            value = match.group(1)
            if len(value) >= 2 and value[0] == value[-1] and value[0] in {'"', "'"}:
                value = value[1:-1]
            return value.strip().lower()
    return ""


def slugify(heading: str) -> str:
    """Reproduce GitHub's heading-to-anchor transformation."""
    text = heading.strip().lower()
    # Backticks and asterisks are dropped; underscores are NOT — GitHub keeps them,
    # and headings here carry identifiers like `simple_release`.
    text = re.sub(r"[`*]", "", text)
    text = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", text)  # links keep their label
    text = re.sub(r"[^\w\s-]", "", text)        # drop punctuation, keep CJK
    return text.replace(" ", "-")


def anchors_of(path: Path) -> set[str]:
    """Collect the anchor slugs a markdown file exposes, honoring duplicates."""
    slugs: set[str] = set()
    seen: dict[str, int] = {}
    in_fence = False
    try:
        text = path.read_text(encoding="utf-8")
    except OSError:
        return slugs
    for line in text.splitlines():
        if FENCE_RE.match(line):
            in_fence = not in_fence
            continue
        if in_fence:
            continue
        match = HEADING_RE.match(line)
        if not match:
            continue
        base = slugify(match.group(2))
        if not base:
            continue
        count = seen.get(base, 0)
        slugs.add(base if count == 0 else f"{base}-{count}")
        seen[base] = count + 1
    return slugs


def approved_docs(root: Path) -> list[Path]:
    directory = root / APPROVED_DIR
    if not directory.is_dir():
        return []
    return sorted(p for p in directory.glob("*.md") if p.name != "README.md")


def check_index(root: Path) -> list[str]:
    index = root / INDEX_REL
    if not index.is_file():
        return [f"missing approved-doc index: {INDEX_REL}"]
    text = index.read_text(encoding="utf-8")
    linked = {
        target.split("#", 1)[0].rsplit("/", 1)[-1]
        for target in LINK_RE.findall(text)
    }
    errors: list[str] = []
    for doc in approved_docs(root):
        status = status_of(doc)
        if status and status not in INDEXED_STATUSES:
            continue
        if doc.name not in linked:
            errors.append(
                f"{INDEX_REL}: approved doc is not linked from the index: "
                f"{doc.name} (status: {status or 'none'}). An unindexed contract "
                "gets re-derived into a second source of truth."
            )
    return errors


def link_sources(root: Path) -> list[Path]:
    seen: set[Path] = set()
    out: list[Path] = []
    for relative in LINK_SOURCES:
        path = root / relative
        if path.is_file() and path.resolve() not in seen:
            seen.add(path.resolve())
            out.append(path)
    for doc in approved_docs(root):
        if doc.resolve() not in seen:
            seen.add(doc.resolve())
            out.append(doc)
    return out


def check_anchors(root: Path) -> list[str]:
    errors: list[str] = []
    anchor_cache: dict[Path, set[str]] = {}
    for source in link_sources(root):
        relative = source.relative_to(root).as_posix()
        text = source.read_text(encoding="utf-8")
        for match in LINK_RE.finditer(text):
            target = match.group(1)
            if target.startswith(("http://", "https://", "mailto:", "#")):
                continue
            path_part, _, fragment = target.partition("#")
            if not fragment or not path_part.endswith(".md"):
                continue
            resolved = (source.parent / path_part).resolve()
            if not resolved.is_file():
                continue  # missing-file links are the existing script-ref gate's job
            if resolved not in anchor_cache:
                anchor_cache[resolved] = anchors_of(resolved)
            if fragment not in anchor_cache[resolved]:
                line = text.count("\n", 0, match.start()) + 1
                errors.append(
                    f"{relative}:{line}: dead anchor `#{fragment}` in {path_part} "
                    "— the heading was reworded; update the link to the current slug."
                )
    return errors


def check(root: Path) -> list[str]:
    return check_index(root) + check_anchors(root)


def selftest() -> int:
    import tempfile

    failures: list[str] = []

    def expect(condition: bool, label: str) -> None:
        if not condition:
            failures.append(label)

    expect(slugify("Model serving SSOT / 模型交付 SSOT") == "model-serving-ssot--模型交付-ssot",
           "bilingual heading slug")
    expect(slugify("Studio SSOT (`/studio` Image / Video / BakeOff)")
           == "studio-ssot-studio-image--video--bakeoff", "punctuated heading slug")
    # HEADING_RE strips the leading #s, so slugify only ever sees the heading text.
    expect(slugify("9.1 `simple_release`") == "91-simple_release", "underscore kept in slug")

    with tempfile.TemporaryDirectory() as raw:
        root = Path(raw)
        approved = root / APPROVED_DIR
        approved.mkdir(parents=True)
        (approved / "owner.md").write_text(
            "---\nstatus: approved\n---\n\n# Owner\n\n## Output limit compatibility\n",
            encoding="utf-8",
        )
        (approved / "draft-thing.md").write_text(
            "---\nstatus: draft\n---\n\n# Draft\n", encoding="utf-8"
        )
        (approved / "README.md").write_text("# Index\n", encoding="utf-8")

        errors = check_index(root)
        expect(any("owner.md" in e for e in errors), "unindexed approved doc is reported")
        expect(not any("draft-thing.md" in e for e in errors), "draft doc is exempt")

        (approved / "README.md").write_text(
            "# Index\n\n- [owner](owner.md)\n", encoding="utf-8"
        )
        expect(check_index(root) == [], "linked approved doc passes")

        (root / "CLAUDE.md").write_text(
            "See [x](docs/approved/owner.md#output-limit-compatibility) and "
            "[y](docs/approved/owner.md#gone-heading).\n",
            encoding="utf-8",
        )
        errors = check_anchors(root)
        expect(any("gone-heading" in e for e in errors), "dead anchor is reported")
        expect(not any("output-limit-compatibility" in e for e in errors),
               "live anchor passes")

    if failures:
        for failure in failures:
            print(f"FAIL selftest: {failure}", file=sys.stderr)
        return 1
    print("ok: approved-docs-index selftests pass")
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
        print("ok: approved-doc index complete and doc anchors resolve")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
