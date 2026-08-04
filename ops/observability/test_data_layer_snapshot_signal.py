#!/usr/bin/env python3
"""Behavior tests for the data-volume snapshot signal owner."""

from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest


_SCRIPT = pathlib.Path(__file__).resolve().parent / "data_layer_snapshot_signal.sh"


class DataLayerSnapshotSignalTest(unittest.TestCase):
    def run_signal(self, *, volume_id: str, snapshot_at: str = "2026-08-04T01:02:03Z") -> tuple[subprocess.CompletedProcess[str], list[str]]:
        with tempfile.TemporaryDirectory() as td:
            root = pathlib.Path(td)
            fakebin = root / "bin"
            fakebin.mkdir()
            calls = root / "aws-calls.log"
            aws = fakebin / "aws"
            aws.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    set -u
                    printf '%s\n' "$*" >> "$FAKE_AWS_CALLS"
                    if [ "$1 $2" = "cloudformation describe-stacks" ]; then
                      printf '%s\n' "$FAKE_DATA_VOLUME_ID"
                      exit 0
                    fi
                    if [ "$1 $2" = "ec2 describe-snapshots" ]; then
                      printf '%s\n' "$FAKE_SNAPSHOT_AT"
                      exit 0
                    fi
                    exit 2
                    """
                ),
                encoding="utf-8",
            )
            aws.chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{fakebin}:{os.environ.get('PATH', '')}",
                "FAKE_AWS_CALLS": str(calls),
                "FAKE_DATA_VOLUME_ID": volume_id,
                "FAKE_SNAPSHOT_AT": snapshot_at,
            }
            proc = subprocess.run(
                ["bash", str(_SCRIPT), "us-east-1", "tokenkey-prod-stage0"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            captured = calls.read_text(encoding="utf-8").splitlines() if calls.exists() else []
            return proc, captured

    def test_queries_latest_completed_snapshot_for_stack_data_volume(self) -> None:
        proc, calls = self.run_signal(volume_id="vol-data")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertEqual(json.loads(proc.stdout), {"latest_snapshot_at": "2026-08-04T01:02:03Z"})
        self.assertTrue(any("OutputKey==`DataVolumeId`" in call for call in calls), msg=calls)
        snapshot_call = next(call for call in calls if call.startswith("ec2 describe-snapshots"))
        self.assertIn("Name=volume-id,Values=vol-data", snapshot_call)
        self.assertIn("Name=status,Values=completed", snapshot_call)
        self.assertFalse(any("describe-instances" in call for call in calls))

    def test_missing_data_volume_is_null_without_snapshot_query(self) -> None:
        proc, calls = self.run_signal(volume_id="None")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertEqual(json.loads(proc.stdout), {"latest_snapshot_at": None})
        self.assertFalse(any("describe-snapshots" in call for call in calls), msg=calls)

    def test_missing_completed_snapshot_is_null(self) -> None:
        proc, calls = self.run_signal(volume_id="vol-data", snapshot_at="None")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertEqual(json.loads(proc.stdout), {"latest_snapshot_at": None})
        self.assertTrue(any("describe-snapshots" in call for call in calls), msg=calls)


if __name__ == "__main__":
    unittest.main()
