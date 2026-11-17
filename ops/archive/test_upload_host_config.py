#!/usr/bin/env python3
from __future__ import annotations

import json
import pathlib
import sys
import tempfile
import unittest
from unittest import mock

_DIR = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(_DIR))

import upload_host_config as upload  # noqa: E402


class UploadHostConfigTest(unittest.TestCase):
    def test_plan_default_does_not_call_aws(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            src = pathlib.Path(tmp) / "host.env"
            src.write_text("x=1\n", encoding="utf-8")
            with mock.patch("subprocess.check_call") as check_call, mock.patch(
                "sys.stdout", new_callable=lambda: __import__("io").StringIO()
            ):
                code = upload.main(["--file", str(src), "--label", "smoke"])
            self.assertEqual(code, 0)
            check_call.assert_not_called()

    def test_rejects_pathful_label(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            src = pathlib.Path(tmp) / "host.env"
            src.write_text("x=1\n", encoding="utf-8")
            with self.assertRaises(SystemExit) as raised:
                upload.main(["--file", str(src), "--label", "a/b"])
            self.assertIn("single path segment", str(raised.exception))

    def test_refuse_when_settings_point_at_aws_bucket(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            src = pathlib.Path(tmp) / "host.env"
            src.write_text("x=1\n", encoding="utf-8")
            settings = pathlib.Path(tmp) / "cos-archive.json"
            settings.write_text(
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
                        "forbidden_aws_buckets": [
                            "tokenkey-prod-archive-682751977094"
                        ],
                    }
                ),
                encoding="utf-8",
            )
            import cos_archive_identity as cos_archive

            with mock.patch.dict(
                "os.environ", {"TK_COS_ARCHIVE_SETTINGS": str(settings)}
            ), self.assertRaises(cos_archive.CosArchiveError) as raised:
                upload.main(["--file", str(src), "--label", "smoke"])
            self.assertIn("forbidden AWS", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
