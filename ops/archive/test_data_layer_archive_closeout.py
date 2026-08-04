#!/usr/bin/env python3
"""Behavior tests for the production archive closeout gate."""

from __future__ import annotations

import json
import pathlib
import sys
import tempfile
import unittest
from unittest import mock


_DIR = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(_DIR))

import data_layer_archive_closeout as closeout  # noqa: E402
import data_layer_archive_prod_export as export  # noqa: E402
import data_layer_archive_promote_batch as promote  # noqa: E402


_BATCH_ID = "prod-export-20260803T010203.000000Z-0123456789ab"
_UPPER = "2026-07-01T00:00:00.000000Z"
_INSTANCE = "i-0123456789abcdef0"


def _manifest(*, table: str = "ops_system_logs", instance_id: str = _INSTANCE) -> dict:
    return {
        "batch_id": _BATCH_ID,
        "source_identity_sha256": "f" * 64,
        "source_file_identity": {
            "instance_id": instance_id,
            "table": table,
        },
        "export": {
            "table": table,
            "legacy_upper_exclusive": _UPPER,
        },
        "artifacts": [
            {
                "dataset": "other",
                "row_count": 9,
                "logical_sha256": "d" * 64,
            },
            {
                "dataset": "ops",
                "row_count": 7,
                "logical_sha256": "c" * 64,
            },
        ],
    }


def _write_ledgers(root: pathlib.Path, *, cutoff: str = _UPPER, checksum: str = "a" * 64):
    export_path = root / "export.json"
    promote_path = root / "promote.json"
    export._atomic_json(
        export_path,
        {
            "schema_version": export.LEDGER_SCHEMA_VERSION,
            "mode": export.LEDGER_MODE,
            "environment": "prod",
            "table": "ops_system_logs",
            "export_scope": export.rehearsal.PROD_EXPORT_SCOPE_LEGACY_COLD,
            "legacy_upper_exclusive": _UPPER,
            "cursor_after": {"created_at": "2026-06-30T23:00:00.000000Z", "id": 1},
            "completed_batches": [
                {
                    "batch_id": _BATCH_ID,
                    "manifest_sha256": checksum,
                    "cutoff_exclusive": cutoff,
                }
            ],
            "more_cold_rows_remaining": False,
            "source_mutated": False,
            "deletion_authorized": False,
        },
    )
    export._atomic_json(
        promote_path,
        {
            "schema_version": promote.PROMOTE_LEDGER_SCHEMA,
            "mode": promote.PROMOTE_LEDGER_MODE,
            "environment": "prod",
            "promoted_batches": [
                {
                    "schema_version": promote.PROMOTE_RECEIPT_SCHEMA,
                    "mode": promote.PROMOTE_RECEIPT_MODE,
                    "environment": "prod",
                    "batch_id": _BATCH_ID,
                    "archive_s3_prefix": f"s3://archive/prod/ops-archive/{_BATCH_ID}",
                    "manifest_sha256": checksum,
                    "manifest_promoted_last": True,
                    "archive_standard_days": promote.ARCHIVE_STANDARD_DAYS,
                    "archive_expire_days": promote.ARCHIVE_EXPIRE_DAYS,
                    "source_mutated": False,
                    "deletion_authorized": False,
                }
            ],
            "source_mutated": False,
            "deletion_authorized": False,
        },
    )
    return export_path, promote_path


