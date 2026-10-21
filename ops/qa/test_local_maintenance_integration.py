"""Real PostgreSQL checks: QA_LOCAL_BINARY=<linux arm64 binary> python -m unittest ..."""
import json
import os
from pathlib import Path
import subprocess
import time
import unittest
import uuid


@unittest.skipUnless(os.environ.get('QA_LOCAL_BINARY'), 'requires built Linux binary and Docker')
class LocalProvisionIntegration(unittest.TestCase):
    def command(self, args, check=True):
        result = subprocess.run(args, text=True, capture_output=True)
        if check and result.returncode:
            self.fail(result.stdout + result.stderr)
        return result

    def setUp(self):
        self.name = 'qa-local-test-'+uuid.uuid4().hex[:12]
        binary = Path(os.environ['QA_LOCAL_BINARY']).resolve()
        self.command(['docker','run','-d','--name',self.name,'--network','none',
                      '-e','POSTGRES_HOST_AUTH_METHOD=trust','-e','POSTGRES_USER=tokenkey',
                      '-e','POSTGRES_DB=tokenkey','postgres:18-alpine'])
        self.addCleanup(self.command, ['docker','rm','-f','-v',self.name])
        self.command(['docker','cp',str(binary),self.name+':/qa-local'])
        deadline=time.monotonic()+30
        while self.command(['docker','exec',self.name,'pg_isready','-h','127.0.0.1','-U','tokenkey'],check=False).returncode:
            if time.monotonic()>deadline:
                self.fail('PostgreSQL did not start')
            time.sleep(0.2)
        self.sql('CREATE TABLE qa_records(id bigint,created_at timestamptz NOT NULL,PRIMARY KEY(id,created_at)) PARTITION BY RANGE(created_at)')

    def sql(self, query):
        return self.command(['docker','exec',self.name,'psql','-U','tokenkey','-d','tokenkey','-XAt','-v','ON_ERROR_STOP=1','-c',query]).stdout.strip()

    def run_phase(self, check=True, phase="provision", dry_run=False):
        return self.command(['docker','exec','-e','PGHOST=/var/run/postgresql','-e','PGUSER=tokenkey','-e','PGDATABASE=tokenkey',self.name,'/qa-local','--phase='+phase,'--confirm=tokenkey-qa-local-'+phase+'-v1']+(['--dry-run'] if dry_run else []),check=check)

    def test_provision_is_idempotent_accepts_live_writes_and_preserves_old_records(self):
        self.sql("CREATE TABLE qa_records_20000101_00 PARTITION OF qa_records FOR VALUES FROM ('2000-01-01T00:00:00Z') TO ('2000-01-01T01:00:00Z'); INSERT INTO qa_records VALUES(1,'2000-01-01T00:30:00Z')")
        for _ in range(2):
            receipt=json.loads(self.run_phase().stdout)
            self.assertTrue(receipt['ok'])
            self.assertEqual(receipt['provision']['ranges_covered'],72)
            self.assertFalse(receipt['upload_authorized'])
            self.assertFalse(receipt['deletion_authorized'])
        self.sql('INSERT INTO qa_records VALUES(2,now())')
        self.assertEqual(self.sql("SELECT count(*) FROM pg_inherits WHERE inhparent='qa_records'::regclass"),'73')
        self.assertEqual(self.sql('SELECT string_agg(id::text,\',\' ORDER BY id) FROM qa_records'),'1,2')

    def test_wrong_parent_key_fails_before_creating_children(self):
        self.sql('DROP TABLE qa_records; CREATE TABLE qa_records(id bigint,created_at timestamptz) PARTITION BY RANGE(id)')
        result=self.run_phase(check=False)
        self.assertNotEqual(result.returncode,0)
        self.assertIn('expected primary QA hourly partition parent',result.stderr)
        self.assertEqual(self.sql("SELECT count(*) FROM pg_inherits WHERE inhparent='qa_records'::regclass"),'0')


