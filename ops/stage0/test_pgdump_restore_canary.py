#!/usr/bin/env python3
"""Behavior tests for the isolated Fleet pg_dump restore canary."""

from __future__ import annotations

import json
import pathlib
import subprocess
import sys
import tempfile
import unittest


HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import pgdump_restore_canary as canary  # noqa: E402


BEFORE = {
    "users": 3,
    "accounts": 5,
    "api_keys": 4,
    "groups": 2,
    "settings": 8,
    "usage_billing_dedup": 11,
}


class FakeDocker:
    def __init__(self, *, restored: dict[str, int] | None = None) -> None:
        self.calls: list[list[str]] = []
        self.live_count_calls = 0
        self.restored = restored if restored is not None else dict(BEFORE)

    def __call__(self, args: list[str], **kwargs: object) -> subprocess.CompletedProcess[str]:
        args = list(args)
        self.calls.append(args)
        joined = " ".join(args)
        stdout = ""
        if args[:2] == ["docker", "inspect"] and "{{.State.Running}}" in args:
            stdout = "true\n"
        elif args[:2] == ["docker", "inspect"] and "{{.Config.Image}}" in args:
            stdout = "postgres:18-alpine\n"
        elif args[:3] == ["docker", "cp", args[2]] and args[2].startswith(
            "tokenkey-postgres:/tmp/"
        ):
            pathlib.Path(args[3]).write_bytes(b"PGDMP" + b"x" * 4091)
        elif args[:2] == ["docker", "run"]:
            stdout = "canary-container-id\n"
        elif args[:3] == ["docker", "exec", args[2]] and "pg_isready" in args:
            stdout = "/var/run/postgresql:5432 - accepting connections\n"
        elif args[:3] == ["docker", "exec", "tokenkey-postgres"] and "json_build_object" in joined:
            self.live_count_calls += 1
            stdout = json.dumps(BEFORE) + "\n"
        elif len(args) >= 3 and args[:2] == ["docker", "exec"] and "json_build_object" in joined:
            stdout = json.dumps(self.restored) + "\n"
        return subprocess.CompletedProcess(args, 0, stdout=stdout, stderr="")


class PgdumpRestoreCanaryTest(unittest.TestCase):
    def test_success_restores_precious_tables_and_persists_receipt(self) -> None:
        fake = FakeDocker()
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            result = canary.run_canary(
                "edge:us3", receipt_root=root, run=fake, sleep=lambda _: None
            )
            persisted = json.loads((root / "latest.json").read_text(encoding="utf-8"))
            leftovers = sorted(path.name for path in root.iterdir())

        self.assertEqual(result, persisted)
        self.assertEqual(result["mode"], "pgdump_restore_canary")
        self.assertEqual(result["target"], "edge:us3")
        self.assertEqual(result["source_counts"], BEFORE)
        self.assertEqual(result["restored_counts"], BEFORE)
        self.assertEqual(result["artifact_bytes"], 4096)
        self.assertEqual(len(result["artifact_sha256"]), 64)
        self.assertFalse(result["source_mutated"])
        self.assertFalse(result["deletion_authorized"])
        self.assertEqual(leftovers, ["canary.lock", "latest.json"])

        pg_dump = next(call for call in fake.calls if "pg_dump" in call)
        self.assertIn("--exclude-table-data=usage_logs*", pg_dump)
        self.assertIn("--exclude-table-data=ops_system_logs*", pg_dump)
        run = next(call for call in fake.calls if call[:2] == ["docker", "run"])
        self.assertIn("--network=none", run)
        self.assertIn("--cpus=0.50", run)
        self.assertIn("--memory=640m", run)
        self.assertIn("--pull=never", run)
        self.assertIn("postgres:18-alpine", run)

    def test_restored_count_outside_source_window_fails_and_keeps_last_success(self) -> None:
        restored = dict(BEFORE)
        restored["accounts"] = 4
        fake = FakeDocker(restored=restored)
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            receipt = root / "latest.json"
            receipt.write_text('{"previous":true}\n', encoding="utf-8")
            with self.assertRaisesRegex(canary.CanaryError, "accounts"):
                canary.run_canary(
                    "prod", receipt_root=root, run=fake, sleep=lambda _: None
                )
            self.assertEqual(receipt.read_text(encoding="utf-8"), '{"previous":true}\n')

        cleanup = [call for call in fake.calls if call[:3] == ["docker", "rm", "-f"]]
        self.assertEqual(len(cleanup), 1)

    def test_invalid_target_fails_before_docker(self) -> None:
        fake = FakeDocker()
        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaisesRegex(canary.CanaryError, "target"):
                canary.run_canary(
                    "edge:../../prod",
                    receipt_root=pathlib.Path(temp),
                    run=fake,
                    sleep=lambda _: None,
                )
        self.assertEqual(fake.calls, [])


if __name__ == "__main__":
    unittest.main(verbosity=2)
