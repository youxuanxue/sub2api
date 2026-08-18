#!/usr/bin/env python3
"""Behavior tests for the explicit usage_logs daily partition operator."""

from __future__ import annotations

import json
import contextlib
import io
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


_TARGET = "edge:us3"
_INSTANCE = "mi-0123456789abcdef0"
_UPPER = "2026-08-05T00:00:00Z"
_CONFIRM = remote.cutover_confirmation_prefix(_TARGET) + _UPPER


def _prepare_receipt(*, target: str = _TARGET, instance_id: str = _INSTANCE) -> dict:
    return {
        "mode": "usage_logs_daily_partition_prepare",
        "target": target,
        "instance_id": instance_id,
        "legacy_upper_exclusive": _UPPER,
        "row_count_before": 6_000_000,
        "bound_validated": True,
        "required_cutover_confirmation": _CONFIRM,
        "source_rows_copied": False,
        "deletion_authorized": False,
    }


class UsageLogsDailyPartitionTest(unittest.TestCase):
    def test_cutover_sql_has_short_lock_and_no_data_copy(self) -> None:
        sql = remote.build_cutover_sql(_UPPER, 6_000_000)
        self.assertIn("lock_timeout = '5s'", sql)
        self.assertIn("LOCK TABLE usage_logs IN ACCESS EXCLUSIVE MODE", sql)
        self.assertIn("ATTACH PARTITION usage_logs_legacy", sql)
        self.assertIn("PARTITION BY RANGE (created_at)", sql)
        self.assertIn("obj_description(oid, 'pg_constraint')", sql)
        self.assertIn("usage_logs_partition_index_map", sql)
        self.assertIn("contype = 'c'", sql)
        self.assertIn("ALTER TABLE usage_logs_legacy DROP CONSTRAINT", sql)
        self.assertIn("ALTER TABLE usage_logs_legacy ADD CONSTRAINT", sql)
        self.assertNotIn("INSERT INTO usage_logs SELECT", sql)
        self.assertNotIn("DROP TABLE usage_logs_legacy", sql)
        self.assertIn("legacy row count drifted below prepare receipt", sql)

    def test_prepare_reuses_existing_operator_bound_and_indexes_first(self) -> None:
        before = {
            "partitioned": False,
            "server_clock": "2026-08-03T00:00:00Z",
            "bound_exists": True,
            "bound_operator_owned": True,
            "legacy_upper_exclusive": _UPPER,
            "row_count": 6_000_000,
        }
        after = {
            **before,
            "bound_validated": True,
        }
        inventory = {
            "unique_indexes": [],
            "incoming_foreign_keys": [],
            "billing_dedup_ready": True,
        }
        with mock.patch.object(remote, "status", side_effect=[before, after]), mock.patch.object(
            remote, "_query_json", return_value=inventory
        ), mock.patch.object(remote, "_psql", return_value="") as psql:
            receipt = remote.prepare(
                _TARGET, remote.prepare_confirmation(_TARGET)
            )

        self.assertEqual(receipt["legacy_upper_exclusive"], _UPPER)
        self.assertEqual(receipt["target"], _TARGET)
        self.assertEqual(psql.call_count, 2)
        self.assertIn("CREATE INDEX CONCURRENTLY", psql.call_args_list[0].args[0])
        self.assertIn(_UPPER, psql.call_args_list[1].args[0])

    def test_prepare_refuses_missing_billing_dedup_unique_key(self) -> None:
        before = {
            "partitioned": False,
            "server_clock": "2026-08-03T00:00:00Z",
            "bound_exists": True,
            "bound_operator_owned": True,
            "legacy_upper_exclusive": _UPPER,
            "row_count": 6_000_000,
        }
        inventory = {
            "unique_indexes": [],
            "incoming_foreign_keys": [],
            "billing_dedup_ready": False,
        }
        with mock.patch.object(remote, "status", return_value=before), mock.patch.object(
            remote, "_query_json", return_value=inventory
        ), mock.patch.object(remote, "_psql") as psql:
            with self.assertRaisesRegex(remote.UsagePartitionError, "billing dedup"):
                remote.prepare(_TARGET, remote.prepare_confirmation(_TARGET))

        psql.assert_not_called()

    def test_abort_refuses_confirmation_not_bound_to_upper(self) -> None:
        with mock.patch.object(remote, "_psql") as psql:
            with self.assertRaisesRegex(remote.UsagePartitionError, "confirmation"):
                remote.abort(
                    _TARGET,
                    _UPPER,
                    remote.abort_confirmation_prefix(_TARGET) + "2026-08-06T00:00:00Z",
                )
        psql.assert_not_called()

    def test_edge_confirmation_cannot_authorize_prod_prepare(self) -> None:
        with mock.patch.object(remote, "status") as status:
            with self.assertRaisesRegex(remote.UsagePartitionError, "confirmation"):
                remote.prepare("prod", remote.prepare_confirmation(_TARGET))
        status.assert_not_called()

    def test_edge_remote_receipt_binds_managed_instance_and_target(self) -> None:
        payload = {
            "mode": "usage_logs_daily_partition_status",
            "target": _TARGET,
            "deletion_authorized": False,
        }
        completed = subprocess.CompletedProcess(
            ["bash"],
            0,
            stdout=json.dumps(payload) + "\n",
            stderr=(
                "[run-probe] resolved region=us-east-2 "
                f"instance_id={_INSTANCE}\n"
            ),
        )
        with mock.patch.object(control.subprocess, "run", return_value=completed) as run:
            result = control._run_remote(
                _TARGET, "status", [], timeout_seconds=120
            )

        self.assertEqual(result["instance_id"], _INSTANCE)
        command = run.call_args.args[0]
        self.assertEqual(command[command.index("--target") + 1], _TARGET)
        self.assertIn(f"REMOTE_TARGET={_TARGET}", command)

    def test_prepare_receipt_cannot_cross_targets(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            receipt = pathlib.Path(temp) / "prepare.json"
            receipt.write_text(json.dumps(_prepare_receipt()), encoding="utf-8")
            with self.assertRaisesRegex(control.UsagePartitionControlError, "validation"):
                control._load_prepare_receipt(receipt, "edge:us4")

    def test_cutover_refuses_confirmation_not_bound_to_upper(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            prepare_path = root / "prepare.json"
            prepare_path.write_text(json.dumps(_prepare_receipt()), encoding="utf-8")
            with mock.patch.object(control, "_run_remote") as run_remote:
                with self.assertRaisesRegex(control.UsagePartitionControlError, "exactly match"):
                    control.cutover(
                        _TARGET,
                        prepare_path,
                        root / "cutover.json",
                        remote.cutover_confirmation_prefix(_TARGET)
                        + "2026-08-06T00:00:00Z",
                    )
            run_remote.assert_not_called()

    def test_cutover_persists_verified_row_counts(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            prepare_path = root / "prepare.json"
            cutover_path = root / "cutover.json"
            prepare_path.write_text(json.dumps(_prepare_receipt()), encoding="utf-8")
            remote_result = {
                "mode": "usage_logs_daily_partition_cutover",
                "target": _TARGET,
                "instance_id": _INSTANCE,
                "legacy_upper_exclusive": _UPPER,
                "source_rows_copied": False,
                "deletion_authorized": False,
                "verification": {
                    "partitioned": True,
                    "legacy_attached": True,
                    "daily_partitions_attached": True,
                    "no_parent_global_unique": True,
                    "no_incoming_legacy_fk": True,
                    "constraints_preserved": True,
                    "legacy_row_count": 6_000_100,
                    "parent_row_count": 6_000_100,
                },
            }
            with mock.patch.object(control, "_run_remote", return_value=remote_result):
                result = control.cutover(
                    _TARGET, prepare_path, cutover_path, _CONFIRM
                )
            self.assertEqual(result["row_count_before"], 6_000_000)
            self.assertEqual(
                json.loads(cutover_path.read_text(encoding="utf-8"))["verification"]["parent_row_count"],
                6_000_100,
            )

    def test_cutover_refuses_legacy_count_below_prepare_receipt(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            prepare_path = root / "prepare.json"
            prepare_path.write_text(json.dumps(_prepare_receipt()), encoding="utf-8")
            remote_result = {
                "mode": "usage_logs_daily_partition_cutover",
                "target": _TARGET,
                "instance_id": _INSTANCE,
                "legacy_upper_exclusive": _UPPER,
                "source_rows_copied": False,
                "deletion_authorized": False,
                "verification": {
                    "partitioned": True,
                    "legacy_attached": True,
                    "daily_partitions_attached": True,
                    "no_parent_global_unique": True,
                    "no_incoming_legacy_fk": True,
                    "constraints_preserved": True,
                    "legacy_row_count": 5_999_999,
                    "parent_row_count": 5_999_999,
                },
            }
            with mock.patch.object(control, "_run_remote", return_value=remote_result):
                with self.assertRaisesRegex(
                    control.UsagePartitionControlError, "complete verification"
                ):
                    control.cutover(
                        _TARGET,
                        prepare_path,
                        root / "cutover.json",
                        _CONFIRM,
                    )

    def test_verify_refuses_prepare_receipt_replay_on_a_different_instance(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            prepare_path = pathlib.Path(temp) / "prepare.json"
            prepare_path.write_text(json.dumps(_prepare_receipt()), encoding="utf-8")
            remote_result = {
                "mode": "usage_logs_daily_partition_verify",
                "target": _TARGET,
                "instance_id": "mi-11111111111111111",
                "deletion_authorized": False,
                "partitioned": True,
                "legacy_attached": True,
                "daily_partitions_attached": True,
                "no_parent_global_unique": True,
                "no_incoming_legacy_fk": True,
                "constraints_preserved": True,
                "legacy_row_count": 6_000_100,
                "parent_row_count": 6_000_100,
            }
            stderr = io.StringIO()
            with mock.patch.object(control, "_run_remote", return_value=remote_result):
                with contextlib.redirect_stderr(stderr):
                    result = control.main(
                        [
                            "verify",
                            "--target",
                            _TARGET,
                            "--prepare-receipt",
                            str(prepare_path),
                        ]
                    )
        self.assertEqual(result, 2)
        self.assertIn("different production instance", stderr.getvalue())

    def test_verify_refuses_name_only_partition_proof(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            prepare_path = pathlib.Path(temp) / "prepare.json"
            prepare_path.write_text(json.dumps(_prepare_receipt()), encoding="utf-8")
            remote_result = {
                "mode": "usage_logs_daily_partition_verify",
                "target": _TARGET,
                "instance_id": _INSTANCE,
                "deletion_authorized": False,
                "partitioned": True,
                "legacy_attached": True,
                "first_daily_partition_exists": True,
                "daily_partitions_attached": False,
                "no_parent_global_unique": True,
                "no_incoming_legacy_fk": True,
                "constraints_preserved": True,
                "legacy_row_count": 6_000_100,
                "parent_row_count": 6_000_100,
            }
            stderr = io.StringIO()
            with mock.patch.object(control, "_run_remote", return_value=remote_result):
                with contextlib.redirect_stderr(stderr):
                    result = control.main(
                        [
                            "verify",
                            "--target",
                            _TARGET,
                            "--prepare-receipt",
                            str(prepare_path),
                        ]
                    )
        self.assertEqual(result, 2)
        self.assertIn("complete verification", stderr.getvalue())

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
CREATE TABLE usage_billing_dedup (
  id bigserial PRIMARY KEY,
  request_id varchar(255) NOT NULL,
  api_key_id bigint NOT NULL,
  request_fingerprint varchar(64) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_usage_billing_dedup_request_api_key
  ON usage_billing_dedup (request_id, api_key_id);
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
            first_prepare = remote.prepare(
                "prod", remote.prepare_confirmation("prod")
            )
            aborted = remote.abort(
                "prod",
                first_prepare["legacy_upper_exclusive"],
                remote.abort_confirmation_prefix("prod")
                + first_prepare["legacy_upper_exclusive"],
            )
            prepared = remote.prepare(
                "prod", remote.prepare_confirmation("prod")
            )
            self._psql("DROP INDEX idx_usage_billing_dedup_request_api_key")
            with self.assertRaisesRegex(remote.UsagePartitionError, "billing dedup"):
                remote.cutover(
                    "prod",
                    prepared["legacy_upper_exclusive"],
                    prepared["row_count_before"],
                    remote.cutover_confirmation_prefix("prod")
                    + prepared["legacy_upper_exclusive"],
                )
            self._psql(
                "CREATE UNIQUE INDEX idx_usage_billing_dedup_request_api_key "
                "ON usage_billing_dedup (request_id, api_key_id)"
            )
            result = remote.cutover(
                "prod",
                prepared["legacy_upper_exclusive"],
                prepared["row_count_before"],
                remote.cutover_confirmation_prefix("prod")
                + prepared["legacy_upper_exclusive"],
            )

        self.assertTrue(aborted["bound_removed"])
        verification = result["verification"]
        self.assertEqual(verification["legacy_row_count"], 1)
        self.assertEqual(verification["parent_row_count"], 1)
        self.assertTrue(verification["constraints_preserved"])
        self.assertTrue(verification["no_incoming_legacy_fk"])
        self.assertTrue(verification["daily_partitions_attached"])
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
        routed_table = self._psql(
            "SELECT tableoid::regclass::text FROM usage_logs "
            "WHERE request_id = 'req-2'"
        )
        self.assertEqual(routed_table, "usage_logs_" + upper[:10].replace("-", ""))


if __name__ == "__main__":
    unittest.main(verbosity=2)