class LocalCleanupIntegration(LocalProvisionIntegration):
    def setUp(self):
        super().setUp()
        root=Path(__file__).resolve().parents[2]
        self.sql("ALTER TABLE qa_records ADD COLUMN request_id text NOT NULL DEFAULT 'fixture'")
        for name in ('tk_069_create_qa_archive_shards.sql','tk_070_qa_archive_closeout_control.sql','tk_073_qa_hourly_lifecycle_control.sql'):
            self.sql((root/'backend/migrations'/name).read_text())
        self.command(['docker','exec',self.name,'mkdir','-p','/app/data/qa_blobs','/app/data/qa_dlq'])

    def hour(self, hours_ago):
        import datetime
        raw=self.sql("SELECT (date_trunc('hour',clock_timestamp())-interval '%s hours') AT TIME ZONE 'UTC'"%hours_ago)
        return datetime.datetime.fromisoformat(raw)

    def source_hour(self, hour, record_id):
        import datetime
        name='qa_records_'+hour.strftime('%Y%m%d_%H')
        self.sql("CREATE TABLE %s PARTITION OF qa_records FOR VALUES FROM ('%sZ') TO ('%sZ'); INSERT INTO qa_records(id,created_at) VALUES(%d,'%sZ')"%(name,hour.isoformat(),(hour+datetime.timedelta(hours=1)).isoformat(),record_id,hour.isoformat()))
        return name

    def hot_files(self,hour):
        paths=[]
        for root in ('qa_blobs','qa_dlq'):
            directory='/app/data/'+root+'/'+hour.strftime('%Y/%m/%d/%H')
            self.command(['docker','exec',self.name,'mkdir','-p',directory])
            self.command(['docker','exec',self.name,'touch',directory+'/fixture'])
            paths.append(directory)
        return paths

    def exists(self,path):
        return self.command(['docker','exec',self.name,'test','-e',path],check=False).returncode==0

    def test_cleanup_plan_then_drop_unarchived_preserves_retention_boundary_and_other_data(self):
        expired=self.hour(25)
        protected=self.hour(24)
        old=self.source_hour(expired,1)
        keep=self.source_hour(protected,2)
        old_files=self.hot_files(expired)
        keep_files=self.hot_files(protected)
        self.command(['docker','exec',self.name,'touch','/app/data/unrelated'])
        plan=json.loads(self.run_phase(phase='cleanup',dry_run=True).stdout)
        self.assertFalse(plan['deletion_authorized'])
        self.assertEqual(plan['cleanup']['expired_partitions'],1)
        self.assertEqual(self.sql('SELECT count(*) FROM '+old),'1')
        self.assertTrue(all(self.exists(path) for path in old_files))
        result=json.loads(self.run_phase(phase='cleanup').stdout)
        self.assertTrue(result['ok'])
        self.assertTrue(result['deletion_authorized'])
        self.assertFalse(result['upload_authorized'])
        self.assertEqual(self.sql("SELECT to_regclass('%s') IS NULL"%old),'t')
        self.assertEqual(self.sql('SELECT id FROM '+keep),'2')
        self.assertFalse(any(self.exists(path) for path in old_files))
        self.assertTrue(all(self.exists(path) for path in keep_files))
        self.assertTrue(self.exists('/app/data/unrelated'))
        self.assertEqual(self.sql("SELECT state||':'||verification_error_code||':'||(source_dropped_at IS NOT NULL)::text||':'||(hot_files_cleaned_at IS NOT NULL)::text FROM qa_archive_shards"),'failed:source_unavailable_after_retention:true:true')
        self.assertEqual(json.loads(self.run_phase(phase='cleanup').stdout)['cleanup']['selected_hours'],None)

    def test_orphan_files_and_interrupted_drop_receipt_are_cleaned_without_a_partition(self):
        import datetime
        h=self.hour(27)
        paths=self.hot_files(h)
        self.sql("INSERT INTO qa_archive_shards(window_start,window_end,state,source_dropped_at) VALUES('%sZ','%sZ','failed',now())"%(h.isoformat(),(h+datetime.timedelta(hours=1)).isoformat()))
        orphan=self.hour(26)
        paths+=self.hot_files(orphan)
        result=json.loads(self.run_phase(phase='cleanup').stdout)
        self.assertTrue(result['ok'])
        self.assertEqual(len(result['cleanup']['expiries']),2)
        self.assertFalse(any(self.exists(path) for path in paths))
        self.assertEqual(self.sql('SELECT count(*) FROM qa_archive_shards WHERE source_dropped_at IS NOT NULL AND hot_files_cleaned_at IS NOT NULL'),'2')

    def test_symlink_and_default_partition_block_orphan_deletion(self):
        h=self.hour(27)
        paths=self.hot_files(h)
        self.sql('CREATE TABLE qa_records_default PARTITION OF qa_records DEFAULT')
        result=self.run_phase(phase='cleanup',check=False)
        self.assertNotEqual(result.returncode,0)
        self.assertIn('still has a source partition',result.stdout)
        self.assertTrue(all(self.exists(path) for path in paths))
        self.sql('DROP TABLE qa_records_default')
        old=self.source_hour(h,1)
        # Replace just the fixture hour with a link; outside content must survive.
        self.command(['docker','exec',self.name,'mkdir','-p','/outside'])
        self.command(['docker','exec',self.name,'touch','/outside/evidence'])
        self.command(['docker','exec',self.name,'rm','-rf',paths[0]])
        self.command(['docker','exec',self.name,'ln','-s','/outside',paths[0]])
        result=self.run_phase(phase='cleanup',check=False)
        self.assertNotEqual(result.returncode,0)
        self.assertIn('symlink',result.stdout)
        self.assertEqual(self.sql('SELECT count(*) FROM '+old),'1')
        self.assertTrue(self.exists('/outside/evidence'))

    def test_cleanup_batch_limit_and_resumption(self):
        import datetime
        start=self.hour(80)
        statements=[]
        for offset in range(49):
            hour=start+datetime.timedelta(hours=offset)
            statements.append("CREATE TABLE qa_records_%s PARTITION OF qa_records FOR VALUES FROM ('%sZ') TO ('%sZ')"%(hour.strftime('%Y%m%d_%H'),hour.isoformat(),(hour+datetime.timedelta(hours=1)).isoformat()))
        self.sql(';'.join(statements))
        first=json.loads(self.run_phase(phase='cleanup').stdout)['cleanup']
        self.assertEqual(first['expired_partitions'],49)
        self.assertEqual(len(first['expiries']),48)
        self.assertEqual(self.sql("SELECT count(*) FROM pg_inherits WHERE inhparent='qa_records'::regclass"),'1')
        second=json.loads(self.run_phase(phase='cleanup').stdout)['cleanup']
        self.assertEqual(second['expired_partitions'],1)
        self.assertEqual(len(second['expiries']),1)
        self.assertEqual(self.sql("SELECT count(*) FROM pg_inherits WHERE inhparent='qa_records'::regclass"),'0')

    def test_locked_partition_keeps_rows_and_files_for_next_run(self):
        h=self.hour(25)
        old=self.source_hour(h,1)
        paths=self.hot_files(h)
        command=['docker','exec',self.name,'psql','-U','tokenkey','-d','tokenkey','-XAt','-c',"BEGIN; LOCK TABLE %s IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(20)"%old]
        blocker=subprocess.Popen(command,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        try:
            deadline=time.monotonic()+5
            while self.sql("SELECT count(*) FROM pg_locks WHERE relation='%s'::regclass AND mode='AccessExclusiveLock' AND granted"%old)!='1':
                if time.monotonic()>deadline:self.fail('lock fixture did not start')
                time.sleep(.05)
            result=self.run_phase(phase='cleanup',check=False)
            self.assertNotEqual(result.returncode,0)
            self.assertTrue(all(self.exists(path) for path in paths))
        finally:
            self.sql("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE query LIKE 'BEGIN; LOCK TABLE %s%%' AND pid<>pg_backend_pid()"%old)
            blocker.wait(timeout=5)
        self.assertEqual(self.sql('SELECT count(*) FROM '+old),'1')
        self.assertTrue(json.loads(self.run_phase(phase='cleanup').stdout)['ok'])
        self.assertFalse(any(self.exists(path) for path in paths))


if __name__=='__main__':
    unittest.main()
