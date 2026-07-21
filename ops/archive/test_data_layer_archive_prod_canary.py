#!/usr/bin/env python3
"""Behavior tests for the production export-only archive canary."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock


_DIR = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(_DIR))

import data_layer_archive_prod_canary as canary  # noqa: E402
import data_layer_archive_rehearsal as rehearsal  # noqa: E402


_INSTANCE_ID = "i-0123456789abcdef0"
_STAGING_BASE = "s3://test-backups/prod/pgdump/archive-canary"
_HOLD_STARTED_AT = "2026-07-21T02:30:00.000000Z"


def _timestamp(value: dt.datetime) -> str:
    return value.astimezone(dt.timezone.utc).isoformat().replace("+00:00", "Z")


def _fake_query_runner(
    *,
    as_of: str,
    rows: list[dict[str, object]],
    read_only: str = "on",
):
    def run(sql: str, _timeout_seconds: int, _output_limit: int) -> list[str]:
        if "current_setting('transaction_read_only')" in sql:
            return [
                json.dumps(
                    {
                        "database": rehearsal.PROD_CANARY_DATABASE,
                        "read_only": read_only,
                        "server_clock": as_of,
                    }
                )
            ]
        return [json.dumps(row) for row in rows]

    return run


def _cold_row(as_of: str, *, payload_size: int = 0) -> dict[str, object]:
    created_at = rehearsal._timestamp(
        rehearsal._utc(as_of) - dt.timedelta(days=31)
    )
    return {
        "dataset": "ops",
        "record_id": "1",
        "created_at": created_at,
        "payload": {
            "id": 1,
            "created_at": created_at,
            "message": "x" * payload_size,
        },
    }


class ProdArchiveCanaryTest(unittest.TestCase):
    def test_us039_plan_is_offline_and_bounded(self) -> None:
        with mock.patch.object(
            canary,
            "_command_output",
            side_effect=AssertionError("plan must not execute a command"),
        ):
            plan = canary.build_plan(
                table="ops_system_logs",
                as_of="2026-07-21T03:00:00Z",
                timeout_seconds=20,
                max_rows=1_000,
                max_logical_bytes=16 * 1024 * 1024,
            )
        self.assertFalse(plan["execution_authorized"])
        self.assertFalse(plan["deletion_authorized"])
        self.assertEqual(plan["cutoff_exclusive"], "2026-06-21T03:00:00.000000Z")
        self.assertEqual(plan["stack"], canary.PROD_STACK)
        self.assertEqual(plan["staging_key_base"], canary.S3_KEY_BASE)
        self.assertEqual(plan["staging_retention_days"], 7)

        with self.assertRaisesRegex(canary.CanaryError, "max_rows"):
            canary.build_plan(
                table="ops_system_logs",
                as_of="2026-07-21T03:00:00Z",
                timeout_seconds=20,
                max_rows=canary.HARD_MAX_ROWS + 1,
                max_logical_bytes=1,
            )

    def test_us039_run_guards_fail_before_aws(self) -> None:
        result = subprocess.run(
            [
                sys.executable,
                str(_DIR / "data_layer_archive_prod_canary.py"),
                "run",
                "--table",
                "ops_system_logs",
                "--as-of",
                "2026-07-21T03:00:00Z",
                "--evidence-root",
                "/tmp/tokenkey-canary-evidence",
                "--restore-target-dsn",
                "postgresql://postgres@127.0.0.1/tokenkey_archive_restore_test",
                "--seed",
                "7",
                "--cleanup-hold-receipt",
                "/tmp/nonexistent-cleanup-hold.json",
                "--confirm",
                "wrong",
            ],
            env={"PATH": "/usr/bin:/bin"},
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(result.returncode, 2)
        self.assertIn("confirmation token is invalid", result.stderr)

        delete = subprocess.run(
            [sys.executable, str(_DIR / "data_layer_archive_prod_canary.py"), "delete"],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertNotEqual(delete.returncode, 0)
        self.assertIn("invalid choice", delete.stderr)

        restore = subprocess.run(
            [
                sys.executable,
                str(_DIR / "data_layer_archive_prod_canary.py"),
                "restore-committed",
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertNotEqual(restore.returncode, 0)
        self.assertIn("invalid choice", restore.stderr)

    def test_us039_stage0_output_is_bounded_while_command_runs(self) -> None:
        with self.assertRaisesRegex(canary.CanaryError, "output exceeds 1024 bytes"):
            canary._bounded_command_output(
                [
                    sys.executable,
                    "-c",
                    "import sys; sys.stdout.buffer.write(b'x' * 2048)",
                ],
                timeout_seconds=5,
                output_limit=1024,
            )

    def test_us039_run_refuses_without_active_cleanup_hold(self) -> None:
        args = argparse.Namespace(
            confirm=canary.PROD_CONFIRMATION,
            table="ops_system_logs",
            as_of="2026-07-21T03:00:00Z",
            timeout_seconds=20,
            max_rows=1_000,
            max_logical_bytes=16 * 1024 * 1024,
            restore_target_dsn=(
                "postgresql://postgres@127.0.0.1/tokenkey_archive_restore_test"
            ),
            ssm_timeout_seconds=300,
            evidence_root="/tmp/tokenkey-prod-canary-test-evidence",
            cleanup_hold_receipt="/tmp/missing-cleanup-hold.json",
            seed=7,
        )
        with mock.patch.object(
            canary, "_prod_instance", return_value=_INSTANCE_ID
        ), mock.patch.object(
            canary.cleanup_hold,
            "verify_receipt_for_instance",
            side_effect=canary.cleanup_hold.HoldControlError("cleanup hold is not active"),
        ), mock.patch.object(canary, "_stack_output") as stack_output:
            with self.assertRaisesRegex(
                canary.cleanup_hold.HoldControlError, "not active"
            ):
                canary.run_canary(args)
        stack_output.assert_not_called()

    def test_us039_remote_bundle_imports_and_host_export_rechecks_hold(self) -> None:
        first_archive = canary._remote_bundle_archive()
        self.assertEqual(first_archive, canary._remote_bundle_archive())
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            with canary.tarfile.open(
                fileobj=canary.io.BytesIO(first_archive), mode="r:gz"
            ) as archive:
                archive.extractall(root, filter="data")
            imported = subprocess.run(
                [sys.executable, "-c", "import data_layer_archive_prod_canary"],
                cwd=root,
                capture_output=True,
                text=True,
                check=False,
            )
        self.assertEqual(imported.returncode, 0, imported.stderr)

        command = canary._remote_host_command(
            table="ops_system_logs",
            as_of="2026-07-21T03:00:00Z",
            instance_id=_INSTANCE_ID,
            staging_s3_base_uri=_STAGING_BASE,
            bundle_s3_uri=f"{_STAGING_BASE}/control/{'a' * 64}.tar.gz",
            bundle_sha256="a" * 64,
            hold_started_at=_HOLD_STARTED_AT,
            timeout_seconds=20,
            max_rows=1_000,
            max_logical_bytes=16 * 1024 * 1024,
        )
        self.assertIn("data_layer_archive_cleanup_hold_remote.py", command)
        self.assertIn("--hold-started-at", command)
        parsed = subprocess.run(
            ["bash", "-n"], input=command, capture_output=True, text=True, check=False
        )
        self.assertEqual(parsed.returncode, 0, parsed.stderr)
        self.assertLessEqual(
            len(canary._ssm_parameters(command, 300).encode("utf-8")),
            canary.SSM_PARAMETERS_MAX_BYTES,
        )
        with self.assertRaisesRegex(canary.CanaryError, "payload bound"):
            canary._ssm_parameters("x" * canary.SSM_PARAMETERS_MAX_BYTES, 300)

        hold = {
            "server_clock": "2026-07-21T03:00:00.000000Z",
            "hold_active": True,
            "no_cleanup_after_hold": True,
            "runtime_disabled_proven": True,
        }
        sealed = {
            "batch_dir": "/tmp/prod-canary-batch",
            "canary": {"table": "ops_system_logs"},
            "metrics": {"candidate_rows": 1},
        }
        uploaded = {
            "mode": "prod_archive_export_canary_upload",
            "deletion_authorized": False,
        }
        with mock.patch.object(
            canary.cleanup_hold_remote, "verify_hold", return_value=hold
        ) as verify_hold, mock.patch.object(
            canary, "seal_prod_canary_batch", return_value=sealed
        ) as seal, mock.patch.object(
            canary, "upload_committed_batch", return_value=uploaded
        ):
            result = canary.host_export(
                table="ops_system_logs",
                as_of="2026-07-21T03:00:00Z",
                instance_id=_INSTANCE_ID,
                staging_s3_base_uri=_STAGING_BASE,
                hold_started_at=_HOLD_STARTED_AT,
                confirmation=canary.PROD_CONFIRMATION,
                timeout_seconds=20,
                max_rows=1_000,
                max_logical_bytes=16 * 1024 * 1024,
            )
        verify_hold.assert_called_once_with(_HOLD_STARTED_AT)
        seal.assert_called_once()
        self.assertTrue(result["cleanup_hold"]["runtime_disabled_proven"])

        with mock.patch.object(
            canary.cleanup_hold_remote,
            "verify_hold",
            side_effect=canary.cleanup_hold_remote.HoldError("cleanup hold is not active"),
        ), mock.patch.object(canary, "seal_prod_canary_batch") as seal:
            with self.assertRaisesRegex(canary.cleanup_hold_remote.HoldError, "not active"):
                canary.host_export(
                    table="ops_system_logs",
                    as_of="2026-07-21T03:00:00Z",
                    instance_id=_INSTANCE_ID,
                    staging_s3_base_uri=_STAGING_BASE,
                    hold_started_at=_HOLD_STARTED_AT,
                    confirmation=canary.PROD_CONFIRMATION,
                    timeout_seconds=20,
                    max_rows=1_000,
                    max_logical_bytes=16 * 1024 * 1024,
                )
        seal.assert_not_called()

    def test_us039_seal_rejects_hot_or_oversized_rows(self) -> None:
        as_of = "2026-07-21T03:00:00Z"
        hot_created_at = rehearsal._timestamp(
            rehearsal._utc(as_of) - dt.timedelta(days=1)
        )
        hot = {
            "dataset": "ops",
            "record_id": "2",
            "created_at": hot_created_at,
            "payload": {"id": 2, "created_at": hot_created_at},
        }
        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaisesRegex(canary.CanaryError, "hot row"):
                canary.seal_prod_canary_batch(
                    temp,
                    table="ops_system_logs",
                    as_of=as_of,
                    instance_id=_INSTANCE_ID,
                    staging_s3_base_uri=_STAGING_BASE,
                    query_runner=_fake_query_runner(as_of=as_of, rows=[hot]),
                    max_logical_bytes=1024,
                )
        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaisesRegex(canary.CanaryError, "logical bytes exceed"):
                canary.seal_prod_canary_batch(
                    temp,
                    table="ops_system_logs",
                    as_of=as_of,
                    instance_id=_INSTANCE_ID,
                    staging_s3_base_uri=_STAGING_BASE,
                    query_runner=_fake_query_runner(
                        as_of=as_of,
                        rows=[_cold_row(as_of, payload_size=2_000)],
                    ),
                    max_logical_bytes=256,
                )
        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaisesRegex(canary.CanaryError, "not read-only"):
                canary.seal_prod_canary_batch(
                    temp,
                    table="ops_system_logs",
                    as_of=as_of,
                    instance_id=_INSTANCE_ID,
                    staging_s3_base_uri=_STAGING_BASE,
                    query_runner=_fake_query_runner(
                        as_of=as_of,
                        rows=[_cold_row(as_of)],
                        read_only="off",
                    ),
                )

        too_many = []
        for record_id in ("1", "2"):
            row = _cold_row(as_of)
            row["record_id"] = record_id
            row["payload"] = {**row["payload"], "id": int(record_id)}
            too_many.append(row)
        with tempfile.TemporaryDirectory() as temp:
            sealed = canary.seal_prod_canary_batch(
                temp,
                table="ops_system_logs",
                as_of=as_of,
                instance_id=_INSTANCE_ID,
                staging_s3_base_uri=_STAGING_BASE,
                query_runner=_fake_query_runner(as_of=as_of, rows=too_many),
                max_rows=1,
            )
            manifest = json.loads(
                (pathlib.Path(sealed["batch_dir"]) / "manifest.json").read_text(
                    encoding="utf-8"
                )
            )
            self.assertEqual(manifest["source_rows"], 1)
            self.assertTrue(manifest["canary"]["more_cold_rows_after_sample"])
            self.assertEqual(
                manifest["canary"]["sample_first_key"],
                manifest["canary"]["sample_last_key"],
            )

        same_time_rows = []
        for record_id in ("2", "10"):
            row = _cold_row(as_of)
            row["record_id"] = record_id
            row["payload"] = {**row["payload"], "id": int(record_id)}
            same_time_rows.append(row)
        with tempfile.TemporaryDirectory() as temp:
            sealed = canary.seal_prod_canary_batch(
                temp,
                table="ops_system_logs",
                as_of=as_of,
                instance_id=_INSTANCE_ID,
                staging_s3_base_uri=_STAGING_BASE,
                query_runner=_fake_query_runner(as_of=as_of, rows=same_time_rows),
            )
            manifest = json.loads(
                (pathlib.Path(sealed["batch_dir"]) / "manifest.json").read_text(
                    encoding="utf-8"
                )
            )
        self.assertEqual(manifest["canary"]["sample_first_key"]["id"], 2)
        self.assertEqual(manifest["canary"]["sample_last_key"]["id"], 10)

        def invalid_proof(
            _sql: str, _timeout_seconds: int, _output_limit: int
        ) -> list[str]:
            return ["[]"]

        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaisesRegex(canary.CanaryError, "source proof is invalid"):
                canary.seal_prod_canary_batch(
                    temp,
                    table="ops_system_logs",
                    as_of=as_of,
                    instance_id=_INSTANCE_ID,
                    staging_s3_base_uri=_STAGING_BASE,
                    query_runner=invalid_proof,
                )

        def invalid_row(sql: str, _timeout_seconds: int, _output_limit: int) -> list[str]:
            if "transaction_read_only" in sql:
                return [
                    json.dumps(
                        {
                            "database": rehearsal.PROD_CANARY_DATABASE,
                            "read_only": "on",
                            "server_clock": as_of,
                        }
                    )
                ]
            return ["[]"]

        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaisesRegex(canary.CanaryError, "invalid source row"):
                canary.seal_prod_canary_batch(
                    temp,
                    table="ops_system_logs",
                    as_of=as_of,
                    instance_id=_INSTANCE_ID,
                    staging_s3_base_uri=_STAGING_BASE,
                    query_runner=invalid_row,
                )

        server_clock = "2026-07-21T03:00:00Z"
        ahead_as_of = "2026-07-21T03:10:00Z"
        between_cutoffs = "2026-06-21T03:05:00Z"
        between_row = {
            "dataset": "ops",
            "record_id": "3",
            "created_at": between_cutoffs,
            "payload": {"id": 3, "created_at": between_cutoffs},
        }

        def skewed_clock(sql: str, _timeout_seconds: int, _output_limit: int) -> list[str]:
            if "transaction_read_only" in sql:
                return [
                    json.dumps(
                        {
                            "database": rehearsal.PROD_CANARY_DATABASE,
                            "read_only": "on",
                            "server_clock": server_clock,
                        }
                    )
                ]
            return [json.dumps(between_row)]

        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaisesRegex(canary.CanaryError, "hot row"):
                canary.seal_prod_canary_batch(
                    temp,
                    table="ops_system_logs",
                    as_of=ahead_as_of,
                    instance_id=_INSTANCE_ID,
                    staging_s3_base_uri=_STAGING_BASE,
                    query_runner=skewed_clock,
                )

    def test_us039_s3_manifest_is_uploaded_last_and_encrypted(self) -> None:
        as_of = "2026-07-21T03:00:00Z"
        with tempfile.TemporaryDirectory() as temp:
            sealed = canary.seal_prod_canary_batch(
                temp,
                table="ops_system_logs",
                as_of=as_of,
                instance_id=_INSTANCE_ID,
                staging_s3_base_uri=_STAGING_BASE,
                query_runner=_fake_query_runner(
                    as_of=as_of,
                    rows=[_cold_row(as_of)],
                ),
            )
            uploaded: dict[str, tuple[int, str]] = {}
            calls: list[list[str]] = []

            def fake_command(args: list[str]) -> str:
                calls.append(args)
                if args[1:3] == ["s3", "cp"]:
                    source = pathlib.Path(args[-2])
                    uri = args[-1]
                    metadata = args[args.index("--metadata") + 1]
                    uploaded[uri] = (source.stat().st_size, metadata.split("=", 1)[1])
                    return ""
                if args[1:3] == ["s3api", "head-object"]:
                    bucket = args[args.index("--bucket") + 1]
                    key = args[args.index("--key") + 1]
                    size, checksum = uploaded[f"s3://{bucket}/{key}"]
                    return json.dumps(
                        {
                            "ContentLength": size,
                            "ServerSideEncryption": "AES256",
                            "Metadata": {"sha256": checksum},
                        }
                    )
                raise AssertionError(args)

            receipt = canary.upload_committed_batch(
                sealed["batch_dir"], command_runner=fake_command
            )
            cp_targets = [args[-1] for args in calls if args[1:3] == ["s3", "cp"]]
            self.assertTrue(receipt["manifest_uploaded_last"])
            self.assertTrue(cp_targets[-1].endswith("/manifest.json"))
            self.assertTrue(
                all(
                    item["server_side_encryption"] == "AES256"
                    for item in receipt["objects"]
                )
            )

            def unencrypted(args: list[str]) -> str:
                output = fake_command(args)
                if args[1:3] == ["s3api", "head-object"]:
                    value = json.loads(output)
                    value.pop("ServerSideEncryption")
                    return json.dumps(value)
                return output

            with self.assertRaisesRegex(canary.CanaryError, "not server-side encrypted"):
                canary.upload_committed_batch(
                    sealed["batch_dir"], command_runner=unencrypted
                )

            control_bundle = canary.stage_remote_bundle(
                _STAGING_BASE, command_runner=fake_command
            )
            self.assertIn(f"{_STAGING_BASE}/control/", control_bundle["uri"])
            self.assertEqual(control_bundle["server_side_encryption"], "AES256")

    def test_us039_manifest_rejects_wrong_source_database(self) -> None:
        as_of = "2026-07-21T03:00:00Z"
        with tempfile.TemporaryDirectory() as temp:
            sealed = canary.seal_prod_canary_batch(
                temp,
                table="ops_system_logs",
                as_of=as_of,
                instance_id=_INSTANCE_ID,
                staging_s3_base_uri=_STAGING_BASE,
                query_runner=_fake_query_runner(
                    as_of=as_of,
                    rows=[_cold_row(as_of)],
                ),
            )
            manifest_path = pathlib.Path(sealed["batch_dir"]) / "manifest.json"
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            manifest["source_database"] = "not-tokenkey"
            rehearsal._atomic_json(manifest_path, manifest)
            with self.assertRaisesRegex(rehearsal.RehearsalError, "source identity"):
                rehearsal.verify_batch(sealed["batch_dir"])
            manifest["source_database"] = rehearsal.PROD_CANARY_DATABASE
            manifest["canary"]["cutoff_exclusive"] = "2026-06-21T03:00:01.000000Z"
            rehearsal._atomic_json(manifest_path, manifest)
            with self.assertRaisesRegex(rehearsal.RehearsalError, "cold waterline"):
                rehearsal.verify_batch(sealed["batch_dir"])

    def test_us039_control_plane_resolves_prod_tags_and_validates_receipt(
        self,
    ) -> None:
        calls: list[list[str]] = []

        def fake_aws(args: list[str]):
            calls.append(args)
            if args[:2] == ["cloudformation", "describe-stacks"]:
                return {
                    "Stacks": [
                        {
                            "Outputs": [
                                {"OutputKey": "InstanceId", "OutputValue": _INSTANCE_ID}
                            ]
                        }
                    ]
                }
            if args[:2] == ["ec2", "describe-instances"]:
                return {
                    "Reservations": [
                        {
                            "Instances": [
                                {
                                    "InstanceId": _INSTANCE_ID,
                                    "State": {"Name": "running"},
                                    "Tags": [
                                        {"Key": "Project", "Value": "tokenkey"},
                                        {"Key": "Environment", "Value": "prod"},
                                    ],
                                }
                            ]
                        }
                    ]
                }
            raise AssertionError(args)

        with mock.patch.object(canary, "_aws_json", side_effect=fake_aws):
            self.assertEqual(canary._prod_instance(), _INSTANCE_ID)
        self.assertTrue(
            all(
                args[args.index("--region") + 1] == canary.PROD_REGION
                for args in calls
            )
        )

        with mock.patch.object(
            canary, "_stack_output", return_value=_INSTANCE_ID
        ), mock.patch.object(
            canary,
            "_aws_json",
            return_value={
                "Reservations": [
                    {
                        "Instances": [
                            {
                                "InstanceId": _INSTANCE_ID,
                                "State": {"Name": "running"},
                                "Tags": [
                                    {"Key": "Project", "Value": "tokenkey"},
                                    {"Key": "Environment", "Value": "staging"},
                                ],
                            }
                        ]
                    }
                ]
            },
        ):
            with self.assertRaisesRegex(canary.CanaryError, "tags do not match"):
                canary._prod_instance()

        batch_id = "prod-canary-20260721T030000000000Z-0123456789abcdef"
        receipt = {
            "mode": "prod_archive_export_canary_upload",
            "batch_id": batch_id,
            "s3_prefix": f"{_STAGING_BASE}/{batch_id}",
            "manifest_sha256": "a" * 64,
            "objects": [
                {
                    "uri": f"{_STAGING_BASE}/{batch_id}/manifest.json",
                    "sha256": "a" * 64,
                    "server_side_encryption": "AES256",
                }
            ],
            "manifest_uploaded_last": True,
            "source_mutated": False,
            "deletion_authorized": False,
            "cleanup_hold": {
                "hold_started_at": _HOLD_STARTED_AT,
                "verified_at": "2026-07-21T03:00:00.000000Z",
                "hold_active": True,
                "no_cleanup_after_hold": True,
                "runtime_disabled_proven": True,
            },
        }
        self.assertIs(
            canary._validated_remote_receipt(
                receipt,
                staging_base=_STAGING_BASE,
                expected_hold_started_at=_HOLD_STARTED_AT,
            ),
            receipt,
        )
        with self.assertRaisesRegex(canary.CanaryError, "commit validation"):
            canary._validated_remote_receipt(
                {**receipt, "s3_prefix": f"s3://wrong/{batch_id}"},
                staging_base=_STAGING_BASE,
                expected_hold_started_at=_HOLD_STARTED_AT,
            )

        ssm_calls: list[list[str]] = []

        def fake_ssm(args: list[str]):
            ssm_calls.append(args)
            if args[:2] == ["ssm", "send-command"]:
                return {"Command": {"CommandId": "command-123"}}
            if args[:2] == ["ssm", "get-command-invocation"]:
                return {
                    "Status": "Success",
                    "StandardOutputContent": json.dumps(receipt),
                }
            raise AssertionError(args)

        with mock.patch.object(canary, "_aws_json", side_effect=fake_ssm):
            actual_receipt = canary._run_ssm(
                _INSTANCE_ID,
                "printf done",
                timeout_seconds=30,
            )
        self.assertEqual(actual_receipt, receipt)
        send_parameters = json.loads(
            ssm_calls[0][ssm_calls[0].index("--parameters") + 1]
        )
        self.assertEqual(send_parameters["executionTimeout"], ["30"])
        self.assertTrue(
            all(
                args[args.index("--instance-id") + 1] == _INSTANCE_ID
                if "--instance-id" in args
                else args[args.index("--instance-ids") + 1] == _INSTANCE_ID
                for args in ssm_calls
            )
        )

        with mock.patch.object(
            canary,
            "_aws_json",
            return_value={"Command": {"CommandId": "command-timeout"}},
        ), mock.patch.object(
            canary.time, "monotonic", side_effect=[0.0, 31.0]
        ), mock.patch.object(canary, "_command_output", return_value="") as cancel:
            with self.assertRaisesRegex(canary.CanaryError, "was cancelled"):
                canary._run_ssm(
                    _INSTANCE_ID,
                    "printf done",
                    timeout_seconds=30,
                )
        cancel.assert_called_once_with(
            [
                "aws",
                "ssm",
                "cancel-command",
                "--region",
                canary.PROD_REGION,
                "--command-id",
                "command-timeout",
                "--instance-ids",
                _INSTANCE_ID,
            ]
        )

    def test_us039_run_preserves_complete_mode(self) -> None:
        batch_id = "prod-canary-20260721T030000000000Z-0123456789abcdef"
        receipt = {
            "mode": "prod_archive_export_canary_upload",
            "batch_id": batch_id,
            "s3_prefix": f"{_STAGING_BASE}/{batch_id}",
            "manifest_sha256": "a" * 64,
            "objects": [
                {
                    "uri": f"{_STAGING_BASE}/{batch_id}/manifest.json",
                    "sha256": "a" * 64,
                    "server_side_encryption": "AES256",
                }
            ],
            "manifest_uploaded_last": True,
            "source_mutated": False,
            "deletion_authorized": False,
            "cleanup_hold": {
                "hold_started_at": _HOLD_STARTED_AT,
                "verified_at": "2026-07-21T03:00:00.000000Z",
                "hold_active": True,
                "no_cleanup_after_hold": True,
                "runtime_disabled_proven": True,
            },
        }
        restored = {
            "mode": "prod_archive_export_canary_restore",
            "batch_id": batch_id,
            "verify": {"verified": True},
            "restore": {"verified": True},
            "source_mutated": False,
            "deletion_authorized": False,
        }
        args = argparse.Namespace(
            confirm=canary.PROD_CONFIRMATION,
            table="ops_system_logs",
            as_of="2026-07-21T03:00:00Z",
            timeout_seconds=20,
            max_rows=1_000,
            max_logical_bytes=16 * 1024 * 1024,
            restore_target_dsn=(
                "postgresql://postgres@127.0.0.1/tokenkey_archive_restore_test"
            ),
            ssm_timeout_seconds=300,
            evidence_root="/tmp/tokenkey-prod-canary-test-evidence",
            cleanup_hold_receipt="/tmp/tokenkey-prod-canary-hold.json",
            seed=7,
        )
        with mock.patch.object(
            canary, "_prod_instance", return_value=_INSTANCE_ID
        ), mock.patch.object(
            canary, "_stack_output", return_value="test-backups"
        ), mock.patch.object(
            canary, "_stack_parameter", return_value="7"
        ), mock.patch.object(
            canary,
            "stage_remote_bundle",
            return_value={
                "uri": f"{_STAGING_BASE}/control/{'a' * 64}.tar.gz",
                "sha256": "a" * 64,
                "deletion_authorized": False,
            },
        ), mock.patch.object(
            canary, "_remote_host_command", return_value="true"
        ), mock.patch.object(
            canary.cleanup_hold,
            "verify_receipt_for_instance",
            return_value={"hold_started_at": _HOLD_STARTED_AT},
        ), mock.patch.object(
            canary.cleanup_hold,
            "verify",
            return_value={
                "instance_id": _INSTANCE_ID,
                "server_clock": "2026-07-21T03:00:00Z",
                "no_cleanup_after_hold": True,
            },
        ), mock.patch.object(
            canary, "_run_ssm", return_value=receipt
        ), mock.patch.object(
            canary, "restore_committed_batch", return_value=restored
        ) as restore:
            result = canary.run_canary(args)

        self.assertEqual(result["mode"], "prod_archive_export_canary_complete")
        self.assertTrue(result["production_export_executed"])
        self.assertTrue(result["cleanup_hold"]["no_cleanup_after_hold"])
        restore.assert_called_once_with(
            s3_prefix=receipt["s3_prefix"],
            batch_id=batch_id,
            evidence_root=args.evidence_root,
            target_dsn=args.restore_target_dsn,
            seed=args.seed,
            timeout_seconds=args.timeout_seconds,
            expected_manifest_sha256=receipt["manifest_sha256"],
        )

    def test_us039_committed_download_binds_manifest_checksum(self) -> None:
        as_of = "2026-07-21T03:00:00Z"
        temporary_directory = tempfile.TemporaryDirectory
        with temporary_directory() as source_root, temporary_directory() as evidence_root:
            sealed = canary.seal_prod_canary_batch(
                source_root,
                table="ops_system_logs",
                as_of=as_of,
                instance_id=_INSTANCE_ID,
                staging_s3_base_uri=_STAGING_BASE,
                query_runner=_fake_query_runner(
                    as_of=as_of,
                    rows=[_cold_row(as_of)],
                ),
            )
            batch_dir = pathlib.Path(sealed["batch_dir"])
            batch_id = sealed["batch_id"]
            s3_prefix = sealed["staging_s3_prefix"]
            manifest_bytes = (batch_dir / "manifest.json").read_bytes()
            manifest_sha256 = rehearsal._sha256(manifest_bytes)
            objects = {
                f"{s3_prefix}/{path.name}": path.read_bytes()
                for path in batch_dir.iterdir()
            }

            def download(args: list[str]) -> str:
                pathlib.Path(args[-1]).write_bytes(objects[args[-2]])
                return ""

            downloaded = canary._download_committed_batch(
                s3_prefix,
                batch_id,
                evidence_root,
                command_runner=download,
                expected_manifest_sha256=manifest_sha256,
            )
            self.assertTrue(rehearsal.verify_batch(downloaded)["verified"])

        with tempfile.TemporaryDirectory() as evidence_root:
            with self.assertRaisesRegex(canary.CanaryError, "checksum mismatch"):
                canary._download_committed_batch(
                    s3_prefix,
                    batch_id,
                    evidence_root,
                    command_runner=download,
                    expected_manifest_sha256="b" * 64,
                )
            self.assertFalse((pathlib.Path(evidence_root) / batch_id).exists())


def _have_postgres_integration() -> bool:
    return bool(shutil.which("docker") and shutil.which("psql"))


@unittest.skipUnless(
    _have_postgres_integration(),
    "needs docker and psql for the real production canary integration test",
)
class ProdArchiveCanaryPostgresIntegrationTest(unittest.TestCase):
    _container: str | None = None
    _source_dsn = ""
    _target_dsn = ""

    @classmethod
    def _psql(cls, dsn: str, sql: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [
                "psql",
                dsn,
                "-X",
                "-q",
                "-t",
                "-A",
                "-v",
                "ON_ERROR_STOP=1",
                "-c",
                sql,
            ],
            capture_output=True,
            text=True,
            check=False,
        )

    @classmethod
    def setUpClass(cls) -> None:
        cls._container = f"tk-archive-prod-canary-{os.getpid()}"
        image = os.environ.get("TOKENKEY_ARCHIVE_POSTGRES_IMAGE", "postgres:18-alpine")
        try:
            subprocess.run(
                [
                    "docker",
                    "run",
                    "-d",
                    "--rm",
                    "--name",
                    cls._container,
                    "-p",
                    "127.0.0.1::5432",
                    "-e",
                    "POSTGRES_PASSWORD=test",
                    "-e",
                    f"POSTGRES_DB={rehearsal.PROD_CANARY_DATABASE}",
                    image,
                ],
                check=True,
                capture_output=True,
                text=True,
            )
            port = ""
            deadline = time.time() + 60
            while time.time() < deadline:
                published = subprocess.run(
                    ["docker", "port", cls._container, "5432/tcp"],
                    capture_output=True,
                    text=True,
                    check=False,
                ).stdout.strip()
                if ":" in published:
                    port = published.rsplit(":", 1)[-1]
                ready = subprocess.run(
                    [
                        "docker",
                        "exec",
                        cls._container,
                        "pg_isready",
                        "-U",
                        "postgres",
                        "-d",
                        rehearsal.PROD_CANARY_DATABASE,
                    ],
                    capture_output=True,
                    check=False,
                ).returncode == 0
                if port and ready:
                    break
                time.sleep(1)
            if not port:
                raise RuntimeError("postgres port was not published")
            base = f"postgresql://postgres:test@127.0.0.1:{port}"
            cls._source_dsn = f"{base}/{rehearsal.PROD_CANARY_DATABASE}"
            target_db = f"{rehearsal.POSTGRES_RESTORE_PREFIX}prod_canary"
            created = cls._psql(f"{base}/postgres", f"CREATE DATABASE {target_db}")
            if created.returncode != 0:
                raise RuntimeError(created.stderr)
            cls._target_dsn = f"{base}/{target_db}"
            now = dt.datetime.now(dt.timezone.utc)
            old = _timestamp(now - dt.timedelta(days=31))
            hot = _timestamp(now - dt.timedelta(days=1))
            seeded = cls._psql(
                cls._source_dsn,
                "CREATE TABLE ops_system_logs ("
                "id bigint primary key, created_at timestamptz, level text, message text);"
                f"INSERT INTO ops_system_logs VALUES (1, '{old}', 'WARN', 'cold'), "
                f"(2, '{hot}', 'INFO', 'hot');",
            )
            if seeded.returncode != 0:
                raise RuntimeError(seeded.stderr)
        except (OSError, RuntimeError, subprocess.CalledProcessError) as exc:
            cls.tearDownClass()
            raise unittest.SkipTest(f"postgres backend unavailable: {exc}")

    @classmethod
    def tearDownClass(cls) -> None:
        if cls._container:
            subprocess.run(["docker", "stop", cls._container], capture_output=True)
            cls._container = None

    def test_us039_prod_canary_restores_without_mutating_source(self) -> None:
        before = self._psql(
            self._source_dsn,
            "SELECT json_agg(row_to_json(t) ORDER BY id)::text FROM ops_system_logs t",
        )
        self.assertEqual(before.returncode, 0, before.stderr)

        def query(sql: str, timeout_seconds: int, _output_limit: int) -> list[str]:
            return rehearsal._run_psql(
                self._source_dsn,
                sql,
                timeout_seconds=timeout_seconds,
                read_only=True,
            )

        as_of = rehearsal._timestamp(dt.datetime.now(dt.timezone.utc))
        with tempfile.TemporaryDirectory() as temp:
            sealed = canary.seal_prod_canary_batch(
                temp,
                table="ops_system_logs",
                as_of=as_of,
                instance_id=_INSTANCE_ID,
                staging_s3_base_uri=_STAGING_BASE,
                query_runner=query,
                timeout_seconds=10,
                max_rows=100,
                max_logical_bytes=1024 * 1024,
            )
            verified = rehearsal.verify_batch(sealed["batch_dir"])
            restored = rehearsal.restore_postgres_random(
                sealed["batch_dir"],
                self._target_dsn,
                seed=7,
                timeout_seconds=10,
            )
        self.assertEqual(verified["row_count"], 1)
        self.assertTrue(restored["verified"])
        self.assertEqual(restored["restored_rows"], 1)

        after = self._psql(
            self._source_dsn,
            "SELECT json_agg(row_to_json(t) ORDER BY id)::text FROM ops_system_logs t",
        )
        self.assertEqual(after.returncode, 0, after.stderr)
        self.assertEqual(after.stdout, before.stdout)


if __name__ == "__main__":
    unittest.main()
