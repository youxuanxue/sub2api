#!/usr/bin/env python3
"""Behavior tests for restoring the newest real Fleet S3 pg_dump object."""

from __future__ import annotations

import datetime as dt
import gzip
import json
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import pgdump_restore_canary as canary  # noqa: E402

NOW = dt.datetime(2026, 8, 18, 8, 0, tzinfo=dt.timezone.utc)
LIVE_COUNTS = {
    "users": 5,
    "accounts": 8,
    "api_keys": 7,
    "groups": 3,
    "settings": 11,
    "usage_billing_dedup": 14,
}
RESTORED_COUNTS = {
    "users": 3,
    "accounts": 5,
    "api_keys": 4,
    "groups": 2,
    "settings": 8,
    "usage_billing_dedup": 11,
}


class FakeCommands:
    def __init__(
        self,
        *,
        objects: list[dict] | None = None,
        downloaded: bytes | None = None,
        rm_fails: bool = False,
        run_times_out: bool = False,
    ) -> None:
        self.calls: list[list[str]] = []
        self.objects = objects if objects is not None else [
            {
                "Key": "edge/us3/pgdump/tokenkey-20260818T070000Z.sql.gz",
                "LastModified": "2026-08-18T07:02:00Z",
                "Size": 0,
            }
        ]
        sql = b"CREATE TABLE users(id bigint);\n" + b"INSERT INTO users VALUES (1);\n" * 200
        self.downloaded = downloaded if downloaded is not None else gzip.compress(sql)
        if self.objects and self.objects[0].get("Size") == 0:
            self.objects[0]["Size"] = len(self.downloaded)
        self.rm_fails = rm_fails
        self.run_times_out = run_times_out
        self.container_present = False

    def __call__(self, args: list[str], **kwargs: object) -> subprocess.CompletedProcess[str]:
        args = list(args)
        self.calls.append(args)
        joined = " ".join(args)
        stdout = ""
        returncode = 0
        stderr = ""
        if args[:3] == ["aws", "s3api", "list-objects-v2"]:
            stdout = json.dumps({"Contents": self.objects})
        elif args[:3] == ["aws", "s3", "cp"]:
            pathlib.Path(args[4]).write_bytes(self.downloaded)
        elif args[:2] == ["docker", "inspect"] and "{{.State.Running}}" in args:
            stdout = "true\n"
        elif args[:2] == ["docker", "inspect"] and "{{.Config.Image}}" in args:
            stdout = "postgres:18-alpine\n"
        elif args[:2] == ["docker", "inspect"]:
            returncode = 0 if self.container_present else 1
            stderr = "present" if self.container_present else "No such container"
        elif args[:2] == ["docker", "run"]:
            self.container_present = True
            if self.run_times_out:
                raise subprocess.TimeoutExpired(args, 120)
            stdout = "canary-container-id\n"
        elif args[:3] == ["docker", "exec", args[2]] and "pg_isready" in args:
            stdout = "accepting connections\n"
        elif args[:3] == ["docker", "exec", "tokenkey-postgres"] and "json_build_object" in joined:
            stdout = json.dumps(LIVE_COUNTS) + "\n"
        elif args[:2] == ["bash", "-o"] and "gunzip -c" in joined:
            stdout = ""
        elif len(args) >= 3 and args[:2] == ["docker", "exec"] and "json_build_object" in joined:
            stdout = json.dumps(RESTORED_COUNTS) + "\n"
        elif args[:3] == ["docker", "rm", "-f"]:
            if self.rm_fails:
                returncode = 1
                stderr = "rm failed"
            else:
                self.container_present = False
        else:
            raise AssertionError(f"unexpected command: {args!r}")
        return subprocess.CompletedProcess(args, returncode, stdout=stdout, stderr=stderr)


