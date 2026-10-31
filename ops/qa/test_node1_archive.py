import datetime as dt
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

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

    def test_selection_skips_open_hour(self):
        hour = dt.datetime(2026, 10, 31, 10, tzinfo=dt.timezone.utc)
        with tempfile.TemporaryDirectory() as source:
            root = Path(source)
            (root / 'qa_blobs' / hour.strftime(archive.HOUR)).mkdir(parents=True)
            self.assertEqual(archive.hours_to_copy(root, hour + dt.timedelta(minutes=59)), [])
            self.assertEqual(archive.hours_to_copy(root, hour + dt.timedelta(hours=1)), [hour])


if __name__ == '__main__':
    unittest.main()
