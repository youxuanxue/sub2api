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

import list_host_config as listing  # noqa: E402


class ListHostConfigTest(unittest.TestCase):
    def test_refuse_aws_bucket_settings(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
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
                listing.main([])
            self.assertIn("forbidden AWS", str(raised.exception))

    def test_rejects_pathful_prefix(self) -> None:
        with self.assertRaises(SystemExit) as raised:
            listing.main(["--prefix", "a/b"])
        self.assertIn("single path segment", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
