import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import cos_node1_mirror as mirror


class CosNode1MirrorTest(unittest.TestCase):
    def test_local_copy_publishes_receipt_after_hash_verification(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "source"
            destination = root / "node1"
            source.mkdir()
            (source / "part-000").write_bytes(b"wal bytes")
            args = mirror.parse_args([
                "copy", "--namespace", "db", "--snapshot", "2026-10-31T00-00Z",
                "--source", str(source), "--destination", str(destination),
            ])
            self.assertEqual(mirror.main([
                "copy", "--namespace", "db", "--snapshot", "plan-only",
                "--source", str(source), "--destination", str(destination),
            ]), 0)
            self.assertFalse((destination / "db").exists())
            result = mirror.copy_snapshot(args)
            published = destination / "db" / "2026-10-31T00-00Z"
            self.assertEqual(result["status"], "copied")
            self.assertTrue((published / "_receipt.json").is_file())
            mirror.verify_receipt(published, published / "_receipt.json")
            self.assertEqual((published / "part-000").read_bytes(), b"wal bytes")

    def test_existing_snapshot_is_immutable_and_replay_is_idempotent(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "source"
            destination = root / "node1"
            source.mkdir()
            (source / "payload").write_bytes(b"one")
            argv = ["copy", "--namespace", "db", "--snapshot", "full-1",
                    "--source", str(source), "--destination", str(destination)]
            mirror.copy_snapshot(mirror.parse_args(argv))
            (source / "payload").write_bytes(b"two")
            replay = mirror.copy_snapshot(mirror.parse_args(argv))
            self.assertEqual(replay["status"], "already_verified")
            self.assertEqual((destination / "db" / "full-1" / "payload").read_bytes(), b"one")

    def test_checksum_tamper_fails_verification(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "source"
            destination = root / "node1"
            source.mkdir()
            (source / "payload").write_bytes(b"one")
            argv = ["copy", "--namespace", "qa", "--snapshot", "hour-1",
                    "--source", str(source), "--destination", str(destination)]
            mirror.copy_snapshot(mirror.parse_args(argv))
            published = destination / "qa" / "hour-1"
            (published / "payload").write_bytes(b"tampered")
            with self.assertRaisesRegex(mirror.MirrorError, "checksum mismatch"):
                mirror.verify_receipt(published, published / "_receipt.json")

    def test_symlinked_source_file_is_rejected_and_namespaces_are_separate(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "source"
            destination = root / "node1"
            source.mkdir()
            outside = root / "outside"
            outside.write_bytes(b"must not follow")
            (source / "escape").symlink_to(outside)
            args = mirror.parse_args(["copy", "--namespace", "qa", "--snapshot", "hour-1",
                                      "--source", str(source), "--destination", str(destination)])
            with self.assertRaisesRegex(mirror.MirrorError, "symlinks are not allowed"):
                mirror.copy_snapshot(args)
            self.assertFalse((destination / "qa").exists())
            self.assertEqual(mirror.validate_namespace("db"), "db")
            self.assertEqual(mirror.validate_namespace("qa"), "qa")
            with self.assertRaises(mirror.MirrorError):
                mirror.validate_namespace("db/qa")

    def test_remote_requires_capacity_estimate_and_uses_bounded_rclone(self):
        with self.assertRaises(SystemExit):
            mirror.parse_args(["copy", "--namespace", "db", "--snapshot", "snap",
                               "--source-uri", "cos:bucket/path", "--destination", "/tmp/node1"])
        with tempfile.TemporaryDirectory() as tmp, mock.patch.object(mirror, "run_rclone") as rclone:
            args = mirror.parse_args(["copy", "--namespace", "db", "--snapshot", "snap",
                                      "--source-uri", "cos:bucket/path", "--expected-bytes", "4",
                                      "--destination", tmp, "--bwlimit", "1M"])
            mirror.copy_snapshot(args)
            rclone.assert_called_once()
            self.assertEqual(rclone.call_args.kwargs["bwlimit"], "1M")

    def test_capacity_floor_rejects_low_free_space(self):
        with tempfile.TemporaryDirectory() as tmp, mock.patch.object(
                mirror.shutil, "disk_usage", return_value=shutil_usage(100, 10, 10)):
            with self.assertRaisesRegex(mirror.MirrorError, "below 20.0% floor"):
                mirror.check_capacity(Path(tmp), 1, min_free_percent=20, reserve_multiplier=1.25)


def shutil_usage(total: int, used: int, free: int):
    import collections
    return collections.namedtuple("usage", "total used free")(total, used, free)


if __name__ == "__main__":
    unittest.main()
