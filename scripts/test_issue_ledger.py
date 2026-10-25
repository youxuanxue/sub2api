"""Curated evidence validation and trailer gates require no generated caches."""
import copy
import importlib.util
import json
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location(
    "issue_ledger", Path(__file__).parent / "upstream/issue_ledger.py")
mod = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(mod)


class IssueLedgerTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.source = mod.SOURCES["upstream"]
        self.entry = {"upstream": self.source["repo"] + "#1, #2",
                      "summary": "A recorded fix", "fixed_if_all_present": ["fixed.py:proved"]}
        (self.root / "fixed.py").write_text("proved", encoding="utf-8")

    def write_ledger(self, entries):
        path = self.root / self.source["ledger"]
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps({"version": 1, "repo": self.source["repo"], "entries": entries}),
                        encoding="utf-8")
        return path

    def test_declared_fix_checks_grouped_refs_without_cache_or_writes(self):
        path = self.write_ledger([self.entry])
        before = path.read_bytes()
        entries = mod.load_ledger(self.source, self.root)
        problems = mod.declared_problems(entries, self.source,
                                        "Upstream-Fixes: #1, 2\nupstream-fixes: Wei-Shaw/sub2api#1", self.root)
        self.assertEqual(problems, [])
        self.assertEqual(path.read_bytes(), before)
        self.assertEqual(sorted(str(p.relative_to(self.root)) for p in self.root.rglob('*') if p.is_file()),
                         ["fixed.py", self.source["ledger"]])

    def test_missing_empty_and_rotted_evidence_fail_declared_fix(self):
        for entries in ([], [{"upstream": self.entry["upstream"]}],
                        [{**self.entry, "fixed_if_all_present": []}],
                        [{**self.entry, "fixed_if_all_present": ["fixed.py:missing"]}]):
            with self.subTest(entries=entries):
                self.assertTrue(mod.declared_problems(entries, self.source, "Upstream-Fixes: #2", self.root))
                self.assertEqual(mod.declared_problems(entries, self.source, "unrelated change", self.root), [])

    def test_wrong_repo_and_malformed_trailers_fail_instead_of_matching_number(self):
        for ref in ("anthropics/claude-code#1", "bad/Wei-Shaw/sub2api#1", "#1 garbage", "", "0"):
            with self.subTest(ref=ref), self.assertRaises(ValueError):
                mod.declared_problems([self.entry], self.source, "Upstream-Fixes: " + ref, self.root)
        self.assertEqual(mod.declared_problems([self.entry], self.source,
                                              "Anthropic-Fixes: anthropics/claude-code#1", self.root), [])

    def test_invalid_schema_and_duplicate_judgments_fail(self):
        variants = [None, {}, [None], [dict(self.entry, upstream="#1")],
                    [dict(self.entry, upstream=self.source["repo"] + "#1, 2")], [dict(self.entry, upstream="other/repo#1")],
                    [self.entry, self.entry], [dict(self.entry, fixed_if_all_present="fixed.py:proved")],
                    [dict(self.entry, fixed_if_all_present=["fixed.py:"])],
                    [dict(self.entry, fixed_if_all_present=["../fixed.py:proved"])],
                    [dict(self.entry, judgment={"impact": "typo"})]]
        for entries in variants:
            with self.subTest(entries=entries), self.assertRaises((ValueError, TypeError)):
                self.write_ledger(entries)
                mod.load_ledger(self.source, self.root)

    def test_unknown_judgment_status_fails_instead_of_defaulting(self):
        """The tracking Issue is filtered on these status sets, so a status nobody
        classified must fail here rather than silently default into one bucket and
        change what the queue shows."""
        judged = dict(self.entry, upstream=self.source["repo"] + "#1")
        for status in ("mostly_ok", "", "FIXED_IN_TOKENKEY", "unresolved"):
            with self.subTest(status=status), self.assertRaises(ValueError):
                self.write_ledger([dict(judged, judgment={
                    "impact": "medium", "tokenkey_status": status, "rationale": "why"})])
                mod.load_ledger(self.source, self.root)

        # Every status the real ledgers and the classifiers emit must stay loadable,
        # and each must belong to exactly one bucket.
        self.assertFalse(mod.QUEUE_STATUSES & mod.SETTLED_STATUSES)
        self.assertFalse(mod.QUEUE_STATUSES & mod.KEYWORD_STATUSES)
        self.assertFalse(mod.SETTLED_STATUSES & mod.KEYWORD_STATUSES)
        for status in sorted(mod.JUDGMENT_STATUSES):
            with self.subTest(status=status):
                self.write_ledger([dict(judged, judgment={
                    "impact": "medium", "tokenkey_status": status, "rationale": "why"})])
                entries = mod.load_ledger(self.source, self.root)
                self.assertEqual(entries[0]["judgment"]["tokenkey_status"], status)

    def test_shipped_ledgers_and_classifier_statuses_are_all_classified(self):
        """An allowlist that omits a status the repository actually uses would fail
        closed on the real ledgers, so pin both the shipped judgments and every
        status the keyword classifiers can emit."""
        for source in mod.SOURCES.values():
            for entry in mod.load_ledger(source):
                status = (entry.get("judgment") or {}).get("tokenkey_status")
                if status is not None:
                    self.assertIn(status, mod.JUDGMENT_STATUSES, entry["upstream"])
        emitted = set()
        for name in ("upstream/issue-triage-generate.py", "anthropic/cc-issue-triage-generate.py"):
            text = (Path(__file__).parent / name).read_text(encoding="utf-8")
            emitted |= set(re.findall(r'status = "([a-z_]+)"', text))
        self.assertTrue(emitted)
        self.assertLessEqual(emitted, mod.KEYWORD_STATUSES)

    def test_cli_fails_missing_ledger_and_uses_requested_root_for_anchors(self):
        command = [sys.executable, str(Path(mod.__file__)), "--root", str(self.root), "--ledger", "upstream"]
        self.assertEqual(subprocess.run(command, capture_output=True, check=False).returncode, 1)
        self.write_ledger([self.entry])
        self.assertEqual(subprocess.run(command, capture_output=True, check=False).returncode, 0)
        subprocess.run(["git", "init", "-q", str(self.root)], check=True)
        subprocess.run(["git", "-C", str(self.root), "-c", "user.name=Test", "-c", "user.email=test@example.org",
                        "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-qm",
                        "fix\n\nUpstream-Fixes: #1"], check=True)
        self.assertEqual(subprocess.run(command + ["--commits-range", "HEAD"], capture_output=True, check=False).returncode, 0)
        (self.root / "fixed.py").write_text("regressed", encoding="utf-8")
        self.assertEqual(subprocess.run(command + ["--commits-range", "HEAD"], capture_output=True, check=False).returncode, 1)

    def test_history_is_inert_when_current_evidence_is_missing(self):
        entry = copy.deepcopy(self.entry)
        entry["history"] = [{"fixed_if_all_present": ["fixed.py:proved"]}]
        entry["fixed_if_all_present"] = ["fixed.py:missing"]
        self.write_ledger([entry])
        self.assertTrue(mod.declared_problems(mod.load_ledger(self.source, self.root), self.source,
                                             "Upstream-Fixes: #1", self.root))


if __name__ == "__main__":
    unittest.main()