class ArchiveCloseoutTest(unittest.TestCase):
    def test_ledger_pair_requires_cutoff_at_partition_upper(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            export_path, promote_path = _write_ledgers(
                root, cutoff="2026-06-30T23:59:59.000000Z"
            )
            with self.assertRaisesRegex(closeout.CloseoutError, "partition upper"):
                closeout.validate_ledger_pair(export_path, promote_path)

    def test_ledger_pair_requires_exact_promote_checksum(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            export_path, promote_path = _write_ledgers(root)
            payload = json.loads(promote_path.read_text(encoding="utf-8"))
            payload["promoted_batches"][0]["manifest_sha256"] = "b" * 64
            export._atomic_json(promote_path, payload)
            with self.assertRaisesRegex(closeout.CloseoutError, "does not match"):
                closeout.validate_ledger_pair(export_path, promote_path)

    def test_ledger_pair_validates_every_archive_prefix(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            export_path, promote_path = _write_ledgers(root)
            payload = json.loads(promote_path.read_text(encoding="utf-8"))
            payload["promoted_batches"][0]["archive_s3_prefix"] = (
                f"s3://archive/prod/wrong-prefix/{_BATCH_ID}"
            )
            export._atomic_json(promote_path, payload)
            with self.assertRaisesRegex(closeout.CloseoutError, "archive prefix"):
                closeout.validate_ledger_pair(export_path, promote_path)

    def test_closeout_writes_restore_bound_receipt(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            hold_path = root / "hold.json"
            hold_path.write_text("{}\n", encoding="utf-8")
            batch_dir = root / _BATCH_ID
            batch_dir.mkdir()
            manifest_bytes = closeout.rehearsal._canonical_json(_manifest()).encode(
                "utf-8"
            )
            (batch_dir / "manifest.json").write_bytes(manifest_bytes)
            manifest_sha256 = closeout.rehearsal._sha256(manifest_bytes)
            export_path, promote_path = _write_ledgers(
                root, checksum=manifest_sha256
            )
            restore = {
                "verified": True,
                "batch_id": _BATCH_ID,
                "selected_dataset": "ops",
                "expected_rows": 7,
                "restored_rows": 7,
                "logical_sha256": "c" * 64,
                "deletion_authorized": False,
            }
            verification = {
                "verified": True,
                "batch_id": _BATCH_ID,
                "manifest_sha256": manifest_sha256,
            }
            with mock.patch.object(
                closeout.canary, "_prod_instance", return_value=_INSTANCE
            ), mock.patch.object(
                closeout.cleanup_hold,
                "verify_receipt_for_instance",
                return_value={"hold_started_at": "2026-07-21T00:00:00Z"},
            ), mock.patch.object(
                closeout.cleanup_hold,
                "verify",
                return_value={
                    "instance_id": _INSTANCE,
                    "server_clock": "2026-08-03T00:00:00Z",
                },
            ), mock.patch.object(
                closeout, "_download_archive_batch", return_value=batch_dir
            ), mock.patch.object(
                closeout.rehearsal, "verify_batch", return_value=verification
            ), mock.patch.object(
                closeout.rehearsal, "restore_postgres_random", return_value=restore
            ):
                result = closeout.closeout(
                    export_ledger_path=export_path,
                    promote_ledger_path=promote_path,
                    cleanup_hold_receipt_path=hold_path,
                    closeout_receipt_path=root / "closeout.json",
                    evidence_root=root / "evidence",
                    restore_target_dsn="postgresql://archive_restore@localhost/archive_restore",
                    seed=7,
                    confirmation=closeout.CLOSEOUT_CONFIRMATION,
                )

            self.assertTrue(result["cleanup_release_authorized"])
            self.assertFalse(result["deletion_authorized"])
            self.assertEqual(result["selected_batch_id"], _BATCH_ID)
            self.assertEqual(
                result["selected_manifest_binding"]["instance_id"], _INSTANCE
            )
            self.assertEqual(
                closeout.load_closeout_receipt(root / "closeout.json")["table"],
                "ops_system_logs",
            )

    def test_manifest_binding_rejects_wrong_table_and_instance(self) -> None:
        verification = {
            "verified": True,
            "batch_id": _BATCH_ID,
            "manifest_sha256": "a" * 64,
        }
        for field, manifest in (
            ("table", _manifest(table="ops_error_logs")),
            ("instance", _manifest(instance_id="i-aaaaaaaaaaaaaaaaa")),
        ):
            with self.subTest(field=field), self.assertRaisesRegex(
                closeout.CloseoutError, "does not match"
            ):
                closeout._validate_manifest_binding(
                    manifest,
                    verification,
                    batch_id=_BATCH_ID,
                    manifest_sha256="a" * 64,
                    table="ops_system_logs",
                    instance_id=_INSTANCE,
                    legacy_upper_exclusive=_UPPER,
                )

    def test_receipt_rejects_unbound_restore_and_invalid_evidence(self) -> None:
        valid = {
            "schema_version": closeout.CLOSEOUT_SCHEMA_VERSION,
            "mode": closeout.CLOSEOUT_MODE,
            "environment": "prod",
            "instance_id": _INSTANCE,
            "table": "ops_system_logs",
            "legacy_upper_exclusive": _UPPER,
            "final_cutoff_exclusive": _UPPER,
            "batch_count": 1,
            "export_ledger_sha256": "a" * 64,
            "promote_ledger_sha256": "b" * 64,
            "cleanup_hold_receipt_sha256": "c" * 64,
            "hold_started_at": "2026-07-21T00:00:00.000000Z",
            "hold_verified_at": "2026-08-03T00:00:00.000000Z",
            "restore_verified_at": "2026-08-03T01:00:00.000000Z",
            "selected_batch_id": _BATCH_ID,
            "selected_archive_s3_prefix": (
                f"s3://archive/prod/ops-archive/{_BATCH_ID}"
            ),
            "selected_manifest_sha256": "d" * 64,
            "selected_manifest_binding": {
                "source_identity_sha256": "f" * 64,
                "instance_id": _INSTANCE,
                "table": "ops_system_logs",
                "legacy_upper_exclusive": _UPPER,
            },
            "restore": {
                "verified": True,
                "batch_id": _BATCH_ID,
                "selected_dataset": "ops",
                "expected_rows": 7,
                "restored_rows": 7,
                "logical_sha256": "e" * 64,
                "deletion_authorized": False,
            },
            "cleanup_release_authorized": True,
            "deletion_authorized": False,
        }
        for field, value in (
            ("instance_id", "i-wrong"),
            ("restore_verified_at", "not-a-timestamp"),
            ("selected_manifest_sha256", "not-a-checksum"),
        ):
            with self.subTest(field=field), tempfile.TemporaryDirectory() as temp:
                receipt = pathlib.Path(temp) / "closeout.json"
                payload = json.loads(json.dumps(valid))
                payload[field] = value
                export._atomic_json(receipt, payload)
                with self.assertRaises(closeout.CloseoutError):
                    closeout.load_closeout_receipt(receipt)

        with tempfile.TemporaryDirectory() as temp:
            receipt = pathlib.Path(temp) / "closeout.json"
            payload = json.loads(json.dumps(valid))
            payload["restore"]["batch_id"] = "different-batch"
            export._atomic_json(receipt, payload)
            with self.assertRaises(closeout.CloseoutError):
                closeout.load_closeout_receipt(receipt)

        for field, value in (
            ("instance_id", "i-aaaaaaaaaaaaaaaaa"),
            ("table", "ops_error_logs"),
            ("legacy_upper_exclusive", "2026-06-01T00:00:00.000000Z"),
        ):
            with self.subTest(binding_field=field), tempfile.TemporaryDirectory() as temp:
                receipt = pathlib.Path(temp) / "closeout.json"
                payload = json.loads(json.dumps(valid))
                payload["selected_manifest_binding"][field] = value
                export._atomic_json(receipt, payload)
                with self.assertRaises(closeout.CloseoutError):
                    closeout.load_closeout_receipt(receipt)


if __name__ == "__main__":
    unittest.main(verbosity=2)
