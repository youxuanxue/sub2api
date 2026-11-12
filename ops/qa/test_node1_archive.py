import argparse
import contextlib
import datetime as dt
import io
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import time
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('node1_archive', Path(__file__).with_name('node1_archive.py'))
archive = importlib.util.module_from_spec(spec)
spec.loader.exec_module(archive)


class Node1ArchiveTest(unittest.TestCase):
    def test_copy_writes_manifest_and_atomic_receipt(self):
        hour = dt.datetime(2026, 10, 31, 10, tzinfo=dt.timezone.utc)
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as node1:
            source_root, node1_root = Path(source), Path(node1)
            source_hour = source_root / 'qa_blobs' / hour.strftime(archive.HOUR)
            source_hour.mkdir(parents=True)
            (source_hour / 'one.bin').write_bytes(b'fixture')
            result = archive.copy_hour(source_root, node1_root, 'prod', hour)
            self.assertTrue(result['ok'])
            target = node1_root / 'qa-raw' / 'prod' / hour.strftime(archive.HOUR)
            self.assertEqual((target / 'qa_blobs' / hour.strftime(archive.HOUR) / 'one.bin').read_bytes(), b'fixture')
            receipt = node1_root / 'receipts' / 'prod' / (hour.strftime(archive.HOUR) + '.json')
            self.assertTrue(receipt.exists())
            self.assertEqual(json.loads(receipt.read_text())['file_count'], 1)
            self.assertFalse(list(target.parent.glob('.*')))

    def test_existing_receipt_is_idempotent_and_mismatch_fails(self):
        hour = dt.datetime(2026, 10, 31, 10, tzinfo=dt.timezone.utc)
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as node1:
            source_root, node1_root = Path(source), Path(node1)
            source_hour = source_root / 'qa_blobs' / hour.strftime(archive.HOUR)
            source_hour.mkdir(parents=True)
            (source_root / 'qa_dlq').mkdir()
            (source_hour / 'one.bin').write_bytes(b'fixture')
            first = archive.copy_hour(source_root, node1_root, 'prod', hour)
            second = archive.copy_hour(source_root, node1_root, 'prod', hour)
            self.assertTrue(second['already_completed'])
            receipt = node1_root / 'receipts' / 'prod' / (hour.strftime(archive.HOUR) + '.json')
            bad = json.loads(receipt.read_text())
            bad['ok'] = False
            receipt.write_text(json.dumps(bad))
            with self.assertRaises(ValueError):
                archive.copy_hour(source_root, node1_root, 'prod', hour)

    def test_published_hour_without_receipt_recovers_after_interrupted_publish(self):
        hour = dt.datetime(2026, 10, 31, 10, tzinfo=dt.timezone.utc)
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as node1:
            source_root, node1_root = Path(source), Path(node1)
            source_hour = source_root / 'qa_blobs' / hour.strftime(archive.HOUR)
            source_hour.mkdir(parents=True)
            (source_hour / 'one.bin').write_bytes(b'fixture')
            archive.copy_hour(source_root, node1_root, 'prod', hour)
            receipt = node1_root / 'receipts' / 'prod' / (hour.strftime(archive.HOUR) + '.json')
            receipt.unlink()
            recovered = archive.copy_hour(source_root, node1_root, 'prod', hour)
            self.assertTrue(recovered['recovered_receipt'])
            self.assertTrue(receipt.is_file())

    def test_existing_receipt_is_rejected_when_published_file_is_tampered(self):
        hour = dt.datetime(2026, 10, 31, 10, tzinfo=dt.timezone.utc)
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as node1:
            source_root, node1_root = Path(source), Path(node1)
            source_hour = source_root / 'qa_blobs' / hour.strftime(archive.HOUR)
            source_hour.mkdir(parents=True)
            (source_hour / 'one.bin').write_bytes(b'fixture')
            archive.copy_hour(source_root, node1_root, 'prod', hour)
            target = node1_root / 'qa-raw' / 'prod' / hour.strftime(archive.HOUR)
            (target / 'qa_blobs' / hour.strftime(archive.HOUR) / 'one.bin').write_bytes(b'tampered')
            with self.assertRaisesRegex(ValueError, 'existing node1 receipt is invalid'):
                archive.copy_hour(source_root, node1_root, 'prod', hour)

    def test_selection_skips_open_hour(self):
        hour = dt.datetime(2026, 10, 31, 10, tzinfo=dt.timezone.utc)
        with tempfile.TemporaryDirectory() as source:
            root = Path(source)
            (root / 'qa_blobs' / hour.strftime(archive.HOUR)).mkdir(parents=True)
            self.assertEqual(archive.hours_to_copy(root, hour + dt.timedelta(minutes=59)), [])
            self.assertEqual(archive.hours_to_copy(root, hour + dt.timedelta(hours=1)), [hour])


