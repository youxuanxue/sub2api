#!/usr/bin/env python3
"""spec-delta-liveness — decide mechanically whether a spec delta is still alive.

`docs/spec-delta/README.md` has always said that completed one-off PR intent
notes are deleted after merge while living decisions stay. Nothing could apply
that rule, because "is this one still living?" was a judgement call, so in
practice nothing was ever deleted and the directory accumulated both kinds.

The cost of that gap is not untidiness. An agent auditing the tree for stale
files sees a spec delta that no grep hit mentions and concludes it is dead — the
exact inference that nearly deleted nine load-bearing documents, four of which
carry Owners tables that sentinels cite as the registered owner of a behavior.
What saved them was one reviewer deciding to open the files. That is the class of
"relied on remembering" this gate exists to remove (CLAUDE.md §5).

So liveness is computed, never asserted in prose:

  1. OWNERS TABLE. A delta that registers behavior owners is a contract: the
     paths in its Owners table are the declared single owner of that behavior.
     Every such path must exist. A dangling owner path means the doc now
     registers an owner that is gone — the doc is stale in a way that matters,
     because a sentinel or a future reader will still treat it as authoritative.
     Only the table rows are read. Prose that names a path is not a registration:
     under the §5.x deletion discipline a doc legitimately says "upstream moved
     this out of X", and X must not be mistaken for a declared owner.

  2. INBOUND REFERENCES. Sentinel rationales, Go comments, skills and workflows
     cite deltas by path. A cited delta is load-bearing regardless of its shape:
     deleting it breaks the citation. Citations are counted, and their existence
     is what makes the file live.

  3. TRUE STUBS. A delta with no Owners table AND no inbound citation is what the
     README's delete-after-merge sentence is about. Those are reported so the
     decision to keep or delete is deliberate and recorded, instead of the file
     sitting in the tree forever because nobody could tell which kind it was.

The two location forms (`docs/spec-delta-<slug>.md` at root per product-dev.mdc,
`docs/spec-delta/<topic>.md` for topics that outlived their PR) are both scanned.
This gate takes no position on which is correct — `product-dev.mdc` owns that.
It only answers, for whichever form a file uses, whether anything still depends
on it.

Note on scope: dangling owner paths fail the gate (assertion 1) because a
registered owner that does not exist is a broken contract. Stub inventory
(assertion 3) is reported, not failed: deleting a merged stub is a judgement the
gate informs rather than forces.

Exit: 0 ok, 1 gate fail, 2 error.
"""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

ROOT_GLOB = "docs/spec-delta-*.md"
DIR_REL = "docs/spec-delta"

# The index itself cites every delta by name; counting it would make every file
# look live and defeat assertion 3.
CITATION_BLIND = ("docs/spec-delta/README.md", "docs/README.md")

# Directories with no bearing on whether a doc is cited by the system.
SKIP_DIRS = {".git", "node_modules", "__pycache__", ".cache", "dist", "vendor"}

# An Owners table is introduced either by a heading or by the table header row
# itself. Both spellings in actual use are accepted. re.M is required: the marker
# is never on line 1, so without it these only ever matched the file start.
OWNERS_MARKER = re.compile(
    r"^#+\s*(?:Owners|Implementation\s*/\s*Owners|实现\s*/\s*Owners)\b"
    r"|^\|\s*(?:行为|Behavior|Behaviour)\s*\|"
    r"|^\|[^|\n]*\|\s*(?:唯一\s*owner|single owner|owner)\s*\|"
    r"|唯一\s*owner|single owner",
    re.I | re.M,
)

# A path-looking inline-code span: has a slash and a file-ish suffix.
CODE_PATH = re.compile(r"`([^`\n]*?/[^`\n]*?\.(?:go|ts|tsx|vue|py|sh|sql|json|ya?ml|md))`")


def _iter_text_files(root: Path):
    for path in root.rglob("*"):
        if not path.is_file():
            continue
        if any(part in SKIP_DIRS for part in path.parts):
            continue
        if path.stat().st_size > 2_000_000:
            continue
        yield path


def discover(root: Path) -> list[Path]:
    deltas = sorted(root.glob(ROOT_GLOB))
    dir_path = root / DIR_REL
    if dir_path.is_dir():
        deltas += sorted(p for p in dir_path.glob("*.md") if p.name != "README.md")
    return deltas