class PgdumpRestoreCanaryTest(unittest.TestCase):
    def run_case(
        self,
        fake: FakeCommands,
        *,
        target: str = "edge:us3",
        s3_uri: str = "s3://tokenkey-prod-pgdump-123/edge/us3/pgdump",
        free_bytes: int = 10 * 1024**3,
        now=None,
    ):
        temp = tempfile.TemporaryDirectory()
        root = pathlib.Path(temp.name)
        env_path = root / "host.env"
        env_path.write_text(f"TOKENKEY_PGDUMP_S3_URI={s3_uri}\n", encoding="utf-8")
        disk_usage = lambda _: shutil._ntuple_diskusage(20 * 1024**3, 0, free_bytes)
        try:
            result = canary.run_canary(
                target,
                receipt_root=root / "canary",
                env_path=env_path,
                run=fake,
                sleep=lambda _: None,
                now=now or (lambda: NOW),
                disk_usage=disk_usage,
            )
            return temp, root, result
        except Exception:
            temp.cleanup()
            raise

    def test_success_restores_newest_s3_object_and_publishes_after_cleanup(self) -> None:
        fake = FakeCommands(
            objects=[
                {"Key": "edge/us3/pgdump/ignore.txt", "LastModified": "2026-08-18T07:59:00Z", "Size": 1},
                {"Key": "edge/us3/pgdump/tokenkey-20260818T060000Z.sql.gz", "LastModified": "2026-08-18T06:01:00Z", "Size": 0},
                {"Key": "edge/us3/pgdump/tokenkey-20260818T070000Z.sql.gz", "LastModified": "2026-08-18T07:02:00Z", "Size": 0},
            ]
        )
        for obj in fake.objects:
            if obj["Key"].endswith(".sql.gz"):
                obj["Size"] = len(fake.downloaded)
        temp, root, result = self.run_case(fake)
        try:
            receipt = json.loads((root / "canary/latest.json").read_text(encoding="utf-8"))
            leftovers = sorted(path.name for path in (root / "canary").iterdir())
        finally:
            temp.cleanup()

        self.assertEqual(result, receipt)
        self.assertEqual(result["source_s3_uri"], "s3://tokenkey-prod-pgdump-123/edge/us3/pgdump/tokenkey-20260818T070000Z.sql.gz")
        self.assertEqual(result["source_last_modified"], "2026-08-18T07:02:00Z")
        self.assertEqual(result["live_counts"], LIVE_COUNTS)
        self.assertEqual(result["restored_counts"], RESTORED_COUNTS)
        self.assertTrue(result["cleanup_verified"])
        self.assertGreater(result["uncompressed_bytes"], result["compressed_bytes"])
        self.assertEqual(len(result["artifact_sha256"]), 64)
        self.assertEqual(leftovers, ["canary.lock", "latest.json"])

        joined = [" ".join(call) for call in fake.calls]
        self.assertTrue(any("s3api list-objects-v2" in call for call in joined))
        self.assertTrue(any("s3 cp s3://tokenkey-prod-pgdump-123/edge/us3/pgdump/tokenkey-20260818T070000Z.sql.gz" in call for call in joined))
        self.assertTrue(any("gunzip -c" in call and "docker exec -i" in call and "ON_ERROR_STOP=1" in call for call in joined))
        self.assertFalse(any("pg_dump" in call for call in joined))

    def test_receipt_completion_time_is_sampled_after_cleanup(self) -> None:
        completed_at = NOW + dt.timedelta(minutes=5)
        clock = iter((NOW, completed_at))

        temp, _, result = self.run_case(FakeCommands(), now=lambda: next(clock))
        try:
            self.assertEqual(result["completed_at"], "2026-08-18T08:05:00Z")
        finally:
            temp.cleanup()

    def test_wrong_target_prefix_fails_before_s3_or_docker(self) -> None:
        fake = FakeCommands()
        with self.assertRaisesRegex(canary.CanaryError, "prefix"):
            self.run_case(fake, s3_uri="s3://bucket/prod/pgdump")
        self.assertEqual(fake.calls, [])

    def test_no_matching_object_fails(self) -> None:
        fake = FakeCommands(objects=[{"Key": "edge/us3/pgdump/readme.txt", "LastModified": "2026-08-18T07:00:00Z", "Size": 12}])
        with self.assertRaisesRegex(canary.CanaryError, "matching"):
            self.run_case(fake)
        self.assertFalse(any(call[:2] == ["docker", "run"] for call in fake.calls))

    def test_stale_object_fails_before_download(self) -> None:
        fake = FakeCommands(objects=[{"Key": "edge/us3/pgdump/tokenkey-20260818T010000Z.sql.gz", "LastModified": "2026-08-18T01:01:00Z", "Size": 2000}])
        with self.assertRaisesRegex(canary.CanaryError, "stale"):
            self.run_case(fake)
        self.assertFalse(any(call[:3] == ["aws", "s3", "cp"] for call in fake.calls))

    def test_insufficient_capacity_aborts_before_docker_run(self) -> None:
        fake = FakeCommands()
        with self.assertRaisesRegex(canary.CanaryError, "capacity"):
            self.run_case(fake, free_bytes=1024)
        self.assertFalse(any(call[:2] == ["docker", "run"] for call in fake.calls))

    def test_s3_size_mismatch_aborts_before_restore(self) -> None:
        fake = FakeCommands()
        fake.objects[0]["Size"] = len(fake.downloaded) + 1
        with self.assertRaisesRegex(canary.CanaryError, "size mismatch"):
            self.run_case(fake)
        self.assertFalse(any(call[:2] == ["docker", "run"] for call in fake.calls))

    def test_container_cleanup_failure_keeps_previous_receipt(self) -> None:
        fake = FakeCommands(rm_fails=True)
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            canary_root = root / "canary"
            canary_root.mkdir()
            receipt = canary_root / "latest.json"
            receipt.write_text('{"previous":true}\n', encoding="utf-8")
            env_path = root / "host.env"
            env_path.write_text("TOKENKEY_PGDUMP_S3_URI=s3://bucket/edge/us3/pgdump\n", encoding="utf-8")
            with self.assertRaisesRegex(canary.CanaryError, "cleanup"):
                canary.run_canary(
                    "edge:us3", receipt_root=canary_root, env_path=env_path,
                    run=fake, sleep=lambda _: None, now=lambda: NOW,
                    disk_usage=lambda _: shutil._ntuple_diskusage(10**10, 0, 10**10),
                )
            self.assertEqual(receipt.read_text(encoding="utf-8"), '{"previous":true}\n')

    def test_docker_run_timeout_removes_container_created_before_timeout(self) -> None:
        fake = FakeCommands(run_times_out=True)
        with self.assertRaisesRegex(canary.CanaryError, "command could not run"):
            self.run_case(fake)
        self.assertFalse(fake.container_present)
        self.assertTrue(any(call[:3] == ["docker", "rm", "-f"] for call in fake.calls))

    def test_download_cleanup_failure_keeps_previous_receipt(self) -> None:
        fake = FakeCommands()
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            canary_root = root / "canary"
            canary_root.mkdir()
            receipt = canary_root / "latest.json"
            receipt.write_text('{"previous":true}\n', encoding="utf-8")
            env_path = root / "host.env"
            env_path.write_text("TOKENKEY_PGDUMP_S3_URI=s3://bucket/edge/us3/pgdump\n", encoding="utf-8")
            with mock.patch.object(canary, "_unlink_dump", side_effect=OSError("unlink denied")):
                with self.assertRaisesRegex(canary.CanaryError, "unlink denied"):
                    canary.run_canary(
                        "edge:us3", receipt_root=canary_root, env_path=env_path,
                        run=fake, sleep=lambda _: None, now=lambda: NOW,
                        disk_usage=lambda _: shutil._ntuple_diskusage(10**10, 0, 10**10),
                    )
            self.assertEqual(receipt.read_text(encoding="utf-8"), '{"previous":true}\n')


if __name__ == "__main__":
    unittest.main(verbosity=2)