class SourceArchiveTest(unittest.TestCase):
    hour = dt.datetime(2026, 11, 11, 10, tzinfo=dt.timezone.utc)

    def test_empty_source_hour_has_manifest_but_legacy_empty_hour_still_fails(self):
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as node1:
            result = archive.copy_hour(Path(source), Path(node1), 'prod', self.hour, 'old')
            self.assertEqual(result['file_count'], 0)
            self.assertEqual(result['total_bytes'], 0)
            self.assertEqual(len(result['manifest_sha256']), 64)
            with self.assertRaises(ValueError):
                archive.copy_hour(Path(source), Path(node1), 'prod', self.hour)

    def write_source(self, root, content=b'fixture'):
        path = root / 'qa_blobs' / self.hour.strftime(archive.HOUR) / 'one.bin'
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        return path

    def test_two_sources_cannot_overwrite_each_other_or_legacy(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            node1 = root / 'node1'
            node1.mkdir()
            results = {}
            for source_id, content in (('', b'legacy'), ('lhins-old', b'old'), ('lhins-new', b'new')):
                source = root / (source_id or 'legacy')
                self.write_source(source, content)
                results[source_id] = archive.copy_hour(source, node1, 'prod', self.hour, source_id)
                target = node1 / 'qa-raw' / archive.archive_namespace('prod', source_id) / self.hour.strftime(archive.HOUR)
                self.assertEqual((target / 'qa_blobs' / self.hour.strftime(archive.HOUR) / 'one.bin').read_bytes(), content)
                manifest, digest, _ = archive._verified_manifest(target, 'prod', self.hour, source_id)
                self.assertEqual(digest, results[source_id]['manifest_sha256'])
                self.assertEqual(manifest.get('source_id', ''), source_id)
            for source_id, content in (('', b'legacy'), ('lhins-old', b'old'), ('lhins-new', b'new')):
                source = root / (source_id or 'legacy')
                result = archive.copy_hour(source, node1, 'prod', self.hour, source_id)
                self.assertTrue(result['already_completed'])
                self.assertEqual(result['manifest_sha256'], results[source_id]['manifest_sha256'])
                source_file = source / 'qa_blobs' / self.hour.strftime(archive.HOUR) / 'one.bin'
                self.assertEqual(source_file.read_bytes(), content)
            self.assertEqual(results['']['schema_version'], archive.SCHEMA)
            self.assertEqual(results['lhins-old']['schema_version'], archive.SOURCE_SCHEMA)

    def test_unsafe_identity_and_non_utc_hour_fail_before_copy(self):
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as node1:
            source_root, target = Path(source), Path(node1)
            self.write_source(source_root)
            for source_id in ('../old', '/old', 'old/new', '.', '..', '-old', 'old\nnew', 'a' * 65):
                with self.subTest(source_id=source_id), mock.patch.object(archive, '_stage_hour') as stage:
                    with self.assertRaisesRegex(ValueError, 'identity'):
                        archive.copy_hour(source_root, target, 'prod', self.hour, source_id)
                    stage.assert_not_called()
            for hour in (self.hour.replace(tzinfo=None), self.hour + dt.timedelta(minutes=1)):
                with self.assertRaisesRegex(ValueError, 'UTC hour'):
                    archive.copy_hour(source_root, target, 'prod', hour, 'old')

    def test_receipt_from_another_source_or_legacy_cannot_authorize_a_source(self):
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as node1:
            source_root, target = Path(source), Path(node1)
            self.write_source(source_root)
            result = archive.copy_hour(source_root, target, 'prod', self.hour, 'old')
            receipt = archive.receipt_path(target, 'prod', self.hour, 'old')
            for fields in ({'source_id': 'new'}, {'schema_version': archive.SCHEMA},
                           {'environment': 'cn-prod'}, {'manifest_sha256': '0' * 64}):
                receipt.write_text(json.dumps(result | fields))
                with self.assertRaisesRegex(ValueError, 'receipt is invalid'):
                    archive.copy_hour(source_root, target, 'prod', self.hour, 'old')
            self.assertEqual((source_root / 'qa_blobs' / self.hour.strftime(archive.HOUR) / 'one.bin').read_bytes(), b'fixture')

    def test_late_source_files_fail_without_replacing_archive_or_receipt(self):
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as node1:
            source_root, target = Path(source), Path(node1)
            original = self.write_source(source_root)
            result = archive.copy_hour(source_root, target, 'prod', self.hour, 'old')
            receipt = archive.receipt_path(target, 'prod', self.hour, 'old')
            before = receipt.read_bytes()
            late = original.with_name('late.bin')
            late.write_bytes(b'late capture')
            with self.assertRaisesRegex(ValueError, 'receipt is invalid'):
                archive.copy_hour(source_root, target, 'prod', self.hour, 'old')
            self.assertEqual(receipt.read_bytes(), before)
            self.assertEqual(late.read_bytes(), b'late capture')
            published = target / 'qa-raw' / archive.archive_namespace('prod', 'old') / self.hour.strftime(archive.HOUR)
            self.assertEqual(archive.sha256_file(published / 'manifest.json')[0], result['manifest_sha256'])

    def test_interrupted_publish_recovers_only_the_matching_source_snapshot(self):
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as node1:
            source_root, target = Path(source), Path(node1)
            original = self.write_source(source_root)
            first = archive.copy_hour(source_root, target, 'prod', self.hour, 'old')
            receipt = archive.receipt_path(target, 'prod', self.hour, 'old')
            receipt.unlink()
            recovered = archive.copy_hour(source_root, target, 'prod', self.hour, 'old')
            self.assertTrue(recovered['recovered_receipt'])
            self.assertEqual(recovered['manifest_sha256'], first['manifest_sha256'])
            receipt.unlink()
            original.write_bytes(b'late replacement')
            with self.assertRaisesRegex(ValueError, 'stale receipt'):
                archive.copy_hour(source_root, target, 'prod', self.hour, 'old')
            self.assertEqual(original.read_bytes(), b'late replacement')

    def test_run_isolates_source_summary_and_preserves_legacy_summary(self):
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as node1:
            self.write_source(Path(source))
            legacy = Path(node1) / 'receipts' / 'prod' / 'last-run.json'
            archive.atomic_json(legacy, {'legacy': True})
            args = argparse.Namespace(source_root=source, node1_root=node1,
                                      environment='prod', source_id='old',
                                      before='2026-11-11T12:00:00Z', remote_target='')
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(archive.run(args), 0)
            result = json.loads(stdout.getvalue())
            self.assertEqual(result['source_id'], 'old')
            self.assertEqual(result['completed'][0]['source_id'], 'old')
            summary = Path(node1) / 'receipts' / 'prod' / 'sources' / 'old' / 'last-run.json'
            self.assertEqual(json.loads(summary.read_text()), result)
            self.assertEqual(json.loads(legacy.read_text()), {'legacy': True})

    def test_source_receipts_count_toward_qa_capacity(self):
        with tempfile.TemporaryDirectory() as directory:
            receipts = Path(directory) / 'receipts'
            archive.atomic_json(
                archive.receipt_path(receipts, 'prod', self.hour, 'old'),
                {'schema_version': archive.SOURCE_SCHEMA, 'ok': True, 'total_bytes': 11},
            )
            self.assertEqual(archive.receipt_bytes(receipts, kind='qa'), 11)


class ArchiveProgressTest(unittest.TestCase):
    def test_progress_precedes_work_and_preserves_failure_receipts(self):
        hour = dt.datetime(2026, 11, 10, 6, tzinfo=dt.timezone.utc)
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as node1:
            source_root, node1_root = Path(source), Path(node1)
            for selected in (hour, hour + dt.timedelta(hours=1)):
                directory = source_root / 'qa_blobs' / selected.strftime(archive.HOUR)
                directory.mkdir(parents=True)
                (directory / 'one.bin').write_bytes(b'private payload')
            args = argparse.Namespace(source_root=source, node1_root=node1,
                                      environment='prod', before='2026-11-10T08:00:00Z',
                                      remote_target='')
            stderr, stdout = io.StringIO(), io.StringIO()
            original = archive.copy_hour

            def copy(source, target, environment, selected):
                events = [json.loads(line) for line in stderr.getvalue().splitlines()]
                self.assertEqual(events[-1]['phase'], 'start')
                self.assertEqual(events[-1]['window_start'], selected.isoformat())
                if selected == hour:
                    raise ValueError('fixture failure')
                return original(source, target, environment, selected)

            with contextlib.redirect_stderr(stderr), contextlib.redirect_stdout(stdout), \
                 mock.patch.object(archive, 'copy_hour', side_effect=copy):
                self.assertEqual(archive.run(args), 2)
            summary = json.loads(stdout.getvalue())
            self.assertFalse(summary['ok'])
            self.assertEqual(summary['failed'], [{'window_start': hour.isoformat(),
                                                  'error': 'fixture failure'}])
            self.assertEqual(len(summary['completed']), 1)
            events = [json.loads(line) for line in stderr.getvalue().splitlines()]
            self.assertEqual([event['phase'] for event in events],
                             ['start', 'failed', 'start', 'complete'])
            self.assertNotIn('private payload', stderr.getvalue())
            receipt = node1_root / 'receipts' / 'prod' / 'last-run.json'
            self.assertEqual(json.loads(receipt.read_text()), summary)


    def test_orphan_staging_from_a_killed_run_is_removed_on_the_next_run(self):
        # systemd's TimeoutStartSec sends SIGTERM, which skips push_hour's
        # finally; prod left two .push-* directories (993 MB) that way.
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as node1:
            root = Path(node1)
            stale = root / '.push-2026111011-80t9evow'
            fresh = root / '.push-2026111112-abc123'
            unrelated = root / '.push-notes'
            for directory in (stale, fresh, unrelated):
                directory.mkdir()
                (directory / 'manifest.json').write_text('{}')
            stale_file = root / '.push-2026111010-file'
            stale_file.write_text('not a directory')
            receipts = root / 'receipts'
            receipts.mkdir()
            old = time.time() - 3 * 3600
            for path in (stale, unrelated, stale_file):
                os.utime(path, (old, old))
            args = argparse.Namespace(source_root=source, node1_root=node1, environment='prod',
                                      before='2026-11-11T08:00:00Z', remote_target='')
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout):
                self.assertEqual(archive.run(args), 0)
            summary = json.loads(stdout.getvalue())
            self.assertEqual(summary['removed_staging'], ['.push-2026111011-80t9evow'])
            self.assertEqual(summary['staging_cleanup_failed'], [])
            self.assertFalse(stale.exists())
            # A run may overlap a manual one, so recent staging is left alone,
            # and only directories named by push_hour are ever touched.
            self.assertTrue(fresh.is_dir())
            self.assertTrue(unrelated.is_dir())
            self.assertTrue(stale_file.is_file())
            self.assertTrue(receipts.is_dir())

    def test_orphan_cleanup_tolerates_a_missing_root(self):
        with tempfile.TemporaryDirectory() as directory:
            self.assertEqual(archive.remove_orphan_staging(Path(directory) / 'absent'), ([], []))

    def test_a_staging_dir_that_cannot_be_removed_does_not_stop_archiving(self):
        hour = dt.datetime(2026, 11, 11, 6, tzinfo=dt.timezone.utc)
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as node1:
            stale = Path(node1) / '.push-2026111011-stuck'
            stale.mkdir()
            old = time.time() - 3 * 3600
            os.utime(stale, (old, old))
            directory = Path(source) / 'qa_blobs' / hour.strftime(archive.HOUR)
            directory.mkdir(parents=True)
            (directory / 'one.bin').write_bytes(b'payload')
            args = argparse.Namespace(source_root=source, node1_root=node1, environment='prod',
                                      before='2026-11-11T08:00:00Z', remote_target='')
            stdout, stderr = io.StringIO(), io.StringIO()
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr), \
                 mock.patch.object(archive.shutil, 'rmtree', side_effect=PermissionError('denied')):
                self.assertEqual(archive.run(args), 0)
            summary = json.loads(stdout.getvalue())
            self.assertEqual(len(summary['completed']), 1)
            self.assertEqual(summary['removed_staging'], [])
            self.assertEqual([f['name'] for f in summary['staging_cleanup_failed']],
                             ['.push-2026111011-stuck'])
            self.assertIn('qa_archive_staging_cleanup_failed', stderr.getvalue())

    def test_staging_pattern_matches_the_names_push_hour_creates(self):
        hour = dt.datetime(2026, 11, 10, 11, tzinfo=dt.timezone.utc)
        with tempfile.TemporaryDirectory() as directory:
            created = Path(tempfile.mkdtemp(prefix=f".push-{hour.strftime('%Y%m%d%H')}-", dir=directory))
            self.assertRegex(created.name, archive.STAGING_PATTERN)


if __name__ == '__main__':
    unittest.main()
