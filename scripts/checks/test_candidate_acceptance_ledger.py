"""Acceptance coverage must not go green after criteria or evidence drift."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location('candidate_ledger', Path(__file__).with_name('candidate-acceptance-ledger.py'))
LEDGER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(LEDGER)


class CandidateAcceptanceLedgerTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.reference = 'backend/example_test.go::TestAdmission'
        self.story = ('## Acceptance Criteria\n\n1. AC-001 (negative): rejects an illegal route.\n'
                      '2. AC-002 (positive): preserves a legal route.\n'
                      '## Linked Tests\n\n- `backend/example_test.go`::`TestAdmission`\n'
                      '### Coverage Boundaries\n\nProduction observation is independent.\n')
        self.data = {'schema_version': 2, 'contract': 'US-050',
                     'criteria': {key: {'status': 'contract-tested', 'evidence': [self.reference]}
                                  for key in ('AC-001', 'AC-002')},
                     'operational_observations': f'{LEDGER.STORY.as_posix()}#coverage-boundaries'}
        self.write(LEDGER.STORY, self.story)
        self.write(Path('backend/example_test.go'), 'package example\nfunc TestAdmission(t *testing.T) {}\n')

    def write(self, path, text):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)

    def check(self):
        self.write(LEDGER.LEDGER, json.dumps(self.data))
        return LEDGER.check(self.root)

    def test_accepts_complete_linked_contract_evidence_without_live_receipts(self):
        self.assertEqual([], self.check())

    def test_rejects_missing_and_invented_criteria(self):
        self.data['criteria']['AC-999'] = self.data['criteria'].pop('AC-002')
        errors = self.check()
        self.assertIn('missing criteria: AC-002', errors)
        self.assertIn('unknown criteria: AC-999', errors)

    def test_new_story_criterion_requires_a_ledger_decision(self):
        self.write(LEDGER.STORY, self.story.replace('## Linked Tests', '3. AC-003 (negative): rechecks.\n\n## Linked Tests'))
        self.assertIn('missing criteria: AC-003', self.check())

    def test_rejects_missing_unlinked_and_deleted_tests(self):
        for evidence in ([], ['backend/example_test.go::TestMissing'], ['../outside_test.go::TestAdmission']):
            with self.subTest(evidence=evidence):
                self.data['criteria']['AC-001']['evidence'] = evidence
                self.assertTrue(self.check())
        self.data['criteria']['AC-001']['evidence'] = [self.reference]
        self.write(Path('backend/example_test.go'), '// func TestAdmission(t *testing.T) {}\n')
        self.assertTrue(self.check())

    def test_blocked_requires_reason_and_live_requires_independent_receipt(self):
        self.data['criteria']['AC-001'] = {'status': 'blocked'}
        self.assertTrue(self.check())
        self.data['criteria']['AC-001']['reason'] = 'Missing regression for revocation'
        self.assertEqual([], self.check())
        self.data['criteria']['AC-002']['status'] = 'live-verified'
        self.assertTrue(self.check())
        self.data['criteria']['AC-002']['live_evidence'] = 'Production observation is independent.'
        self.assertTrue(self.check())
        self.write(LEDGER.STORY, self.story + '\n[Run receipt](https://example.test/receipts/run-123)\n')
        self.data['criteria']['AC-002']['live_evidence'] = 'https://example.test/receipts/run-123'
        self.assertEqual([], self.check())

    def test_rejects_malformed_status_and_legacy_global_block(self):
        self.data['criteria']['AC-001']['status'] = []
        self.assertTrue(self.check())
        self.data['criteria']['AC-001']['status'] = 'contract-tested'
        self.data['live_observation'] = {'status': 'blocked'}
        self.assertTrue(self.check())

    def test_rejects_duplicate_json_and_story_criteria(self):
        raw = json.dumps(self.data).replace('"schema_version": 2', '"schema_version": 1, "schema_version": 2')
        self.write(LEDGER.LEDGER, raw)
        self.assertTrue(any('duplicate JSON key' in error for error in LEDGER.check(self.root)))
        self.write(LEDGER.STORY, self.story.replace('AC-002', 'AC-001'))
        self.assertTrue(any('unique acceptance criteria' in error for error in self.check()))

    def test_python_evidence_resolves_qualified_test_method(self):
        self.write(Path('fixtures/test_gate.py'), 'class GateTest:\n    def test_rejects_drift(self):\n        pass\n')
        self.assertTrue(LEDGER.evidence_exists(self.root, 'fixtures/test_gate.py::GateTest.test_rejects_drift'))
        self.assertFalse(LEDGER.evidence_exists(self.root, 'fixtures/test_gate.py::WrongTest.test_rejects_drift'))


if __name__ == '__main__':
    unittest.main()
