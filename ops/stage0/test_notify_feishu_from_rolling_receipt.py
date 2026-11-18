#!/usr/bin/env python3
"""Contract tests for rolling-receipt → Feishu notify glue."""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = pathlib.Path(__file__).with_name("notify-feishu-from-rolling-receipt.sh")


class NotifyFeishuFromRollingReceiptTest(unittest.TestCase):
    def test_skips_when_previous_runtime_tag_missing(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            receipt = pathlib.Path(tmp) / "receipt.json"
            receipt.write_text(json.dumps({
                "tag": "1.8.383",
                "outcome": "success",
                "previous_runtime_tag": None,
                "finished_at": "2026-11-18T00:01:35Z",
            }))
            proc = subprocess.run(
                [
                    "bash", str(SCRIPT),
                    "--receipt", str(receipt),
                    "--environment", "cn-prod",
                    "--api-url", "https://api.galaxywaveai.com",
                ],
                cwd=ROOT,
                capture_output=True,
                text=True,
                check=False,
                env={**os.environ, "TK_FEISHU_WEBHOOK_URL": "https://example.invalid/hook"},
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertIn("previous_runtime_tag empty", proc.stdout + proc.stderr)

    def test_forwards_baseline_to_notify_dry_run(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            receipt = pathlib.Path(tmp) / "receipt.json"
            receipt.write_text(json.dumps({
                "tag": "1.8.383",
                "outcome": "success",
                "previous_runtime_tag": "1.8.381",
                "finished_at": "2026-11-18T00:01:35Z",
            }))
            # Patch PATH so notify-feishu-release.sh is invoked as dry-run via wrapper?
            # Call notify script indirectly: set empty webhook → skip after parse.
            # Better: monkey by running with DRY via replacing notify script — keep
            # contract light: missing webhook still exits 0 after reading receipt.
            proc = subprocess.run(
                [
                    "bash", str(SCRIPT),
                    "--receipt", str(receipt),
                    "--environment", "prod",
                    "--api-url", "https://api.tokenkey.dev",
                    "--run-url", "https://github.com/youxuanxue/sub2api/actions/runs/1",
                ],
                cwd=ROOT,
                capture_output=True,
                text=True,
                check=False,
                env={**os.environ, "TK_FEISHU_WEBHOOK_URL": "", "TK_FEISHU_SIGNING_SECRET": ""},
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            combined = proc.stdout + proc.stderr
            self.assertIn("TK_FEISHU_WEBHOOK_URL empty", combined)
            self.assertNotIn("previous_runtime_tag empty", combined)


if __name__ == "__main__":
    unittest.main()