def owners_table_rows(text: str) -> list[str]:
    """The rows of a delta's Owners table, or [] if it has none.

    Only the table counts as a registration. Prose that merely names a path is
    not a contract, and under the §5.x deletion discipline prose routinely names
    files that upstream removed ("the logic moved out of X, we no longer carry
    it") — reading those as registered owners would fail the gate on documents
    that are telling the truth.
    """
    marker = OWNERS_MARKER.search(text)
    if not marker:
        return []
    lines = text[marker.start():].splitlines()
    rows: list[str] = []
    for line in lines:
        stripped = line.strip()
        if stripped.startswith("|"):
            rows.append(stripped)
        elif rows:
            # The table ended; a later unrelated table is not this one.
            break
    return rows


def owner_paths(text: str) -> list[str]:
    """Paths cited by a delta's Owners table, if it has one."""
    found: list[str] = []
    for row in owners_table_rows(text):
        for raw in CODE_PATH.findall(row):
            candidate = raw.strip()
            # Cells sometimes carry a `path.go` plus a member suffix.
            candidate = candidate.split("::")[0].split("#")[0].strip()
            if candidate and candidate not in found:
                found.append(candidate)
    return found


def count_citations(root: Path, delta: Path, corpus: dict[Path, str]) -> list[str]:
    rel = delta.relative_to(root).as_posix()
    name = delta.name
    citers: list[str] = []
    for path, text in corpus.items():
        crel = path.relative_to(root).as_posix()
        if crel == rel or crel in CITATION_BLIND:
            continue
        if rel in text or name in text:
            citers.append(crel)
    return sorted(citers)


def check(root: Path, quiet: bool = False) -> list[str]:
    deltas = discover(root)
    if not deltas:
        return ["no spec-delta documents found; expected at least one"]

    corpus = {}
    for path in _iter_text_files(root):
        try:
            corpus[path] = path.read_text(encoding="utf-8", errors="ignore")
        except OSError:
            continue

    errors: list[str] = []
    stubs: list[str] = []
    live: list[tuple[str, int, int]] = []

    for delta in deltas:
        rel = delta.relative_to(root).as_posix()
        text = corpus.get(delta, delta.read_text(encoding="utf-8", errors="ignore"))
        owners = owner_paths(text)
        citers = count_citations(root, delta, corpus)

        for owner in owners:
            if not (root / owner).exists():
                errors.append(
                    f"{rel}: Owners table registers '{owner}', which does not exist. "
                    "A registered owner that is gone is a broken contract: update the "
                    "table to the current owner, or remove the row if the behavior is gone."
                )

        if owners or citers:
            live.append((rel, len(owners), len(citers)))
        else:
            stubs.append(rel)

    if not quiet:
        for rel, n_owners, n_citers in live:
            bits = []
            if n_owners:
                bits.append(f"{n_owners} owner path(s)")
            if n_citers:
                bits.append(f"{n_citers} citation(s)")
            print(f"  live  {rel} — {', '.join(bits)}")
        for rel in stubs:
            print(
                f"  stub  {rel} — no Owners table and no citation; "
                "delete-after-merge candidate per docs/spec-delta/README.md"
            )
    return errors


