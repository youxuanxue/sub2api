#!/usr/bin/env python3
"""Require ops tools to be discoverable from a maintained entry point.

Trace non-test references from skills, workflows, runbooks, preflight and
explicit standalone CLI exemptions. Test references and unrooted cycles do not
prove an operational consumer. Python imports may omit the .py extension.
This is a discoverability gate, not proof of production execution frequency.
"""
from __future__ import annotations

import os
import re
import subprocess
import sys
from functools import lru_cache
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]

# Files under these roots may legitimately reference an ops tool. Backend/
# frontend never call ops glue, so they are intentionally excluded (keeps the
# corpus small and fast).
CORPUS_ROOTS = [".cursor/skills", ".github/workflows", ".github/actions", "scripts", "ops", "deploy", "docs"]
CORPUS_ROOT_GLOBS = ["*.md"]  # repo-root docs (CLAUDE.md, README*.md)

# Tools are enumerated from here.
TOOL_GLOBS = ["ops/**/*.sh", "ops/**/*.py"]
_TEST_MARKERS = ("test_", "test-", "_test.")

# Legit zero-reference tools (basename -> reason). Forced classification:
# a stale key (now referenced, or no longer an ops tool) fails the check.
EXEMPT: dict[str, str] = {}


def _git(args: list[str]) -> list[str]:
    out = subprocess.run(["git", *args], cwd=REPO_ROOT, capture_output=True, text=True)
    if out.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)} failed: {out.stderr.strip()}")
    return [ln for ln in out.stdout.splitlines() if ln.strip()]


def is_tool(path: str) -> bool:
    b = os.path.basename(path)
    if any(m in b for m in _TEST_MARKERS):
        return False
    if "/tests/" in path or "/test/" in path:
        return False
    return True


def list_tools() -> list[str]:
    return sorted(
        p
        for p in _git(["ls-files", *TOOL_GLOBS])
        if is_tool(p) and (REPO_ROOT / p).is_file()
    )


def collect_corpus() -> dict[str, str]:
    files = set(_git(["ls-files", *CORPUS_ROOTS]))
    for g in CORPUS_ROOT_GLOBS:
        files.update(_git(["ls-files", g]))
    corpus: dict[str, str] = {}
    for f in files:
        try:
            corpus[f] = (REPO_ROOT / f).read_text(encoding="utf-8", errors="ignore")
        except OSError:
            corpus[f] = ""
    return corpus


def is_entry(path: str) -> bool:
    return (
        path.startswith((".cursor/skills/", ".github/workflows/", "docs/"))
        or ("/" not in path and path.endswith(".md"))
        or path == "scripts/preflight.sh"
    )


@lru_cache(maxsize=None)
def reference_matcher(path: str):
    """Compile once per target; the graph visits each target from many sources."""
    name = os.path.basename(path)
    if path.startswith(".github/actions/") and name in ("action.yml", "action.yaml"):
        directory = str(Path(path).parent)
        pattern = re.compile(r"(?<![\w/-])(?:\./)?" + re.escape(directory)
                             + r"(?:/" + re.escape(name) + r")?(?=[\s\"\'`)\]]|$)")
        return None, directory, pattern
    stem = Path(path).stem if path.endswith(".py") else ""
    pattern = re.compile(r"(?<![\w-])" + re.escape(stem) + r"(?![\w-])") if stem else None
    return name, stem, pattern


def references(content: str, path: str) -> bool:
    name, needle, pattern = reference_matcher(path)
    if name is None:  # Composite actions are referenced by their directory.
        return needle in content and bool(pattern.search(content))
    if name in content:
        return True
    # Fast literal rejection avoids a regex scan for every absent graph edge.
    return bool(pattern and needle in content and pattern.search(content))


def reachable_files(corpus: dict[str, str], entrypoints: set[str]) -> set[str]:
    candidates = {p: text for p, text in corpus.items() if is_tool(p)}
    reached = entrypoints & candidates.keys()
    pending = list(reached)
    while pending:
        source = pending.pop()
        for target in candidates.keys() - reached:
            if references(candidates[source], target):
                reached.add(target)
                pending.append(target)
    return reached


def scan(tools: list[str], corpus: dict[str, str], exempt: dict[str, str]):
    """Return unreachable tools and exemptions that are unnecessary or stale."""
    roots = {p for p in corpus if is_entry(p)}
    wired = reachable_files(corpus, roots)
    tool_basenames = {os.path.basename(t) for t in tools}
    stale = sorted(
        k for k in exempt
        if k not in tool_basenames
        or any(os.path.basename(t) == k and t in wired for t in tools)
        or not exempt[k].strip()
    )
    declared = {t for t in tools if os.path.basename(t) in exempt}
    reached = reachable_files(corpus, roots | declared) if declared - wired else wired
    return [t for t in tools if t not in reached], stale


