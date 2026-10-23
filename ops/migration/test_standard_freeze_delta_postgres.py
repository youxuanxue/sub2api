"""Real PostgreSQL regression for the freeze-delta sequence guard.

Run with MIGRATION_PG_TEST_CONTAINER naming an isolated tokenkey-*-test container.
No network, application containers, production data, or billing APIs are used.
"""
import os
import subprocess
import unittest
import uuid

from ops.migration.standard_freeze_delta import plan

CONTAINER = os.environ.get('MIGRATION_PG_TEST_CONTAINER', '')


@unittest.skipUnless(CONTAINER, 'requires an isolated PostgreSQL test container')
class SequenceGuardPostgresTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not CONTAINER.startswith('tokenkey-') or not CONTAINER.endswith('-test'):
            raise ValueError('isolated test container required')
        cls.database = 'freeze_delta_' + uuid.uuid4().hex
        subprocess.run(['docker', 'exec', CONTAINER, 'createdb', '-U', 'postgres', cls.database], check=True)
        cls.command = ['docker', 'exec', '-i', CONTAINER, 'psql', '-XqAt', '-v', 'ON_ERROR_STOP=1',
                       '-U', 'postgres', '-d', cls.database]

    @classmethod
    def tearDownClass(cls):
        subprocess.run(['docker', 'exec', CONTAINER, 'dropdb', '-U', 'postgres', cls.database], check=True)

    def sql(self, sql, *, success=True):
        result = subprocess.run(self.command, input=sql, text=True, capture_output=True, timeout=15)
        if success:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0)
        return result.stdout.strip()

    def setUp(self):
        # A restored partitioned table whose parent-owned sequence lags the copied rows:
        # the shape the guard exists for (pg_dump of partitions does not carry setval).
        self.sql("""DROP SCHEMA public CASCADE; CREATE SCHEMA public;
CREATE SEQUENCE usage_logs_id_seq START 100;
CREATE TABLE usage_logs (id bigint DEFAULT nextval('usage_logs_id_seq'), created_at timestamptz NOT NULL)
  PARTITION BY RANGE(created_at);
CREATE TABLE usage_logs_p1 PARTITION OF usage_logs FOR VALUES FROM ('2026-01-01') TO ('2026-02-01');
ALTER SEQUENCE usage_logs_id_seq OWNED BY usage_logs.id;
INSERT INTO usage_logs VALUES (100,'2026-01-02'),(205,'2026-01-03');
""")
        self.guard = plan('same', 'same', {'dedup_max_id': 1})['commands']['sequence_guard']

    def test_rejects_partition_parent_sequence_behind_data(self):
        self.sql(self.guard, success=False)

    def test_accepts_once_sequence_is_past_data(self):
        self.sql("SELECT setval('usage_logs_id_seq', 205, true)")
        self.sql(self.guard)

    def test_unused_sequence_at_highest_value_is_still_behind(self):
        # is_called=false means nextval returns 205 again: a collision.
        self.sql("SELECT setval('usage_logs_id_seq', 205, false)")
        self.sql(self.guard, success=False)


if __name__ == '__main__':
    unittest.main()
