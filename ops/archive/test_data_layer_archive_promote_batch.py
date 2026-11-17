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
_COS_BUCKET = "tokenkey-prod-ops-archive-1320330536"
_STAGING_PREFIX = f"s3://{_COS_BUCKET}/prod/ops-archive-export/{_BATCH_ID}"
_ARCHIVE_PREFIX = f"s3://{_COS_BUCKET}/prod/ops-archive/{_BATCH_ID}"


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


def _sample_promote_receipt() -> dict[str, object]:
    manifest_sha256 = "b" * 64
    return {
        "schema_version": promote.PROMOTE_RECEIPT_SCHEMA,
        "mode": promote.PROMOTE_RECEIPT_MODE,
        "environment": "prod",
        "batch_id": _BATCH_ID,
        "staging_s3_prefix": _STAGING_PREFIX,
        "archive_s3_prefix": _ARCHIVE_PREFIX,
        "manifest_sha256": manifest_sha256,
        "objects": [
            {
                "uri": f"{_ARCHIVE_PREFIX}/ops.jsonl.gz",
                "bytes": 128,
                "sha256": "a" * 64,
                "server_side_encryption": "AES256",
            },
            {
                "uri": f"{_ARCHIVE_PREFIX}/manifest.json",
                "bytes": 128,
                "sha256": manifest_sha256,
                "server_side_encryption": "AES256",
            },
        ],
        "manifest_promoted_last": True,
        "archive_standard_days": promote.ARCHIVE_STANDARD_DAYS,
        "archive_expire_days": promote.ARCHIVE_EXPIRE_DAYS,
        "source_mutated": False,
        "deletion_authorized": False,
    }


class PromoteBatchTest(unittest.TestCase):
    def test_plan_is_offline_and_names_cos_archive_contract(self) -> None:
        plan = promote.build_plan(batch_id=_BATCH_ID)
        self.assertFalse(plan["execution_authorized"])
        self.assertEqual(plan["archive_backend"], "tencent-cos")
        self.assertEqual(plan["archive_standard_days"], 400)
        self.assertEqual(plan["archive_expire_days"], 400)
        self.assertEqual(plan["archive_s3_prefix"], _ARCHIVE_PREFIX)
        self.assertEqual(plan["staging_s3_prefix"], _STAGING_PREFIX)

    def test_plan_refuses_if_settings_point_at_aws(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "cos-archive.json"
            path.write_text(
                json.dumps(
                    {
                        "bucket": "tokenkey-prod-archive-682751977094",
                        "region": "us-east-1",
                        "endpoint": "cos.na-siliconvalley.myqcloud.com",
                        "prefixes": {
                            "ops_archive": "prod/ops-archive",
                            "ops_archive_export": "prod/ops-archive-export",
                            "host_config": "prod/host-config",
                        },
                        "expire_days": {
                            "ops_archive": 400,
                            "ops_archive_export": 7,
                            "host_config": 90,
                        },
                        "forbidden_aws_buckets": ["tokenkey-prod-archive-682751977094"],
                    }
                )
            )
            with mock.patch.dict("os.environ", {"TK_COS_ARCHIVE_SETTINGS": str(path)}):
                with self.assertRaisesRegex(promote.PromoteError, "forbidden AWS"):
                    promote.build_plan(batch_id=_BATCH_ID)

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
            if "head-object" in args:
                key = args[args.index("--key") + 1]
                sha = manifest_sha256 if key.endswith("manifest.json") else "a" * 64
                return json.dumps(
                    {
                        "ContentLength": 128,
                        "ServerSideEncryption": "AES256",
                        "Metadata": {"sha256": sha},
                    }
                )
            if "cp" in args:
                return ""
            raise AssertionError(f"unexpected command: {args}")

        with mock.patch.object(
            promote, "_download_manifest", return_value=(manifest, manifest_bytes, manifest_sha256)
        ), mock.patch.object(
            promote,
            "build_plan",
            return_value={
                "staging_s3_prefix": _STAGING_PREFIX,
                "archive_s3_prefix": _ARCHIVE_PREFIX,
            },
        ):
            receipt = promote.promote_batch(
                batch_id=_BATCH_ID,
                confirmation=promote.PROMOTE_CONFIRMATION,
                command_runner=command_runner,
            )
        self.assertTrue(receipt["manifest_promoted_last"])
        self.assertEqual(receipt["manifest_sha256"], manifest_sha256)
        cp_targets = [args[-1] for args in calls if "cp" in args]
        self.assertEqual(cp_targets[-1], f"{_ARCHIVE_PREFIX}/manifest.json")
        self.assertTrue(any("--endpoint-url" in args for args in calls if "cp" in args))

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
        existing_receipt = _sample_promote_receipt()
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

    def test_promote_ledger_rejects_malformed_receipt(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            promote_path = pathlib.Path(temp) / "promote.json"
            promote.init_promote_ledger(promote_path)
            loaded = json.loads(promote_path.read_text(encoding="utf-8"))
            receipt = _sample_promote_receipt()
            receipt["objects"][-1]["sha256"] = "c" * 64
            loaded["promoted_batches"].append(receipt)
            promote._atomic_json(promote_path, loaded)

            with self.assertRaisesRegex(promote.PromoteError, "manifest binding"):
                promote.load_promote_ledger(promote_path)

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