def main() -> int:
    try:
        tools = list_tools()
        corpus = collect_corpus()
    except RuntimeError as exc:
        print(f"::error::{exc}", file=sys.stderr)
        return 2
    orphans, stale = scan(tools, corpus, EXEMPT)
    if orphans or stale:
        print("FAIL: ops/ tool orphan check", file=sys.stderr)
        for t in orphans:
            print(
                f"  - ORPHAN: {t} — no maintained non-test entry reaches this tool. Wire it into the owning "
                f"skill tool-table / workflow / preflight / sibling script, or add "
                f"its basename to EXEMPT with a reason.",
                file=sys.stderr,
            )
        for k in stale:
            print(
                f"  - STALE EXEMPT: '{k}' is now referenced or no longer an ops "
                f"tool; drop it from EXEMPT.",
                file=sys.stderr,
            )
        return 1
    print(f"ok: all {len(tools)} ops/ tool(s) wired (skill/workflow/script/deploy/doc); 0 orphans")
    return 0


def _selftest() -> int:
    import unittest

    class WiringTests(unittest.TestCase):
        def test_reference_boundaries_are_preserved(self):
            path = "ops/helper.py"  # script-ref-allow-missing: in-memory graph fixture
            for text in ("from helper import run", "helper.py", "prefix-helper.py"):
                self.assertTrue(references(text, path), text)
            for text in ("helper_extra", "prefix-helper", "helpers", "no matching module"):
                self.assertFalse(references(text, path), text)
            action = ".github/actions/maintain/action.yml"
            self.assertTrue(references("uses: ./.github/actions/maintain\n", action))
            self.assertFalse(references("uses: ./.github/actions/maintain-other\n", action))

        def test_rooted_transitive_import_is_discoverable(self):
            corpus = {
                "docs/ops.md": "run ops/entry.sh",  # script-ref-allow-missing: in-memory graph fixture
                "ops/entry.sh": "python3 worker.py",  # script-ref-allow-missing: in-memory graph fixture
                "ops/worker.py": "from helper import run",  # script-ref-allow-missing: in-memory graph fixture
                "ops/helper.py": "def run(): pass",  # script-ref-allow-missing: in-memory graph fixture
            }
            self.assertEqual(scan(list(corpus)[1:], corpus, {}), ([], []))

        def test_composite_action_directory_reaches_its_tools(self):
            corpus = {
                ".github/workflows/ci.yml": "uses: ./.github/actions/maintain\n",
                ".github/actions/maintain/action.yml": "run: ops/cleanup.sh",  # script-ref-allow-missing: in-memory graph fixture
                "ops/cleanup.sh": "true",  # script-ref-allow-missing: in-memory graph fixture
                ".github/actions/unused/action.yml": "run: ops/old.sh",  # script-ref-allow-missing: in-memory graph fixture
                "ops/old.sh": "true",  # script-ref-allow-missing: in-memory graph fixture
            }
            for reference in (
                "uses: ./.github/actions/maintain\n",
                "see .github/actions/maintain/action.yml\n",
                "see `.github/actions/maintain/action.yml`",
                "[action](.github/actions/maintain/action.yml)",
            ):
                corpus[".github/workflows/ci.yml"] = reference
                with self.subTest(reference=reference):
                    self.assertEqual(scan(["ops/cleanup.sh", "ops/old.sh"], corpus, {}),  # script-ref-allow-missing: in-memory graph fixture
                                     (["ops/old.sh"], []))  # script-ref-allow-missing: in-memory graph fixture

        def test_tests_and_mutual_mentions_do_not_make_tools_live(self):
            corpus = {
                "scripts/preflight.sh": "python3 ops/test_cycle.py",  # script-ref-allow-missing: in-memory graph fixture
                "ops/test_cycle.py": "import a",  # script-ref-allow-missing: in-memory graph fixture
                "ops/a.py": "import b",  # script-ref-allow-missing: in-memory graph fixture
                "ops/b.py": "import a",  # script-ref-allow-missing: in-memory graph fixture
            }
            self.assertEqual(scan(["ops/a.py", "ops/b.py"], corpus, {}),  # script-ref-allow-missing: in-memory graph fixture
                             (["ops/a.py", "ops/b.py"], []))  # script-ref-allow-missing: in-memory graph fixture

        def test_standalone_cli_roots_dependencies(self):
            corpus = {"ops/a.py": "import b", "ops/b.py": "pass"}  # script-ref-allow-missing: in-memory graph fixture
            self.assertEqual(scan(list(corpus), corpus, {"a.py": "recovery CLI"}), ([], []))

        def test_exemption_must_be_needed_and_nonempty(self):
            corpus = {"docs/ops.md": "a.py", "ops/a.py": "pass", "ops/b.py": "pass"}  # script-ref-allow-missing: in-memory graph fixture
            _, stale = scan(["ops/a.py", "ops/b.py"], corpus,  # script-ref-allow-missing: in-memory graph fixture
                            {"a.py": "old", "b.py": "", "gone.py": "removed"})
            self.assertEqual(stale, ["a.py", "b.py", "gone.py"])

        def test_self_mention_does_not_create_entry(self):
            corpus = {"ops/a.py": "a.py"}  # script-ref-allow-missing: in-memory graph fixture
            self.assertEqual(scan(list(corpus), corpus, {}), (["ops/a.py"], []))  # script-ref-allow-missing: in-memory graph fixture

    return 0 if unittest.TextTestRunner().run(
        unittest.defaultTestLoader.loadTestsFromTestCase(WiringTests)
    ).wasSuccessful() else 1


if __name__ == "__main__":
    if "--selftest" in sys.argv:
        raise SystemExit(_selftest())
    raise SystemExit(main())
