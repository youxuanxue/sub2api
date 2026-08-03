#!/usr/bin/env python3
"""Behavior tests for the explicit usage_logs daily partition operator."""

from __future__ import annotations

import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


_DIR = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(_DIR))

import usage_logs_daily_partition as control  # noqa: E402
import usage_logs_daily_partition_remote as remote  # noqa: E402


_INSTANCE = "i-0123456789abcdef0"
_UPPER = "2026-08-05T00:00:00Z"
_CONFIRM = remote.CUTOVER_CONFIRMATION_PREFIX + _UPPER


def _prepare_receipt() -> dict:
    return {
        "mode": "prod_usage_logs_daily_partition_prepare",
        "environment": "prod",
        "instance_id": _INSTANCE,
        "legacy_upper_exclusive": _UPPER,
        "row_count_before": 6_000_000,
        "bound_validated": True,
        "required_cutover_confirmation": _CONFIRM,
        "source_rows_copied": False,
        "deletion_authorized": False,
    }


class UsageLogsDailyPartitionTest(unittest.TestCase):
    def test_cutover_sql_has_short_lock_and_no_data_copy(self) -> None:
        sql = remote.build_cutover_sql(_UPPER)
        self.assertIn("lock_timeout = '5s'", sql)
        self.assertIn("LOCK TABLE usage_logs IN ACCESS EXCLUSIVE MODE", sql)
        self.assertIn("ATTACH PARTITION usage_logs_legacy", sql)
        self.assertIn("PARTITION BY RANGE (created_at)", sql)
        self.assertIn("obj_description(oid, 'pg_constraint')", sql)
        self.assertIn("usage_logs_partition_index_map", sql)
        self.assertIn("contype = 'c'", sql)
        self.assertNotIn("INSERT INTO usage_logs SELECT", sql)
        self.assertNotIn("DROP TABLE usage_logs_legacy", sql)

    def test_cutover_refuses_confirmation_not_bound_to_upper(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            prepare_path = root / "prepare.json"
            prepare_path.write_text(json.dumps(_prepare_receipt()), encoding="utf-8")
            with mock.patch.object(control, "_run_remote") as run_remote:
                with self.assertRaisesRegex(control.UsagePartitionControlError, "exactly match"):
                    control.cutover(
                        prepare_path,
                        root / "cutover.json",
                        remote.CUTOVER_CONFIRMATION_PREFIX + "2026-08-06T00:00:00Z",
                    )
            run_remote.assert_not_called()

    def test_cutover_persists_verified_row_counts(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            prepare_path = root / "prepare.json"
            cutover_path = root / "cutover.json"
            prepare_path.write_text(json.dumps(_prepare_receipt()), encoding="utf-8")
            remote_result = {
                "mode": "prod_usage_logs_daily_partition_cutover",
                "environment": "prod",
                "instance_id": _INSTANCE,
                "legacy_upper_exclusive": _UPPER,
                "source_rows_copied": False,
                "deletion_authorized": False,
                "verification": {
                    "partitioned": True,
                    "legacy_attached": True,
                    "no_parent_global_unique": True,
                    "no_incoming_legacy_fk": True,
                    "constraints_preserved": True,
                    "legacy_row_count": 6_000_100,
                    "parent_row_count": 6_000_100,
                },
            }
            with mock.patch.object(control, "_run_remote", return_value=remote_result):
                result = control.cutover(prepare_path, cutover_path, _CONFIRM)
            self.assertEqual(result["row_count_before"], 6_000_000)
            self.assertEqual(
                json.loads(cutover_path.read_text(encoding="utf-8"))["verification"]["parent_row_count"],
                6_000_100,
            )

    def test_raw_usage_conflict_target_removed_but_billing_dedup_kept(self) -> None:
        repo = (_DIR.parents[1] / "backend" / "internal" / "repository" / "usage_log_repo_insert.go").read_text(encoding="utf-8")
        billing = (_DIR.parents[1] / "backend" / "internal" / "repository" / "usage_billing_repo.go").read_text(encoding="utf-8")
        self.assertNotIn("ON CONFLICT (request_id, api_key_id) DO NOTHING", repo)
        self.assertIn("ON CONFLICT (request_id, api_key_id) DO NOTHING", billing)


class UsageLogsDailyPartitionPostgresTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        commands = {
            name: shutil.which(name)
            for name in ("initdb", "pg_ctl", "createdb", "psql")
        }
        if any(path is None for path in commands.values()):
            raise unittest.SkipTest("local PostgreSQL binaries are unavailable")
        if hasattr(os, "geteuid") and os.geteuid() == 0:
            raise unittest.SkipTest("initdb refuses to run as root")
        cls.commands = commands
        cls.temporary = tempfile.TemporaryDirectory(prefix="usage-partition-pg-")
        cls.addClassCleanup(cls.temporary.cleanup)
        cls.root = pathlib.Path(cls.temporary.name)
        cls.data = cls.root / "data"
        subprocess.run(
            [
                commands["initdb"],
                "-D",
                str(cls.data),
                "-A",
                "trust",
                "-U",
                "tokenkey",
                "--no-locale",
            ],
            check=True,
            capture_output=True,
            text=True,
        )
        cls.addClassCleanup(cls._stop_postgres)
        subprocess.run(
            [
                commands["pg_ctl"],
                "-D",
                str(cls.data),
                "-o",
                f"-c listen_addresses='' -k {cls.root} -p 5432",
                "-w",
                "start",
            ],
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        subprocess.run(
            [
                commands["createdb"],
                "-h",
                str(cls.root),
                "-p",
                "5432",
                "-U",
                "tokenkey",
                "tokenkey",
            ],
            check=True,
            capture_output=True,
            text=True,
        )

    @classmethod
    def _stop_postgres(cls) -> None:
        subprocess.run(
            [cls.commands["pg_ctl"], "-D", str(cls.data), "-m", "fast", "stop"],
            check=False,
            capture_output=True,
            text=True,
        )

    @classmethod
    def _psql(cls, sql: str, *, timeout_seconds: int = 360) -> str:
        completed = subprocess.run(
            [
                cls.commands["psql"],
                "-h",
                str(cls.root),
                "-p",
                "5432",
                "-U",
                "tokenkey",
                "-d",
                "tokenkey",
                "-X",
                "-A",
                "-t",
                "-v",
                "ON_ERROR_STOP=1",
                "-c",
                sql,
            ],
            capture_output=True,
            text=True,
            timeout=timeout_seconds,
            check=False,
        )
        if completed.returncode != 0:
            raise remote.UsagePartitionError(
                "PostgreSQL migration command failed: "
                + (completed.stderr or completed.stdout)
            )
        return completed.stdout.strip()

    def test_real_postgres_attach_preserves_constraints_and_utc_days(self) -> None:
        self._psql(
            """
ALTER DATABASE tokenkey SET timezone TO 'Asia/Shanghai';
SET TIME ZONE 'Asia/Shanghai';
CREATE TABLE users (id bigint PRIMARY KEY);
CREATE TABLE api_keys (id bigint PRIMARY KEY);
CREATE TABLE accounts (id bigint PRIMARY KEY);
CREATE TABLE usage_logs (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  api_key_id bigint NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
  account_id bigint NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  request_id varchar(64),
  model varchar(100) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT usage_logs_model_check CHECK (model <> '')
);
CREATE INDEX idx_usage_logs_user_created ON usage_logs (user_id, created_at);
CREATE INDEX idx_usage_logs_partial ON usage_logs (model) WHERE model <> 'unused';
CREATE UNIQUE INDEX idx_usage_logs_request_id_api_key_unique
  ON usage_logs (request_id, api_key_id);
CREATE TABLE billing_usage_entries (
  id bigserial PRIMARY KEY,
  usage_log_id bigint NOT NULL REFERENCES usage_logs(id) ON DELETE CASCADE
);
INSERT INTO users VALUES (1);
INSERT INTO api_keys VALUES (1);
INSERT INTO accounts VALUES (1);
INSERT INTO usage_logs(user_id, api_key_id, account_id, request_id, model, created_at)
VALUES (1, 1, 1, 'req-1', 'model', now() - interval '1 day');
INSERT INTO billing_usage_entries(usage_log_id) VALUES (1);
"""
        )
        with mock.patch.object(remote, "_psql", side_effect=self._psql):
            prepared = remote.prepare(remote.PREPARE_CONFIRMATION)
            result = remote.cutover(
                prepared["legacy_upper_exclusive"],
                remote.CUTOVER_CONFIRMATION_PREFIX
                + prepared["legacy_upper_exclusive"],
            )

        verification = result["verification"]
        self.assertEqual(verification["legacy_row_count"], 1)
        self.assertEqual(verification["parent_row_count"], 1)
        self.assertTrue(verification["constraints_preserved"])
        self.assertTrue(verification["no_incoming_legacy_fk"])
        upper = prepared["legacy_upper_exclusive"]
        self._psql(
            "INSERT INTO usage_logs"
            "(user_id, api_key_id, account_id, request_id, model, created_at) "
            f"VALUES (1, 1, 1, 'req-2', 'model', TIMESTAMPTZ '{upper}' + interval '1 hour') "
            "ON CONFLICT DO NOTHING"
        )
        with self.assertRaises(remote.UsagePartitionError):
            self._psql(
                "INSERT INTO usage_logs"
                "(user_id, api_key_id, account_id, model, created_at) "
                f"VALUES (1, 1, 1, '', TIMESTAMPTZ '{upper}' + interval '2 hours')"
            )
        with self.assertRaises(remote.UsagePartitionError):
            self._psql(
                "INSERT INTO usage_logs"
                "(user_id, api_key_id, account_id, model, created_at) "
                f"VALUES (999, 1, 1, 'model', TIMESTAMPTZ '{upper}' + interval '2 hours')"
            )
        counts = json.loads(
            self._psql(
                "SELECT row_to_json(v) FROM (SELECT count(*) AS rows, "
                "count(*) FILTER (WHERE tableoid = 'usage_logs_legacy'::regclass) "
                "AS legacy_rows FROM usage_logs) v"
            )
        )
        self.assertEqual(counts, {"rows": 2, "legacy_rows": 1})


if __name__ == "__main__":
    unittest.main(verbosity=2)
