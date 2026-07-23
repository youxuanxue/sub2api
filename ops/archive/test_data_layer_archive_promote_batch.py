#!/usr/bin/env python3
"""Behavior tests for archive batch promote."""

from __future__ import annotations

import json
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


_DIR = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(_DIR))

import data_layer_archive_promote_batch as promote  # noqa: E402
import data_layer_archive_prod_canary as canary  # noqa: E402
import data_layer_archive_prod_export as export  # noqa: E402
import data_layer_archive_rehearsal as rehearsal  # noqa: E402


_BATCH_ID = "prod-export-20260722T112823.174855Z-8a928e2ea2c9"
_STAGING_PREFIX = (
    "s3://test-backups/prod/pgdump/archive-export/"
    f"{_BATCH_ID}"
)
_ARCHIVE_PREFIX = f"s3://test-archive/prod/ops-archive/{_BATCH_ID}"


def _sample_manifest() -> dict[str, object]:
    return {
        "batch_id": _BATCH_ID,
        "mode": rehearsal.PROD_EXPORT_MODE,
        "staging_s3_prefix": _STAGING_PREFIX,
        "source_mutated": False,
        "deletion_authorized": False,
        "artifacts": [
            {
                "path": "ops.jsonl.gz",
                "artifact_sha256": "a" * 64,
            }
        ],
    }


class PromoteBatchTest(unittest.TestCase):
    def test_plan_is_offline_and_names_archive_contract(self) -> None:
        with mock.patch.object(canary, "_stack_output") as stack_output:
            stack_output.side_effect = lambda stack, key: {
                (canary.BACKUP_STACK, "BucketName"): "test-backups",
                (promote.ARCHIVE_STACK, "BucketName"): "test-archive",
                (promote.ARCHIVE_STACK, "ArchiveS3Uri"): "s3://test-archive/prod/ops-archive",
            }[(stack, key)]
            plan = promote.build_plan(batch_id=_BATCH_ID)
        self.assertFalse(plan["execution_authorized"])
        self.assertEqual(plan["archive_standard_days"], 90)
        self.assertEqual(plan["archive_expire_days"], 400)
        self.assertEqual(plan["archive_s3_prefix"], _ARCHIVE_PREFIX)

    def test_promote_refuses_invalid_confirmation(self) -> None:
        with self.assertRaisesRegex(promote.PromoteError, "confirmation token"):
            promote.promote_batch(batch_id=_BATCH_ID, confirmation="wrong")

    def test_promote_copies_artifacts_then_manifest(self) -> None:
        manifest = _sample_manifest()
        manifest_bytes = (json.dumps(manifest) + "\n").encode("utf-8")
        manifest_sha256 = rehearsal._sha256(manifest_bytes)
        calls: list[list[str]] = []

        def command_runner(args: list[str]) -> str:
            calls.append(args)
            if len(args) >= 3 and args[0:3] == ["aws", "s3api", "head-object"]:
                return json.dumps(
                    {
                        "ContentLength": 128,
                        "ServerSideEncryption": "AES256",
                        "Metadata": {"sha256": "a" * 64},
                    }
                )
            if args[:3] == ["aws", "s3", "cp"]:
                return ""
            raise AssertionError(f"unexpected command: {args}")

        with mock.patch.object(canary, "_stack_output") as stack_output, mock.patch.object(
            canary, "_head_s3_object", side_effect=lambda uri, **kwargs: {
                "uri": uri,
                "bytes": 128,
                "sha256": kwargs["expected_sha256"],
                "server_side_encryption": "AES256",
            }
        ), mock.patch.object(
            promote, "_download_manifest", return_value=(manifest, manifest_bytes, manifest_sha256)
        ), mock.patch.object(
            promote, "build_plan",
            return_value={
                "staging_s3_prefix": _STAGING_PREFIX,
                "archive_s3_prefix": _ARCHIVE_PREFIX,
            },
        ):
            stack_output.side_effect = lambda stack, key: {
                (canary.BACKUP_STACK, "BucketName"): "test-backups",
                (promote.ARCHIVE_STACK, "BucketName"): "test-archive",
                (promote.ARCHIVE_STACK, "ArchiveS3Uri"): "s3://test-archive/prod/ops-archive",
            }[(stack, key)]
            receipt = promote.promote_batch(
                batch_id=_BATCH_ID,
                confirmation=promote.PROMOTE_CONFIRMATION,
                command_runner=command_runner,
            )
        self.assertTrue(receipt["manifest_promoted_last"])
        self.assertEqual(receipt["manifest_sha256"], manifest_sha256)
        cp_targets = [args[-1] for args in calls if args[:3] == ["aws", "s3", "cp"]]
        self.assertEqual(cp_targets[-1], f"{_ARCHIVE_PREFIX}/manifest.json")

    def test_promote_ledger_skips_already_promoted_batches(self) -> None:
        export_ledger = {
            "schema_version": export.LEDGER_SCHEMA_VERSION,
            "mode": export.LEDGER_MODE,
            "environment": "prod",
            "table": "ops_system_logs",
            "export_scope": rehearsal.PROD_EXPORT_SCOPE_LEGACY_COLD,
            "legacy_upper_exclusive": rehearsal.PROD_LEGACY_UPPER_EXCLUSIVE,
            "cursor_after": None,
            "completed_batches": [
                {
                    "batch_id": _BATCH_ID,
                    "manifest_sha256": "b" * 64,
                }
            ],
            "more_cold_rows_remaining": False,
            "source_mutated": False,
            "deletion_authorized": False,
        }
        existing_receipt = {
            "batch_id": _BATCH_ID,
            "manifest_sha256": "b" * 64,
        }
        with tempfile.TemporaryDirectory() as temp:
            export_path = pathlib.Path(temp) / "export.json"
            promote_path = pathlib.Path(temp) / "promote.json"
            export_path.write_text(json.dumps(export_ledger) + "\n", encoding="utf-8")
            promote.init_promote_ledger(promote_path)
            loaded = promote.load_promote_ledger(promote_path)
            loaded["promoted_batches"].append(existing_receipt)
            promote._atomic_json(promote_path, loaded)
            with mock.patch.object(
                promote, "promote_batch", side_effect=AssertionError("should not promote")
            ):
                result = promote.promote_export_ledger(
                    export_ledger_path=export_path,
                    promote_ledger_path=promote_path,
                    confirmation=promote.PROMOTE_CONFIRMATION,
                )
            self.assertEqual(result["newly_promoted"], 0)
            self.assertTrue(result["drop_ready"])

    def test_cli_refuses_without_confirmation(self) -> None:
        result = subprocess.run(
            [
                sys.executable,
                str(_DIR / "data_layer_archive_promote_batch.py"),
                "promote",
                "--batch-id",
                _BATCH_ID,
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


if __name__ == "__main__":
    unittest.main()
