"""Real PostgreSQL regression for the one-time Tokyo ID repair.

Run with TOKYO_REPAIR_TEST_CONTAINER naming an isolated tokenkey-*-test container.
No network, application containers, production data, or billing APIs are used.
"""
import csv
import hashlib
import io
import os
from pathlib import Path
import subprocess
import time
import unittest
import uuid

from ops.migration.standard_freeze_delta import plan

ROOT = Path(__file__).resolve().parent
CONTAINER = os.environ.get('TOKYO_REPAIR_TEST_CONTAINER', '')


@unittest.skipUnless(CONTAINER, 'requires an isolated PostgreSQL test container')
class TokyoUsageRepairTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not CONTAINER.startswith('tokenkey-') or not CONTAINER.endswith('-test'):
            raise ValueError('isolated test container required')
        cls.database = 'tokyo_repair_' + uuid.uuid4().hex
        subprocess.run(['docker', 'exec', CONTAINER, 'createdb', '-U', 'postgres', cls.database], check=True)
        cls.command = ['docker', 'exec', '-i', CONTAINER, 'psql', '-XqAt', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', cls.database]

    @classmethod
    def tearDownClass(cls):
        subprocess.run(['docker', 'exec', CONTAINER, 'dropdb', '-U', 'postgres', cls.database], check=True)

    def sql(self, sql, *, digest=None, success=True):
        cmd = self.command + (['-v', 'expected_content_md5=' + digest] if digest is not None else [])
        result = subprocess.run(cmd, input=sql, text=True, capture_output=True, timeout=15)
        if success:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0)
        return result.stdout.strip()

    def setUp(self):
        self.sql("""DROP SCHEMA public CASCADE; CREATE SCHEMA public;
CREATE SEQUENCE usage_logs_id_seq START 14724938;
CREATE TABLE usage_logs (id bigint DEFAULT nextval('usage_logs_id_seq'), created_at timestamptz NOT NULL,
request_id text, api_key_id bigint, actual_cost numeric, metadata jsonb) PARTITION BY RANGE(created_at);
CREATE TABLE usage_logs_20261017 PARTITION OF usage_logs FOR VALUES FROM ('2026-10-17') TO ('2026-10-18');
ALTER SEQUENCE usage_logs_id_seq OWNED BY usage_logs.id;
CREATE TABLE billing_usage_entries (usage_log_id bigint);
INSERT INTO usage_logs VALUES (14724938,'2026-10-17T01:00Z','historical',11,2.00,'{"keep":true}'),
(14727151,'2026-10-17T02:00Z','last-historical',12,3.00,'{}');
INSERT INTO usage_logs(created_at,request_id,api_key_id,actual_cost,metadata)
VALUES ('2026-10-17T03:00Z','new',13,4.00,'{"keep":"new"}');
""")

    def backup_digest(self):
        rows = self.sql("SELECT to_jsonb(u)::text FROM usage_logs u WHERE created_at > '2026-10-17T02:54:43.473757Z' AND id BETWEEN 14724938 AND 14727151 ORDER BY id,created_at;")
        return hashlib.md5(''.join(hashlib.md5(row.encode()).hexdigest() for row in rows.splitlines()).encode()).hexdigest()

    def advance(self):
        self.sql((ROOT / 'tokyo_usage_sequence_20261017.sql').read_text())

    def repair(self, digest=None, success=True):
        return self.sql((ROOT / 'tokyo_usage_rekey_20261017.sql').read_text(),
                        digest=self.backup_digest() if digest is None else digest, success=success)

    def test_repair_keeps_history_and_every_non_id_field_and_is_idempotent(self):
        before = self.sql("SELECT jsonb_agg(to_jsonb(u)-'id' ORDER BY request_id) FROM usage_logs u;")
        self.advance()
        mapping = list(csv.DictReader(io.StringIO(self.repair())))
        self.assertEqual([(r['old_id'], r['new_id']) for r in mapping], [('14724938', '14727152')])
        self.assertEqual(self.sql("SELECT id FROM usage_logs WHERE request_id='historical'"), '14724938')
        self.assertEqual(self.sql("SELECT jsonb_agg(to_jsonb(u)-'id' ORDER BY request_id) FROM usage_logs u;"), before)
        self.assertEqual(self.sql("SELECT count(*)-count(DISTINCT id) FROM usage_logs"), '0')
        self.assertEqual(list(csv.DictReader(io.StringIO(self.repair()))), [])

    def test_stale_backup_aborts_without_changing_rows(self):
        self.advance()
        digest = self.backup_digest()
        self.sql("UPDATE usage_logs SET actual_cost=7 WHERE request_id='new'")
        self.repair(digest, success=False)
        self.assertEqual(self.sql("SELECT id,actual_cost FROM usage_logs WHERE request_id='new'"), '14724938|7')

    def test_billing_references_abort_instead_of_guessing(self):
        self.advance()
        self.sql('INSERT INTO billing_usage_entries VALUES (14724938)')
        self.repair(success=False)
        self.assertEqual(self.sql("SELECT id FROM usage_logs WHERE request_id='new'"), '14724938')

    def test_sequence_never_moves_backwards(self):
        self.sql("SELECT setval('usage_logs_id_seq',20000000,true)")
        self.advance()
        self.assertEqual(self.sql("SELECT nextval('usage_logs_id_seq')"), '20000001')

    def test_final_delta_plan_rejects_partition_parent_sequence_behind_data(self):
        guard = plan('same', 'same', {'dedup_max_id': 1})['commands']['sequence_guard']
        self.sql(guard, success=False)
        self.advance()
        self.sql(guard)

    def test_sequence_alter_serializes_nextval(self):
        process = subprocess.Popen(self.command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        process.stdin.write("BEGIN; ALTER SEQUENCE usage_logs_id_seq CACHE 1; SELECT 'locked';\n")
        process.stdin.flush()
        self.assertEqual(process.stdout.readline().strip(), 'locked')
        try:
            start = time.monotonic()
            self.sql("SET lock_timeout='250ms'; SELECT nextval('usage_logs_id_seq')", success=False)
            self.assertLess(time.monotonic() - start, 3)
        finally:
            _, stderr = process.communicate('ROLLBACK;\n', timeout=5)
            self.assertEqual(process.returncode, 0, stderr)


if __name__ == '__main__':
    unittest.main()
