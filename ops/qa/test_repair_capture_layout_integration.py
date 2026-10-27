"""QA_LAYOUT_INTEGRATION=1 runs real PostgreSQL and zstd repair checks."""
import ctypes
import datetime as dt
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import unittest
import uuid

import repair_capture_layout as repair


@unittest.skipUnless(os.environ.get('QA_LAYOUT_INTEGRATION'), 'requires Docker and zstd')
class RepairIntegration(unittest.TestCase):
    def test_repair_real_partition_references_and_compressed_content(self):
        name = 'qa-layout-test-' + uuid.uuid4().hex[:12]
        subprocess.run(['docker', 'run', '-d', '--name', name, '--network', 'none',
                        '-e', 'POSTGRES_HOST_AUTH_METHOD=trust', '-e', 'POSTGRES_USER=tokenkey',
                        '-e', 'POSTGRES_DB=tokenkey', 'postgres:18-alpine'], check=True, capture_output=True)
        self.addCleanup(subprocess.run, ['docker', 'rm', '-f', '-v', name], capture_output=True)
        deadline = time.monotonic() + 30
        while subprocess.run(['docker', 'exec', name, 'pg_isready', '-h', '127.0.0.1', '-U', 'tokenkey'], capture_output=True).returncode:
            self.assertLess(time.monotonic(), deadline)
            time.sleep(.2)
        db = repair.DB(name)
        self.addCleanup(db.close)
        db.query('CREATE TABLE settings(id bigserial PRIMARY KEY,key text UNIQUE,value text,updated_at timestamptz DEFAULT now());')
        fields = ','.join(field + ' text' for field in repair.FIELDS)
        db.query('CREATE TABLE qa_records(id bigint,created_at timestamptz,' + fields + ') PARTITION BY RANGE(created_at);')
        db.query("CREATE TABLE qa_records_20261027_10 PARTITION OF qa_records FOR VALUES FROM ('2026-10-27T10:00Z') TO ('2026-10-27T11:00Z');")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            source = root/'qa_blobs/2026/10/27/ab/ab-request.json.zst'
            source.parent.mkdir(parents=True)
            payload = {'request_id': 'ab-request', 'captured_at': '2026-10-27T10:15:00Z', 'request': {'body': 'unchanged'}}
            plain = json.dumps(payload).encode()
            # Use the same system library as the decoder; no zstd CLI dependency.
            lib = repair.Decoder().lib
            lib.ZSTD_compressBound.argtypes = [ctypes.c_size_t]
            lib.ZSTD_compressBound.restype = ctypes.c_size_t
            lib.ZSTD_compress.argtypes = [ctypes.c_void_p, ctypes.c_size_t, ctypes.c_void_p, ctypes.c_size_t, ctypes.c_int]
            lib.ZSTD_compress.restype = ctypes.c_size_t
            buffer = ctypes.create_string_buffer(lib.ZSTD_compressBound(len(plain)))
            size = lib.ZSTD_compress(buffer, len(buffer), plain, len(plain), 1)
            self.assertFalse(lib.ZSTD_isError(size))
            source.write_bytes(buffer.raw[:size])
            now = dt.datetime(2026, 10, 27, 11, tzinfo=dt.timezone.utc)
            os.utime(source, (now.timestamp()-120, now.timestamp()-120))
            entries = repair.inventory(root, repair.Decoder(), now)
            self.assertEqual(len(entries), 1)
            old = 'file:///app/data/' + entries[0]['source']
            new = 'file:///app/data/' + entries[0]['destination']
            db.query("INSERT INTO qa_records VALUES(1,'2026-10-27T10:15Z'," + ','.join([repair.quote(old)]*4) + "),(2,'2026-10-27T10:16Z','other',NULL,NULL,NULL);")
            plan = {'entries': entries, 'existing_cutover': '', 'cutover': '2026-10-27T11:00:00Z'}
            repair.apply(db, root, plan)
            self.assertFalse(source.exists())
            self.assertEqual(repair.Decoder()((root/entries[0]['destination']).read_bytes()), payload)
            for field in repair.FIELDS:
                self.assertEqual(db.query('SELECT '+field+' FROM qa_records WHERE id=1;'), new)
            self.assertEqual(db.query('SELECT blob_uri FROM qa_records WHERE id=2;'), 'other')
            self.assertEqual(db.query('SELECT value FROM settings;'), plan['cutover'])
            # QAMA excludes a competing lifecycle owner for the whole session.
            self.assertEqual(db.query(f'SELECT pg_try_advisory_lock({repair.QAMA});'), 't')
            other = repair.DB(name)
            try:
                self.assertEqual(other.query(f'SELECT pg_try_advisory_lock({repair.QAMA});'), 'f')
            finally:
                other.close()

            # Reproduce a legacy file whose INSERT is still uncommitted long
            # after the file mtime. It must keep its original usable URI.
            now = repair.utc(db.query('SELECT clock_timestamp();'))
            hour = now.replace(minute=0,second=0,microsecond=0)
            db.query("CREATE TABLE IF NOT EXISTS qa_records_"+hour.strftime('%Y%m%d_%H')+
                     " PARTITION OF qa_records FOR VALUES FROM ("+repair.quote(hour.isoformat())+
                     ") TO ("+repair.quote((hour+dt.timedelta(hours=1)).isoformat())+");")

            def blob(rid, captured):
                path=root/'qa_blobs'/captured.strftime('%Y/%m/%d')/rid[:2]/(rid+'.json.zst')
                path.parent.mkdir(parents=True,exist_ok=True)
                data=json.dumps({'request_id':rid,'captured_at':captured.isoformat()}).encode()
                buf=ctypes.create_string_buffer(lib.ZSTD_compressBound(len(data)))
                n=lib.ZSTD_compress(buf,len(buf),data,len(data),1)
                self.assertFalse(lib.ZSTD_isError(n))
                path.write_bytes(buf.raw[:n])
                os.utime(path,(now.timestamp()-120,now.timestamp()-120))
                return path,repair.entry(root,path,repair.Decoder(),now)

            pending_source,pending_item=blob('cd-delayed',now)
            expired_source,expired_item=blob('ef-orphan',now-dt.timedelta(hours=26))
            pending_old='file:///app/data/'+pending_item['source']
            pending_target=root/pending_item['destination']
            original=pending_source.read_bytes()
            writer=repair.DB(name)
            self.addCleanup(writer.close)
            writer.query('BEGIN; INSERT INTO qa_records VALUES(3,'+repair.quote(now.isoformat())+','+
                         ','.join([repair.quote(pending_old)]*4)+');')
            deferred_plan={**plan,'existing_cutover':plan['cutover'],'entries':[pending_item,expired_item]}

            def apply_once(value):
                connection=repair.DB(name)
                try:
                    return repair.apply(connection,root,value)
                finally:
                    connection.close()

            result=apply_once(deferred_plan)
            self.assertEqual(result,{'migrated_files':1,'deferred_files':1})
            self.assertEqual(pending_source.read_bytes(),original)
            self.assertFalse(pending_target.exists())
            self.assertFalse(expired_source.exists())
            self.assertEqual(repair.Decoder()((root/expired_item['destination']).read_bytes())['request_id'],'ef-orphan')
            writer.query('COMMIT;')
            self.assertEqual(db.query('SELECT blob_uri FROM qa_records WHERE id=3;'),pending_old)
            self.assertEqual(pending_source.read_bytes(),original)
            self.assertEqual(apply_once(deferred_plan),{'migrated_files':2,'deferred_files':0})
            self.assertFalse(pending_source.exists())
            self.assertEqual(pending_target.read_bytes(),original)
            for field in repair.FIELDS:
                self.assertEqual(db.query('SELECT '+field+' FROM qa_records WHERE id=3;'),
                                 'file:///app/data/'+pending_item['destination'])


if __name__ == '__main__':
    unittest.main()