def selftest() -> int:
    import tempfile

    failures: list[str] = []
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        (root / "docs" / "spec-delta").mkdir(parents=True)
        (root / "backend").mkdir()
        (root / "scripts").mkdir()

        # live by Owners table, owner exists
        (root / "backend" / "svc.go").write_text("package svc\n", encoding="utf-8")
        (root / "docs" / "spec-delta-ok.md").write_text(
            "# ok\n\n## Owners\n\n| 行为 | 唯一 owner |\n| --- | --- |\n"
            "| x | `backend/svc.go` |\n",
            encoding="utf-8",
        )
        # dangling owner -> must fail
        (root / "docs" / "spec-delta-dangling.md").write_text(
            "# d\n\n## Owners\n\n| 行为 | 唯一 owner |\n| --- | --- |\n"
            "| x | `backend/gone.go` |\n",
            encoding="utf-8",
        )
        # English mid-document heading + English table header, dangling owner.
        # Nothing here matches on line 1 and no "single owner" prose rescues it,
        # so this fixture is what holds OWNERS_MARKER's re.M and English
        # spellings honest -- the docstring claims both, and a regex that only
        # matched the file start silently classified this as a stub.
        (root / "docs" / "spec-delta-english.md").write_text(
            "# Edge model rejection\n\n## Owners\n\n| Behavior | Owner | Consumer |\n"
            "| --- | --- | --- |\n| verdict | `backend/also-gone.go` | gateway |\n",
            encoding="utf-8",
        )
        # Prose naming a path upstream deleted must NOT count as a registration:
        # the Owners table below registers an existing owner, so this doc is
        # clean even though the narrative mentions a file that is gone.
        (root / "docs" / "spec-delta-narrates.md").write_text(
            "# n\n\n## 背景\n\n上游把逻辑从 `backend/removed-upstream.go` 挪走了,我们不再持有它。\n\n"
            "## Owners\n\n| 行为 | 唯一 owner |\n| --- | --- |\n| x | `backend/svc.go` |\n",
            encoding="utf-8",
        )
        # true stub: no owners, no citation
        (root / "docs" / "spec-delta-stub.md").write_text("# s\n\nBackground only.\n", encoding="utf-8")
        # live by citation from a sentinel
        (root / "docs" / "spec-delta" / "cited.md").write_text("# c\n\nTopic.\n", encoding="utf-8")
        (root / "scripts" / "sentinel.json").write_text(  # script-ref-allow-missing: tempdir fixture
            '{"rationale": "see docs/spec-delta/cited.md."}\n', encoding="utf-8"
        )

        errors = check(root, quiet=True)
        joined = " ".join(errors)
        if "spec-delta-dangling.md" not in joined:
            failures.append("dangling owner path not reported")
        if "backend/gone.go" not in joined:
            failures.append("dangling owner path name missing from message")
        if "spec-delta-ok.md" in joined:
            failures.append("existing owner path wrongly reported")
        if "backend/also-gone.go" not in joined:
            failures.append("English mid-document Owners heading not recognized")
        if "backend/removed-upstream.go" in joined:
            failures.append("prose path outside the Owners table wrongly treated as a registration")
        if "spec-delta-narrates.md" in joined:
            failures.append("doc narrating an upstream deletion wrongly failed")
        if len(errors) != 2:
            failures.append(f"expected exactly 2 errors, got {len(errors)}: {errors}")

        # the narrating doc must still be live via its real Owners table
        narrates = owner_paths((root / "docs" / "spec-delta-narrates.md").read_text(encoding="utf-8"))
        if narrates != ["backend/svc.go"]:
            failures.append(f"owners table of narrating doc parsed wrong: {narrates}")

        # classification checks
        deltas = {p.name for p in discover(root)}
        expected = {
            "spec-delta-ok.md",
            "spec-delta-dangling.md",
            "spec-delta-english.md",
            "spec-delta-narrates.md",
            "spec-delta-stub.md",
            "cited.md",
        }
        if deltas != expected:
            failures.append(f"discovery wrong: {sorted(deltas)}")

        corpus = {p: p.read_text(encoding="utf-8", errors="ignore") for p in _iter_text_files(root)}
        cited_by = count_citations(root, root / "docs" / "spec-delta" / "cited.md", corpus)
        if cited_by != ["scripts/sentinel.json"]:  # script-ref-allow-missing: tempdir fixture
            failures.append(f"sentinel citation not detected: {cited_by}")
        if count_citations(root, root / "docs" / "spec-delta-stub.md", corpus):
            failures.append("stub wrongly reported as cited")
        if owner_paths((root / "docs" / "spec-delta-stub.md").read_text(encoding="utf-8")):
            failures.append("stub wrongly parsed as having owners")

    if failures:
        for failure in failures:
            print(f"FAIL selftest: {failure}", file=sys.stderr)
        return 1
    print("ok: spec-delta-liveness selftests pass")
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
        errors = check(args.root.resolve(), quiet=args.quiet)
    except OSError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2
    if errors:
        for error in errors:
            print(f"FAIL: {error}", file=sys.stderr)
        return 1
    if not args.quiet:
        print("ok: every spec-delta Owners path resolves")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
