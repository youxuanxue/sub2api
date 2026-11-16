from __future__ import annotations

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from ops.observability.check_feishu_config import findings, main, signature_label


class FeishuConfigCheckTest(unittest.TestCase):
    def test_healthy_probe_has_no_findings(self) -> None:
        probe = {
            "db": {
                "row_present": True,
                "feishu_enabled": True,
                "feishu_webhook_present": True,
                "feishu_secret_present": True,
            },
            "host_env": {
                "feishu_webhook_present": True,
                "feishu_secret_present": True,
            },
        }
        self.assertEqual(findings(probe, 0), [])

    def test_missing_values_are_reported_without_secret_content(self) -> None:
        probe = {
            "db": {"row_present": True, "feishu_enabled": False},
            "host_env": {"feishu_webhook_present": False, "feishu_secret_present": False},
        }
        result = findings(probe, 0)
        self.assertEqual(result, [
            "db_feishu_disabled",
            "db_webhook_missing",
            "db_secret_missing",
            "host_webhook_missing",
            "host_secret_missing",
        ])
        self.assertNotIn("fixture-private-secret", json.dumps(probe))

    def test_transport_failure_has_stable_signature(self) -> None:
        self.assertEqual(findings({}, 3), ["probe_transport_failed:3"])
        self.assertEqual(
            signature_label(["db_secret_missing", "db_webhook_missing"]),
            signature_label(["db_webhook_missing", "db_secret_missing"]),
        )

    def test_abnormal_probe_sends_feishu_and_creates_issue(self) -> None:
        with tempfile.NamedTemporaryFile("w", encoding="utf-8") as handle:
            json.dump({"db": {"row_present": False}, "host_env": {}}, handle)
            handle.flush()
            calls: list[list[str]] = []

            def fake_sh(args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
                calls.append(args)
                if args[:3] == ["gh", "issue", "list"]:
                    return subprocess.CompletedProcess(args, 0, stdout="[]")
                if args[:2] == ["gh", "label"]:
                    return subprocess.CompletedProcess(args, 0, stdout="")
                if args[:3] == ["gh", "issue", "create"]:
                    return subprocess.CompletedProcess(
                        args, 0, stdout="https://github.com/example/repo/issues/1\n"
                    )
                raise AssertionError(args)

            with patch.dict(
                os.environ,
                {
                    "TK_FEISHU_WEBHOOK_URL": "https://example.test/hook",
                    "TK_FEISHU_SIGNING_SECRET": "fixture-private-secret",
                    "GITHUB_REPOSITORY": "example/repo",
                    "GITHUB_RUN_ID": "123",
                },
                clear=False,
            ), patch("ops.observability.check_feishu_config.post_feishu") as send, \
                    patch("ops.observability.check_feishu_config.sh", side_effect=fake_sh):
                result = main(["--probe-file", handle.name])

        self.assertEqual(result, 0)
        send.assert_called_once()
        self.assertNotIn("fixture-private-secret", send.call_args.args[0])
        self.assertTrue(any(args[:3] == ["gh", "issue", "create"] for args in calls))


class FeishuConfigWorkflowContractTest(unittest.TestCase):
    ROOT = Path(__file__).resolve().parents[2]

    def test_daily_workflow_is_read_only_and_reports_issues(self) -> None:
        text = (self.ROOT / ".github/workflows/prod-feishu-config-sync.yml").read_text()
        self.assertIn("Prod Feishu Config Watchdog", text)
        self.assertIn("issues: write", text)
        self.assertIn("check_feishu_config.py", text)
        self.assertNotIn("environment: prod", text)
        self.assertNotIn("sync-feishu-config.sh", text)

    def test_repair_workflow_is_manual_and_approved(self) -> None:
        text = (self.ROOT / ".github/workflows/prod-feishu-config-repair.yml").read_text()
        self.assertIn("workflow_dispatch", text)
        self.assertIn("environment: prod", text)
        self.assertIn("sync-feishu-config.sh prod", text)
        self.assertNotIn("schedule:", text)


if __name__ == "__main__":
    unittest.main()
