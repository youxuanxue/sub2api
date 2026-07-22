#!/usr/bin/env python3
"""Behavior tests for production legacy cold batch export."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


_DIR = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(_DIR))

import data_layer_archive_prod_canary as canary  # noqa: E402
import data_layer_archive_prod_export as export  # noqa: E402
import data_layer_archive_rehearsal as rehearsal  # noqa: E402


_INSTANCE_ID = "i-0123456789abcdef0"
_STAGING_BASE = "s3://test-backups/prod/pgdump/archive-export"
_HOLD_STARTED_AT = "2026-07-21T02:30:00.000000Z"
_AS_OF = "2026-07-21T03:00:00.000000Z"


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


def _legacy_cold_row(
    as_of: str,
    *,
    record_id: str = "1",
    days_before: int = 45,
    payload_size: int = 0,
) -> dict[str, object]:
    created_at = rehearsal._timestamp(
        rehearsal._utc(as_of) - dt.timedelta(days=days_before)
    )
    return {
        "dataset": "ops",
        "record_id": record_id,
        "created_at": created_at,
        "payload": {
            "id": int(record_id),
            "created_at": created_at,
            "message": "x" * payload_size,
        },
    }


class ProdArchiveExportTest(unittest.TestCase):
    def test_plan_is_offline_and_legacy_scoped(self) -> None:
        with mock.patch.object(
            canary,
            "_command_output",
            side_effect=AssertionError("plan must not execute a command"),
        ):
            plan = export.build_plan(
                table="ops_system_logs",
                legacy_upper_exclusive=rehearsal.PROD_LEGACY_UPPER_EXCLUSIVE,
                timeout_seconds=60,
                max_rows=50_000,
                max_logical_bytes=256 * 1024 * 1024,
            )
        self.assertFalse(plan["execution_authorized"])
        self.assertFalse(plan["deletion_authorized"])
        self.assertEqual(plan["export_scope"], rehearsal.PROD_EXPORT_SCOPE_LEGACY_COLD)
        self.assertEqual(plan["staging_key_base"], export.S3_KEY_BASE)
        self.assertEqual(plan["required_confirmation"], export.PROD_EXPORT_CONFIRMATION)

        with self.assertRaisesRegex(export.ExportError, "legacy_upper_exclusive"):
            export.build_plan(
                table="ops_system_logs",
                legacy_upper_exclusive="2026-08-01T00:00:00.000000Z",
                timeout_seconds=60,
                max_rows=50_000,
                max_logical_bytes=256 * 1024 * 1024,
            )

    def test_init_and_load_ledger(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            ledger_path = pathlib.Path(temp) / "ledger.json"
            created = export.init_ledger(
                ledger_path,
                table="ops_system_logs",
                legacy_upper_exclusive=rehearsal.PROD_LEGACY_UPPER_EXCLUSIVE,
            )
            self.assertTrue(created["more_cold_rows_remaining"])
            self.assertIsNone(created["cursor_after"])
            loaded = export.load_ledger(ledger_path)
            self.assertEqual(loaded["table"], "ops_system_logs")
            with self.assertRaisesRegex(export.ExportError, "already exists"):
                export.init_ledger(
                    ledger_path,
                    table="ops_system_logs",
                    legacy_upper_exclusive=rehearsal.PROD_LEGACY_UPPER_EXCLUSIVE,
                )

    def test_seal_export_batch_records_cursor_and_legacy_bounds(self) -> None:
        rows = [_legacy_cold_row(_AS_OF, record_id="1")]
        with tempfile.TemporaryDirectory() as temp:
            sealed = export.seal_prod_export_batch(
                temp,
                table="ops_system_logs",
                instance_id=_INSTANCE_ID,
                staging_s3_base_uri=_STAGING_BASE,
                cursor_before=None,
                legacy_upper_exclusive=rehearsal.PROD_LEGACY_UPPER_EXCLUSIVE,
                query_runner=_fake_query_runner(as_of=_AS_OF, rows=rows),
                max_rows=50_000,
            )
            manifest = json.loads(
                (pathlib.Path(sealed["batch_dir"]) / "manifest.json").read_text(
                    encoding="utf-8"
                )
            )
            self.assertEqual(manifest["mode"], rehearsal.PROD_EXPORT_MODE)
            self.assertTrue(manifest["batch_id"].startswith("prod-export-"))
            self.assertEqual(
                manifest["export"]["legacy_upper_exclusive"],
                rehearsal.PROD_LEGACY_UPPER_EXCLUSIVE,
            )
            self.assertIsNone(manifest["export"]["cursor_before"])
            self.assertEqual(
                manifest["export"]["cursor_after"],
                manifest["export"]["last_key"],
            )
            verification = rehearsal.verify_batch(pathlib.Path(sealed["batch_dir"]))
            self.assertEqual(verification["mode"], "prod_archive_export_batch_verify")

    def test_seal_export_batch_honors_cursor_and_more_rows_flag(self) -> None:
        rows = [
            _legacy_cold_row(_AS_OF, record_id="1"),
            _legacy_cold_row(_AS_OF, record_id="2"),
        ]
        with tempfile.TemporaryDirectory() as temp:
            sealed = export.seal_prod_export_batch(
                temp,
                table="ops_system_logs",
                instance_id=_INSTANCE_ID,
                staging_s3_base_uri=_STAGING_BASE,
                cursor_before=None,
                legacy_upper_exclusive=rehearsal.PROD_LEGACY_UPPER_EXCLUSIVE,
                query_runner=_fake_query_runner(as_of=_AS_OF, rows=rows),
                max_rows=1,
            )
            manifest = json.loads(
                (pathlib.Path(sealed["batch_dir"]) / "manifest.json").read_text(
                    encoding="utf-8"
                )
            )
        self.assertTrue(manifest["export"]["more_cold_rows_remaining"])
        cursor_after = manifest["export"]["cursor_after"]
        with tempfile.TemporaryDirectory() as temp:
            sealed = export.seal_prod_export_batch(
                temp,
                table="ops_system_logs",
                instance_id=_INSTANCE_ID,
                staging_s3_base_uri=_STAGING_BASE,
                cursor_before=cursor_after,
                legacy_upper_exclusive=rehearsal.PROD_LEGACY_UPPER_EXCLUSIVE,
                query_runner=_fake_query_runner(as_of=_AS_OF, rows=rows[1:]),
                max_rows=1,
            )
            manifest = json.loads(
                (pathlib.Path(sealed["batch_dir"]) / "manifest.json").read_text(
                    encoding="utf-8"
                )
            )
        self.assertEqual(manifest["export"]["cursor_before"], cursor_after)
        self.assertFalse(manifest["export"]["more_cold_rows_remaining"])

    def test_run_batch_guard_fails_before_aws(self) -> None:
        result = subprocess.run(
            [
                sys.executable,
                str(_DIR / "data_layer_archive_prod_export.py"),
                "run-batch",
                "--ledger",
                "/tmp/nonexistent-ledger.json",
                "--evidence-root",
                "/tmp/tokenkey-export-evidence",
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

    def test_run_batch_advances_ledger(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            temp_path = pathlib.Path(temp)
            ledger_path = temp_path / "ledger.json"
            export.init_ledger(
                ledger_path,
                table="ops_system_logs",
                legacy_upper_exclusive=rehearsal.PROD_LEGACY_UPPER_EXCLUSIVE,
            )
            batch_dir = temp_path / "batch"
            batch_dir.mkdir()
            rows = [_legacy_cold_row(_AS_OF, record_id="9")]
            sealed = export.seal_prod_export_batch(
                temp_path / "sealed",
                table="ops_system_logs",
                instance_id=_INSTANCE_ID,
                staging_s3_base_uri=_STAGING_BASE,
                cursor_before=None,
                legacy_upper_exclusive=rehearsal.PROD_LEGACY_UPPER_EXCLUSIVE,
                query_runner=_fake_query_runner(as_of=_AS_OF, rows=rows),
            )
            manifest_path = pathlib.Path(sealed["batch_dir"]) / "manifest.json"
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            uploaded = {
                "mode": "prod_archive_export_canary_upload",
                "batch_id": manifest["batch_id"],
                "s3_prefix": manifest["staging_s3_prefix"],
                "manifest_sha256": rehearsal._sha256(manifest_path.read_bytes()),
                "objects": [{"uri": f"{manifest['staging_s3_prefix']}/manifest.json"}],
                "manifest_uploaded_last": True,
                "source_mutated": False,
                "deletion_authorized": False,
                "export": manifest["export"],
                "cleanup_hold": {"hold_started_at": _HOLD_STARTED_AT},
            }
            args = argparse.Namespace(
                confirm=export.PROD_EXPORT_CONFIRMATION,
                ledger=str(ledger_path),
                evidence_root=str(temp_path / "evidence"),
                cleanup_hold_receipt=str(temp_path / "hold.json"),
                ssm_timeout_seconds=300,
                timeout_seconds=120,
                max_rows=50_000,
                max_logical_bytes=256 * 1024 * 1024,
                verify_restore=False,
                restore_target_dsn="",
                seed=0,
            )
            hold_receipt = {"hold_started_at": _HOLD_STARTED_AT}
            hold_verify = {"server_clock": _AS_OF}
            with mock.patch.object(
                canary, "_prod_instance", return_value=_INSTANCE_ID
            ), mock.patch.object(
                export.cleanup_hold,
                "verify_receipt_for_instance",
                return_value=hold_receipt,
            ), mock.patch.object(
                export.cleanup_hold, "verify", return_value=hold_verify
            ), mock.patch.object(
                canary, "_stack_output", return_value="test-backups"
            ), mock.patch.object(
                canary, "_stack_parameter", return_value="7"
            ), mock.patch.object(
                export, "stage_remote_bundle", return_value={"uri": f"{_STAGING_BASE}/control/x.tar.gz", "sha256": "a" * 64}
            ), mock.patch.object(
                canary, "_run_ssm", return_value=uploaded
            ), mock.patch.object(
                canary,
                "_download_committed_batch",
                return_value=pathlib.Path(sealed["batch_dir"]),
            ):
                result = export.run_export_batch(args)
            self.assertEqual(result["mode"], "prod_archive_export_batch_complete")
            ledger = export.load_ledger(ledger_path)
            self.assertEqual(len(ledger["completed_batches"]), 1)
            self.assertFalse(ledger["more_cold_rows_remaining"])

    def test_remote_bundle_includes_export_script(self) -> None:
        sources = export._remote_bundle_sources()
        self.assertIn("data_layer_archive_prod_export.py", sources)
        self.assertIn("data_layer_archive_prod_canary.py", sources)
        command = export._remote_host_command(
            table="ops_system_logs",
            instance_id=_INSTANCE_ID,
            staging_s3_base_uri=_STAGING_BASE,
            bundle_s3_uri=f"{_STAGING_BASE}/control/{'a' * 64}.tar.gz",
            bundle_sha256="a" * 64,
            hold_started_at=_HOLD_STARTED_AT,
            cursor_before_json="null",
            legacy_upper_exclusive=rehearsal.PROD_LEGACY_UPPER_EXCLUSIVE,
            timeout_seconds=120,
            max_rows=50_000,
            max_logical_bytes=256 * 1024 * 1024,
        )
        self.assertIn("host-export", command)
        self.assertIn("--legacy-upper-exclusive", command)
        parsed = subprocess.run(
            ["bash", "-n"], input=command, capture_output=True, text=True, check=False
        )
        self.assertEqual(parsed.returncode, 0, parsed.stderr)


    def test_download_accepts_prod_export_batch_prefix(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            batch_id = "prod-export-20260722T000000.000000Z-deadbeef0000"
            prefix = f"s3://test-backups/prod/pgdump/archive-export/{batch_id}"
            with self.assertRaises(canary.CanaryError) as ctx:
                canary._download_committed_batch(
                    prefix,
                    batch_id,
                    temp,
                    expected_manifest_sha256="a" * 64,
                )
            self.assertNotIn("do not match", str(ctx.exception))


if __name__ == "__main__":
    unittest.main()
