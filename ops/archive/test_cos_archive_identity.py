#!/usr/bin/env python3
from __future__ import annotations

import json
import pathlib
import tempfile
import unittest
from unittest import mock

import cos_archive_identity as cos


ROOT = pathlib.Path(__file__).resolve().parent


class CosArchiveIdentityTest(unittest.TestCase):
    def test_settings_load_approved_bucket(self) -> None:
        settings = cos.load_settings(ROOT / "cos-archive.json")
        self.assertEqual(settings["bucket"], "tokenkey-prod-ops-archive-1320330536")
        self.assertIn("cos.na-siliconvalley.myqcloud.com", settings["endpoint"])
        self.assertEqual(settings["expire_days"]["ops_archive_export"], 7)

    def test_refuse_aws_archive_bucket_and_endpoint(self) -> None:
        settings = cos.load_settings(ROOT / "cos-archive.json")
        with self.assertRaisesRegex(cos.CosArchiveError, "forbidden AWS"):
            cos.refuse_aws_destination(
                "s3://tokenkey-prod-archive-682751977094/prod/ops-archive/x",
                settings=settings,
            )
        with self.assertRaisesRegex(cos.CosArchiveError, "must not be AWS"):
            cos.refuse_aws_destination(
                "s3://anything.amazonaws.com/prod/ops-archive/x",
                settings=settings,
            )

    def test_refuse_wrong_bucket_even_if_not_listed(self) -> None:
        settings = cos.load_settings(ROOT / "cos-archive.json")
        with self.assertRaisesRegex(cos.CosArchiveError, "must be tokenkey-prod-ops-archive"):
            cos.refuse_aws_destination(
                "s3://tokenkey-prod-backup-usw-1320330536/prod/ops-archive/x",
                settings=settings,
            )

    def test_approved_uri_passes(self) -> None:
        settings = cos.load_settings(ROOT / "cos-archive.json")
        cos.refuse_aws_destination(cos.archive_base_uri(settings), settings=settings)
        cos.refuse_aws_destination(cos.export_base_uri(settings) + "/batch", settings=settings)

    def test_inject_endpoint_virtual_hosted(self) -> None:
        settings = cos.load_settings(ROOT / "cos-archive.json")
        argv = cos.inject_endpoint(
            ["aws", "s3api", "head-object", "--bucket", settings["bucket"]],
            settings=settings,
        )
        self.assertEqual(argv[2], "--endpoint-url")
        self.assertEqual(argv[3], f"https://{settings['endpoint']}")
        self.assertNotIn(settings["bucket"], argv[3])

    def test_aws_cli_env_forces_virtual_addressing(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            creds = pathlib.Path(tmp) / "cos-backup.env"
            creds.write_text("tencent_SecretId=AKIDtest\ntencent_SecretKey=secret\n")
            cfg = pathlib.Path(tmp) / "aws-config"
            with mock.patch.dict(
                "os.environ",
                {
                    "TK_COS_ARCHIVE_SETTINGS": str(ROOT / "cos-archive.json"),
                    "TK_COS_ARCHIVE_AWS_CONFIG": str(cfg),
                },
                clear=False,
            ):
                env = cos.aws_cli_env(creds)
            self.assertEqual(env["AWS_CONFIG_FILE"], str(cfg))
            self.assertIn("addressing_style = virtual", cfg.read_text())
            self.assertEqual(env["AWS_ACCESS_KEY_ID"], "AKIDtest")

    def test_settings_reject_aws_endpoint(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "bad.json"
            path.write_text(
                json.dumps(
                    {
                        "bucket": "tokenkey-prod-ops-archive-1320330536",
                        "region": "us-east-1",
                        "endpoint": "s3.us-east-1.amazonaws.com",
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
                    }
                )
            )
            with self.assertRaisesRegex(cos.CosArchiveError, "must not be AWS"):
                cos.load_settings(path)


if __name__ == "__main__":
    unittest.main()
