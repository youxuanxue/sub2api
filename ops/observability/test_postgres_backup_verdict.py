import json
from pathlib import Path
import tempfile
import unittest
from postgres_backup_verdict import evaluate


class VerdictTests(unittest.TestCase):
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
