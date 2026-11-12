import datetime as dt
import errno
import fcntl
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent))
import force_retention as retention
from test_source_worker import source_config
from test_coordinator_host import config as data_config


NOW = dt.datetime(2026, 11, 12, 10, tzinfo=dt.timezone.utc)


class RetentionTest(unittest.TestCase):
    def payload(self, root, age, kind='qa_blobs'):
        path = root / kind / (NOW - dt.timedelta(hours=age)).strftime('%Y/%m/%d/%H') / 'fixture'
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(b'QA payload')
        return path

    def run_source(self, cfg, apply=True, now=NOW):
        with mock.patch.object(retention.socket, 'gethostname', return_value=cfg['hostname']), \
             mock.patch.object(retention.subprocess, 'run', side_effect=AssertionError('expiry must not call SSH, seal or PostgreSQL')):
            return retention.run(cfg, 'source', apply=apply, now=now)

    def test_one_day_boundary_dry_run_and_offline_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cfg = source_config(root)
            old = [self.payload(root, 25, kind) for kind in ('qa_blobs', 'qa_dlq')]
            keep = [self.payload(root, 24), self.payload(root, 0)]
            unrelated = root / 'business.json'
            unrelated.write_text('billing')
            cfg['binary'] = '/missing-binary'
            plan = self.run_source(cfg, apply=False)
            self.assertEqual(len(plan['removed']), 2)
            self.assertTrue(all(path.exists() for path in old + keep))
            self.assertFalse((root / 'qa_retention_state').exists())
            # Archive worker holds its own lock; retention still proceeds.
            with Path(cfg['host_lock']).open('a') as lock:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                result = self.run_source(cfg)
            self.assertTrue(result['ok'])
            self.assertFalse(any(path.exists() for path in old))
            self.assertTrue(all(path.exists() for path in keep))
            self.assertEqual(unrelated.read_text(), 'billing')
            self.assertFalse(result['archive_verified'])
            self.assertEqual(self.run_source(cfg)['removed'], [])
            late = self.payload(root, 25)
            self.run_source(cfg)
            self.assertFalse(late.exists())

    def test_boundary_one_second_before_deadline_keeps_entire_hour(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old = self.payload(root, 25)
            result = self.run_source(source_config(root), now=NOW - dt.timedelta(seconds=1))
            self.assertEqual(result['removed'], [])
            self.assertTrue(old.exists())

    def test_symlink_does_not_escape_and_does_not_block_other_hours(self):
        with tempfile.TemporaryDirectory() as directory, tempfile.TemporaryDirectory() as outside:
            root = Path(directory)
            outside_root = Path(outside)
            protected = outside_root / 'billing'
            protected.write_text('untouched')
            old = self.payload(root, 26)
            link = root / 'qa_blobs' / (NOW - dt.timedelta(hours=25)).strftime('%Y/%m/%d/%H')
            link.parent.mkdir(parents=True, exist_ok=True)
            link.symlink_to(outside_root, target_is_directory=True)
            result = self.run_source(source_config(root))
            self.assertFalse(result['ok'])
            self.assertFalse(old.exists())
            self.assertEqual(protected.read_text(), 'untouched')
            self.assertTrue(link.is_symlink())

    def test_full_disk_audit_failure_does_not_block_reclamation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old = self.payload(root, 25)
            original = retention.archive.atomic_json
            def write(path, value):
                if path.name == 'last-intent.json':
                    raise OSError(errno.ENOSPC, 'full disk')
                original(path, value)
            with mock.patch.object(retention.archive, 'atomic_json', side_effect=write):
                result = self.run_source(source_config(root))
            self.assertFalse(old.exists())
            self.assertFalse(result['ok'])
            self.assertEqual(result['errors'][0]['operation'], 'audit')

    def test_data_retention_uses_native_expiry_without_archive_or_coordinator_lock(self):
        with tempfile.TemporaryDirectory() as directory:
            cfg = data_config(Path(directory))
            with Path(cfg['host_lock']).open('a') as lock, \
                 mock.patch.object(retention.socket, 'gethostname', return_value=cfg['hostname']), \
                 mock.patch.object(retention.coordinator_host, 'source_command', side_effect=AssertionError('no SSH')), \
                 mock.patch.object(retention.coordinator_host, 'database', return_value={'ok': True}) as database:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                result = retention.run(cfg, 'data', apply=True, now=NOW)
            database.assert_called_once_with(cfg, 'expire-shared', dry_run=False)
            self.assertTrue(result['ok'])
            saved = json.loads((Path(cfg['state_root']) / 'last-retention.json').read_text())
            self.assertFalse(saved['archive_verified'])

    def test_wrong_host_cannot_delete(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old = self.payload(root, 25)
            with mock.patch.object(retention.socket, 'gethostname', return_value='wrong-host'):
                with self.assertRaisesRegex(ValueError, 'identity'):
                    retention.run(source_config(root), 'source', apply=True, now=NOW)
            self.assertTrue(old.exists())

    def test_interrupted_archive_staging_is_also_reclaimed_only_after_deadline(self):
        with tempfile.TemporaryDirectory() as directory:
            cfg = source_config(Path(directory))
            receipt_root = Path(cfg['receipt_root'])
            old = receipt_root / ('.push-' + (NOW - dt.timedelta(hours=25)).strftime('%Y%m%d%H') + '-fixture') / 'payload'
            keep = receipt_root / ('.push-' + (NOW - dt.timedelta(hours=24)).strftime('%Y%m%d%H') + '-fixture') / 'payload'
            for path in (old, keep):
                path.parent.mkdir(parents=True)
                path.write_bytes(b'staged QA')
            result = self.run_source(cfg)
            self.assertTrue(result['ok'])
            self.assertFalse(old.exists())
            self.assertTrue(keep.exists())

    def test_partial_delete_failure_continues_other_hours_and_retries(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            older = self.payload(root, 26)
            old = self.payload(root, 25)
            original = retention.source_cleanup.remove_hour
            def remove(base, relative):
                if relative == older.parent.relative_to(root):
                    raise OSError('temporary delete failure')
                original(base, relative)
            with mock.patch.object(retention.source_cleanup, 'remove_hour', side_effect=remove):
                result = self.run_source(source_config(root))
            self.assertFalse(result['ok'])
            self.assertTrue(older.exists())
            self.assertFalse(old.exists())
            self.assertTrue(self.run_source(source_config(root))['ok'])
            self.assertFalse(older.exists())


if __name__ == '__main__':
    unittest.main()
