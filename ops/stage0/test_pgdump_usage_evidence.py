"""Usage reconciliation must fail closed on missing data, cost drift and corruption."""
import copy
import gzip
import pathlib
import tempfile
import unittest

import pgdump_usage_evidence as evidence
import test_pgdump_restore_canary as fixture
from test_pgdump_restore_canary import USAGE, USAGE_SQL, FakeCommands


class UsageEvidenceTest(unittest.TestCase):
    def inspect(self, data):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory)/'dump.sql.gz'
            path.write_bytes(data)
            return evidence.inspect_dump(path)

    def test_exact_costs_dates_and_empty_default_partition(self):
        size, summary = self.inspect(gzip.compress(USAGE_SQL))
        self.assertEqual(size, len(USAGE_SQL))
        self.assertEqual(summary, USAGE)
        same_in_japan = USAGE_SQL.replace(b'07:00:00+00', b'16:00:00+09')
        self.assertEqual(self.inspect(gzip.compress(same_in_japan))[1], USAGE)

    def test_missing_rows_malformed_columns_and_gzip_corruption_fail(self):
        cases = [gzip.compress(b'CREATE TABLE usage_logs(id bigint);\n'),
                 gzip.compress(USAGE_SQL.replace(b'actual_cost',b'other_cost')),
                 gzip.compress(USAGE_SQL)[:-4],
                 gzip.compress(USAGE_SQL)[:10] + b"invalid deflate" + gzip.compress(USAGE_SQL)[10:],
                 gzip.compress(USAGE_SQL.replace(b'0.1234567890',b'NaN')),
                 gzip.compress(USAGE_SQL[:-3])]
        for data in cases:
            with self.subTest(data_length=len(data)), self.assertRaises(ValueError):
                self.inspect(data)

    def test_each_reconciliation_dimension_rejects_drift(self):
        for field,value in [('rows',2),('total_cost','0.123456788'),('actual_cost','0'),
                            ('max_created_at','2026-08-18T07:00:01Z')]:
            actual=copy.deepcopy(USAGE)
            actual['usage_logs_20260818'][field]=value
            self.assertFalse(evidence.verified({'source':USAGE,'restored':actual}),field)
        actual=copy.deepcopy(USAGE); del actual['usage_logs_legacy']
        self.assertFalse(evidence.verified({'source':USAGE,'restored':actual}))
        self.assertFalse(evidence.verified({'source':{},'restored':{}}))

    def test_restore_mismatch_cleans_temporary_container_and_refuses_receipt(self):
        fake=FakeCommands()
        def run(args,**kwargs):
            result=fake(args,**kwargs)
            if 'canary_usage_evidence' in ' '.join(args):
                result.stdout=result.stdout.replace('0.123456789','0.223456789')
            return result
        from pgdump_restore_canary import CanaryError
        with self.assertRaisesRegex(CanaryError,'differ from S3 dump'):
            fixture.PgdumpRestoreCanaryTest().run_case(run)
        self.assertFalse(fake.container_present)


if __name__ == '__main__':
    unittest.main()
