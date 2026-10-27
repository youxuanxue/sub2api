"""Exercise filesystem preservation and DB-commit ordering for layout repair."""
import datetime as dt
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import repair_capture_layout as repair


class RepairTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.now = dt.datetime(2026,10,27,10,tzinfo=dt.timezone.utc)
        self.source = self.root/'qa_blobs/2026/10/26/ab/ab-request.json.zst'
        self.source.parent.mkdir(parents=True)
        self.payload = {'request_id':'ab-request','captured_at':'2026-10-26T23:15:20Z','request':{'body':'preserved'}}
        self.raw = json.dumps(self.payload).encode()
        self.source.write_bytes(self.raw)
        self.item = repair.entry(self.root,self.source,json.loads,self.now)
        self.target = self.root/self.item['destination']

    def test_rehome_preserves_recent_contents_without_expiring_them(self):
        self.assertEqual(self.item['destination'],'qa_blobs/2026/10/26/23/ab/ab-request.json.zst')
        repair.revalidate(self.root,[self.item])
        repair.link_targets(self.root,[self.item])
        self.assertEqual(self.source.stat().st_ino,self.target.stat().st_ino)
        repair.retire_source_links(self.root,[self.item])
        self.assertFalse(self.source.exists())
        self.assertEqual(self.target.read_bytes(),self.raw)
        self.assertEqual(repair.inventory(self.root,json.loads,self.now),[])

    def test_resume_after_links_and_after_source_retirement(self):
        repair.link_targets(self.root,[self.item])
        repair.link_targets(self.root,[self.item])
        repair.retire_source_links(self.root,[self.item])
        repair.revalidate(self.root,[self.item])
        repair.link_targets(self.root,[self.item])
        repair.retire_source_links(self.root,[self.item])
        self.assertEqual(self.target.read_bytes(),self.raw)

    def test_tamper_and_collision_fail_before_retirement(self):
        self.source.write_bytes(b'changed')
        with self.assertRaisesRegex(ValueError,'source changed'):
            repair.revalidate(self.root,[self.item])
        self.source.write_bytes(self.raw)
        self.target.parent.mkdir(parents=True)
        self.target.write_bytes(b'unrelated')
        with self.assertRaisesRegex(ValueError,'destination collision'):
            repair.revalidate(self.root,[self.item])
        self.assertEqual(self.source.read_bytes(),self.raw)
        self.assertEqual(self.target.read_bytes(),b'unrelated')

    def test_payload_identity_date_future_and_symlink_are_rejected(self):
        for field,value in [('request_id','different'),('captured_at','2026-10-25T23:15:20Z'),('captured_at','2027-10-26T23:15:20Z')]:
            with self.subTest(field=field,value=value):
                payload={**self.payload,field:value}
                with self.assertRaises(ValueError):
                    repair.entry(self.root,self.source,lambda _:payload,self.now)
        self.target.parent.parent.mkdir(parents=True)
        self.target.parent.symlink_to(self.source.parent,target_is_directory=True)
        with self.assertRaisesRegex(ValueError,'symlink'):
            repair.revalidate(self.root,[self.item])
        self.assertEqual(self.source.read_bytes(),self.raw)

    def test_orphan_expired_blob_is_rehomed_not_deleted(self):
        repair.link_targets(self.root,[self.item])
        repair.retire_source_links(self.root,[self.item])
        self.assertEqual(self.target.read_bytes(),self.raw)
        self.assertEqual(json.loads(self.target.read_bytes())['captured_at'],self.payload['captured_at'])

    def test_db_failure_preserves_source_and_destination(self):
        class FailingDB:
            def query(_, sql):
                if sql.startswith('SELECT value'):return '2026-10-27T10:00:00Z'
                if sql.startswith('UPDATE'):raise ValueError('database unavailable')
                return ''
        plan={'entries':[self.item],'existing_cutover':'','cutover':'2026-10-27T10:00:00Z'}
        with self.assertRaisesRegex(ValueError,'database unavailable'):
            repair.apply(FailingDB(),self.root,plan)
        self.assertEqual(self.source.read_bytes(),self.raw)
        self.assertEqual(self.target.read_bytes(),self.raw)

    def test_db_commit_precedes_retirement(self):
        class RecordingDB:
            committed=False
            def query(db, sql):
                if sql.startswith('SELECT value'):return '2026-10-27T10:00:00Z'
                if sql.startswith('SELECT count'):return '0'
                if sql=='COMMIT;':db.committed=True
                return ''
        db=RecordingDB();retire=repair.retire_source_links
        def checked_retire(root,items):
            self.assertTrue(db.committed)
            retire(root,items)
        with patch.object(repair,'retire_source_links',checked_retire):
            repair.apply(db,self.root,{'entries':[self.item],'existing_cutover':'','cutover':'2026-10-27T10:00:00Z'})
        self.assertEqual(self.target.read_bytes(),self.raw)
        self.assertFalse(self.source.exists())


if __name__=='__main__':unittest.main()
