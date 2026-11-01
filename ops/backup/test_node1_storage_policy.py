import collections
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import node1_storage_policy as policy


def usage(total: int, free: int):
    return collections.namedtuple("usage", "total used free")(total, total - free, free)


class Node1StoragePolicyTest(unittest.TestCase):
    def test_85_percent_is_warning_but_90_percent_blocks(self):
        with tempfile.TemporaryDirectory() as root, mock.patch.object(
                policy.shutil, "disk_usage", return_value=usage(100, 15)):
            result = policy.check_write(
                filesystem_root=Path(root), dataset_used_bytes=1,
                required_bytes=0, dataset_cap_bytes=10,
            )
            self.assertTrue(result["warning"])

        with tempfile.TemporaryDirectory() as root, mock.patch.object(
                policy.shutil, "disk_usage", return_value=usage(100, 10)):
            with self.assertRaisesRegex(policy.StoragePolicyError, "write blocked"):
                policy.check_write(
                    filesystem_root=Path(root), dataset_used_bytes=1,
                    required_bytes=0, dataset_cap_bytes=10,
                )

    def test_dataset_cap_is_checked_before_write(self):
        with tempfile.TemporaryDirectory() as root, mock.patch.object(
                policy.shutil, "disk_usage", return_value=usage(100, 90)):
            with self.assertRaisesRegex(policy.StoragePolicyError, "dataset cap exceeded"):
                policy.check_write(
                    filesystem_root=Path(root), dataset_used_bytes=9,
                    required_bytes=2, dataset_cap_bytes=10,
                )

    def test_receipts_are_the_dataset_accounting_source(self):
        with tempfile.TemporaryDirectory() as root:
            receipts = Path(root)
            (receipts / "prod").mkdir()
            (receipts / "prod" / "hour.json").write_text(
                '{"schema_version":"qa-node1-hour-v1","ok":true,"total_bytes":12}\n',
                encoding="utf-8",
            )
            (receipts / "prod" / "bad.json").write_text(
                '{"schema_version":"qa-node1-hour-v1","ok":false,"total_bytes":99}\n',
                encoding="utf-8",
            )
            self.assertEqual(policy.receipt_bytes(receipts, kind="qa"), 12)


if __name__ == "__main__":
    unittest.main()
