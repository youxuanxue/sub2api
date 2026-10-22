import datetime as dt
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = str(Path(__file__).with_name("compare-pg-migration-snapshots.py"))


def snapshot(**overrides):
    value = {
        "kind": "pg_migration_snapshot",
        "at": (dt.datetime.now(dt.timezone.utc) - dt.timedelta(seconds=1)).isoformat(),
        "system_identifier": "123",
        "checkpoint_timeline": 1,
        "in_recovery": False,
        "current_lsn": "1/200",
        "replay_lsn": None,
        "archiver": {"failed_count": 0},
    }
    value.update(overrides)
    return value


def run(tmp_path, source, target):
    a, b = tmp_path / "a.json", tmp_path / "b.json"
    a.write_text(json.dumps(source))
    b.write_text(json.dumps(target))
    return subprocess.run([sys.executable, SCRIPT, str(a), str(b)], text=True, capture_output=True)


class SnapshotComparisonTest(unittest.TestCase):
    def test_comparable_same_timeline_recovery(self):
        with tempfile.TemporaryDirectory() as directory:
            tmp_path = Path(directory)
            target = snapshot(in_recovery=True, current_lsn=None, replay_lsn="1/180")
            result = run(tmp_path, snapshot(), target)
            self.assertEqual(result.returncode, 0)
            self.assertEqual(json.loads(result.stdout)["observed_lsn_gap_bytes"], 128)


    def test_rejects_wrong_timeline(self):
        with tempfile.TemporaryDirectory() as directory:
            result = run(Path(directory), snapshot(), snapshot(in_recovery=True, replay_lsn="1/180", checkpoint_timeline=2))
            self.assertEqual(result.returncode, 2)
            self.assertIn("timeline mismatch", result.stderr)


    def test_rejects_primary_target(self):
        with tempfile.TemporaryDirectory() as directory:
            result = run(Path(directory), snapshot(), snapshot(in_recovery=False, replay_lsn="1/180"))
            self.assertEqual(result.returncode, 2)
            self.assertIn("not in recovery", result.stderr)

    def test_target_ahead_remains_observation(self):
        with tempfile.TemporaryDirectory() as directory:
            result = run(Path(directory), snapshot(), snapshot(in_recovery=True, replay_lsn="1/300"))
            self.assertEqual(result.returncode, 0)
            report = json.loads(result.stdout)
            self.assertEqual(report["observed_lsn_gap_bytes"], -256)
            self.assertFalse(report["cutover_authorized"])

    def test_rejects_invalid_or_stale_evidence(self):
        cases = [
            ({"system_identifier": "456"}, "system identifier mismatch"),
            ({"system_identifier": None}, "invalid system identifier"),
            ({"at": "2000-01-01T00:00:00+00:00"}, "stale or future"),
            ({"at": (dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=1)).isoformat()}, "stale or future"),
            ({"at": "2026-10-21T12:00:00"}, "requires timezone"),
            ({"replay_lsn": "1/100000000"}, "invalid LSN"),
            ({"replay_lsn": "-1/100"}, "invalid LSN"),
            ({"replay_lsn": None}, "are required"),
            ({"in_recovery": "false"}, "not in recovery"),
        ]
        for overrides, error in cases:
            with self.subTest(overrides=overrides), tempfile.TemporaryDirectory() as directory:
                target = snapshot(in_recovery=True, replay_lsn="1/180")
                target.update(overrides)
                result = run(Path(directory), snapshot(), target)
                self.assertEqual(result.returncode, 2)
                self.assertIn(error, result.stderr)
                self.assertEqual(result.stdout, "")

    def test_rejects_two_equally_old_snapshots(self):
        with tempfile.TemporaryDirectory() as directory:
            old = "2000-01-01T00:00:00+00:00"
            result = run(Path(directory), snapshot(at=old), snapshot(at=old, in_recovery=True, replay_lsn="1/180"))
            self.assertEqual(result.returncode, 2)
            self.assertIn("stale or future", result.stderr)

    def test_rejects_standby_source(self):
        with tempfile.TemporaryDirectory() as directory:
            result = run(Path(directory), snapshot(in_recovery=True), snapshot(in_recovery=True, replay_lsn="1/180"))
            self.assertEqual(result.returncode, 2)
            self.assertIn("source is not a primary", result.stderr)


if __name__ == "__main__":
    unittest.main()
