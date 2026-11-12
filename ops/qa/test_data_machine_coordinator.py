from __future__ import annotations

import datetime as dt
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent))
import data_machine_coordinator as coordinator
import source_cleanup


HOUR = dt.datetime(2026, 11, 12, 10, tzinfo=dt.timezone.utc)


def write_receipt(root: Path, source: str, hour: dt.datetime = HOUR) -> None:
    path = coordinator.receipt_path(root, "prod", source, hour)
    path.parent.mkdir(parents=True)
    path.write_text(json.dumps({
        "schema_version": coordinator.SOURCE_SCHEMA,
        "environment": "prod",
        "source_id": source,
        "window_start": hour.isoformat(),
        "window_end": (hour + dt.timedelta(hours=1)).isoformat(),
        "ok": True,
        "manifest_sha256": "a" * 64,
        "seal_digest": "b" * 64,
        "file_count": 1,
        "total_bytes": 7,
    }))


class CoordinatorTest(unittest.TestCase):
    def test_missing_a_does_not_block_b_source_cleanup_but_holds_shared_partition(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_receipt(root, "b")
            plan = coordinator.build_plan(
                receipt_root=root, environment="prod", hour=HOUR,
                expected_sources=["a", "b"], now=HOUR + dt.timedelta(hours=26),
            )
            self.assertEqual(plan["source_cleanup_allowed"], ["b"])
            self.assertEqual(plan["shared_partition_action"], "hold")

    def test_explicit_gap_deadline_allows_shared_delete(self):
        with tempfile.TemporaryDirectory() as tmp:
            plan = coordinator.build_plan(
                receipt_root=Path(tmp), environment="prod", hour=HOUR,
                expected_sources=["a", "b"], now=HOUR + dt.timedelta(hours=26),
                gap_after=dt.timedelta(hours=24), allow_gap=True,
            )
            self.assertEqual(plan["shared_partition_action"], "gap-delete")
            self.assertEqual(plan["gap_policy"], "approved-after-deadline")

    def test_plan_is_immutable(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "plan.json"
            plan = coordinator.build_plan(
                receipt_root=Path(tmp), environment="prod", hour=HOUR,
                expected_sources=["a"], now=HOUR,
            )
            coordinator.write_plan(path, plan)
            coordinator.write_plan(path, plan)
            changed = dict(plan, shared_partition_action="delete")
            with self.assertRaisesRegex(coordinator.CoordinatorError, "overwrite"):
                coordinator.write_plan(path, changed)


class SourceCleanupTest(unittest.TestCase):
    def test_source_cleanup_removes_only_authorized_source_hour(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_receipt(root, "b")
            plan = coordinator.build_plan(
                receipt_root=root, environment="prod", hour=HOUR,
                expected_sources=["a", "b"], now=HOUR + dt.timedelta(hours=26),
            )
            plan_path = root / "plan.json"
            coordinator.write_plan(plan_path, plan)
            source = root / "source"
            for kind in ("qa_blobs", "qa_dlq"):
                directory = source / kind / HOUR.strftime("%Y/%m/%d/%H")
                directory.mkdir(parents=True)
                (directory / "one.bin").write_bytes(b"fixture")
            manifest = source_cleanup.archive._source_manifest(
                source_cleanup.archive._hour_sources(source, HOUR), "prod", HOUR, "b")
            plan["sources"]["b"]["manifest_sha256"] = hashlib.sha256(source_cleanup.archive.manifest_bytes(manifest)).hexdigest()
            unsigned = {key: value for key, value in plan.items() if key != "plan_sha256"}
            plan["plan_sha256"] = hashlib.sha256(json.dumps(unsigned, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
            plan_path.write_text(json.dumps(plan))
            check = lambda hour: {"state_digest": "b" * 64}
            real_remove = source_cleanup.remove_hour
            count = 0
            def interrupted_remove(root, relative):
                nonlocal count
                count += 1
                if count == 2:
                    raise OSError('interrupted cleanup')
                real_remove(root, relative)
            with mock.patch.object(source_cleanup, 'remove_hour', side_effect=interrupted_remove):
                with self.assertRaisesRegex(OSError, 'interrupted'):
                    source_cleanup.apply_source_cleanup(plan_path, source, "b", apply=True, seal_check=check,
                                                       now=HOUR + dt.timedelta(hours=26))
            self.assertFalse((source / "qa_blobs" / HOUR.strftime("%Y/%m/%d/%H")).exists())
            self.assertTrue((source / "qa_dlq" / HOUR.strftime("%Y/%m/%d/%H")).exists())
            result = source_cleanup.apply_source_cleanup(plan_path, source, "b", apply=True, seal_check=check,
                                                         now=HOUR + dt.timedelta(hours=26))
            self.assertEqual(result["action"], "delete")
            self.assertFalse((source / "qa_blobs" / HOUR.strftime("%Y/%m/%d/%H")).exists())
            self.assertFalse((source / "qa_dlq" / HOUR.strftime("%Y/%m/%d/%H")).exists())
            held = source_cleanup.apply_source_cleanup(plan_path, source, "a", apply=True, seal_check=check,
                                                       now=HOUR + dt.timedelta(hours=26))
            self.assertEqual(held["action"], "hold")
            late = source / 'qa_blobs' / HOUR.strftime('%Y/%m/%d/%H')
            late.mkdir(parents=True)
            (late / 'one.bin').write_bytes(b'fixture')
            with self.assertRaisesRegex(coordinator.CoordinatorError, 'late'):
                source_cleanup.apply_source_cleanup(plan_path, source, "b", apply=True, seal_check=check,
                                                    now=HOUR + dt.timedelta(hours=26))

    def test_symlink_hour_never_deletes_outside_source(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            outside = root / 'outside'
            outside.mkdir()
            protected = outside / 'keep'
            protected.write_bytes(b'keep')
            source = root / 'source'
            target = source / 'qa_blobs/2026/11/12/10'
            target.parent.mkdir(parents=True)
            target.symlink_to(outside, target_is_directory=True)
            with self.assertRaises(coordinator.CoordinatorError):
                source_cleanup.remove_hour(source, target.relative_to(source))
            self.assertEqual(protected.read_bytes(), b'keep')

    def test_source_cleanup_rejects_tampered_plan(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_receipt(root, "b")
            plan = coordinator.build_plan(
                receipt_root=root, environment="prod", hour=HOUR,
                expected_sources=["b"], now=HOUR,
            )
            plan_path = root / "plan.json"
            coordinator.write_plan(plan_path, plan)
            tampered = dict(plan, environment="cn-prod")
            plan_path.write_text(json.dumps(tampered))
            with self.assertRaisesRegex(coordinator.CoordinatorError, "hash"):
                source_cleanup.apply_source_cleanup(plan_path, root, "b", apply=True)


class SafetyTest(unittest.TestCase):
    def test_unexpired_hour_cannot_authorize_source_or_shared_cleanup(self):
        with tempfile.TemporaryDirectory() as tmp:
            write_receipt(Path(tmp), "b")
            plan = coordinator.build_plan(receipt_root=Path(tmp), environment="prod", hour=HOUR,
                                          expected_sources=["b"], now=HOUR + dt.timedelta(hours=24))
            self.assertEqual(plan["source_cleanup_allowed"], [])
            self.assertEqual(plan["shared_partition_action"], "hold")

    def test_missing_seal_cannot_authorize_cleanup(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_receipt(root, "b")
            path = coordinator.receipt_path(root, "prod", "b", HOUR)
            value = json.loads(path.read_text())
            del value["seal_digest"]
            path.write_text(json.dumps(value))
            plan = coordinator.build_plan(receipt_root=root, environment="prod", hour=HOUR,
                                          expected_sources=["b"], now=HOUR + dt.timedelta(hours=30))
            self.assertEqual(plan["shared_partition_action"], "hold")
            self.assertEqual(plan["source_cleanup_allowed"], [])

    def test_complete_sources_authorize_shared_cleanup(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_receipt(root, "a")
            write_receipt(root, "b")
            plan = coordinator.build_plan(receipt_root=root, environment="prod", hour=HOUR,
                                          expected_sources=["a", "b"], now=HOUR + dt.timedelta(hours=30))
            self.assertEqual(plan["shared_partition_action"], "delete")
            self.assertEqual(plan["source_cleanup_allowed"], ["a", "b"])


if __name__ == "__main__":
    unittest.main()
