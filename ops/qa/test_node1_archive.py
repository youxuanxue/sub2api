import argparse
import contextlib
import datetime as dt
import io
import importlib.util
import json
from pathlib import Path
import tempfile
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


if __name__ == '__main__':
    unittest.main()
