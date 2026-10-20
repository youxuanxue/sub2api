import json
from pathlib import Path
import tempfile
import unittest
from postgres_backup_verdict import evaluate


class VerdictTests(unittest.TestCase):
    def test_expired_success_is_a_warning_but_wal_failure_is_an_issue(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'health.json'
            receipt = {'observed_at': 999999, 'status': 'degraded',
                       'reasons': ['restore_acceptance_missing_or_stale'],
                       'restore_acceptance': {'status': 'ok', 'completed_at': 1}}
            path.write_text(json.dumps(receipt))
            verdict = evaluate(path, 'prod', 1000000)
            self.assertEqual((verdict['status'], verdict['severity']), ('warning', 'warning'))
            receipt['reasons'].append('archive_round_trip_missing_or_stale')
            path.write_text(json.dumps(receipt))
            verdict = evaluate(path, 'prod', 1000000)
            self.assertEqual((verdict['status'], verdict['severity']), ('issue_candidate', 'error'))

    def test_current_degraded_stale_and_missing_health(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'health.json'
            self.assertEqual(evaluate(path, 'prod', 1000)['status'], 'issue_candidate')
            for receipt, expected in [
                ({'observed_at': 999, 'status': 'ok'}, 'ok'),
                ({'observed_at': 999, 'status': 'degraded', 'reasons': ['WAL gap']}, 'issue_candidate'),
                ({'observed_at': 1, 'status': 'ok'}, 'issue_candidate'),
                ([], 'issue_candidate'),
            ]:
                path.write_text(json.dumps(receipt))
                verdict = evaluate(path, 'prod', 1000)
                self.assertEqual(verdict['status'], expected)
                self.assertEqual(verdict['severity'], 'info' if expected == 'ok' else 'error')
