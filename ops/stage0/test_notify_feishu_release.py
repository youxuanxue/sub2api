#!/usr/bin/env python3
"""Contract tests for Feishu prod rollout changelog baselines."""
from __future__ import annotations

import os
import json
import tempfile
from unittest.mock import patch
import pathlib
import subprocess
import unittest


SCRIPT = pathlib.Path(__file__).with_name("notify-feishu-release.sh")


class NotifyFeishuReleaseTest(unittest.TestCase):
    def test_qa_notice_reports_component_acceptance_and_handles_first_install(self):
        proc = self.run_script("1.8.228", "https://api.example.com", "--component", "qa", "--dry-run")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("QA 验收通过", proc.stdout)
        self.assertIn("首次安装", proc.stdout)
        self.assertNotIn("发版上线", proc.stdout)
        proc = self.run_script("1.8.228", "https://api.example.com", "--component", "unknown", "--dry-run")
        self.assertNotEqual(proc.returncode, 0)

    def run_script(self, *args: str) -> subprocess.CompletedProcess[str]:
        env = {**os.environ, "GITHUB_REPOSITORY": "youxuanxue/sub2api"}
        return subprocess.run(
            ["bash", str(SCRIPT), *args],
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )

    def test_dry_run_shows_explicit_live_baseline(self) -> None:
        proc = self.run_script(
            "1.8.163",
            "https://api.tokenkey.dev",
            "--previous-tag",
            "v1.8.161",
            "--notes",
            "- fix: timezone worker startup\n- feat: example",
            "--dry-run",
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIn("线上基线", proc.stdout)
        self.assertIn("v1.8.161", proc.stdout)
        self.assertIn("timezone worker startup", proc.stdout)

    def test_domestic_card_uses_real_environment_and_cutover_time(self):
        proc = self.run_script('1.8.300', 'https://106.75.29.139', '--previous-tag', '1.8.298',
                               '--environment', 'cn-prod', '--deployed-at', '2026-10-16T12:51:11Z', '--dry-run')
        self.assertEqual(proc.returncode, 0, proc.stderr)
        card = json.loads(proc.stdout[proc.stdout.index('{'):])['card']
        self.assertIn('cn-prod', card['header']['title']['content'])
        body = card['elements'][0]['text']['content']
        self.assertIn('**环境**  cn-prod', body)
        self.assertIn('2026-10-16 20:51 CST', body)
        self.assertIn('https://106.75.29.139', body)

    def test_delivery_receipt_requires_explicit_feishu_success(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            fake = root / 'curl'
            fake.write_text('#!/usr/bin/env python3\nimport os,pathlib,sys\na=sys.argv\npathlib.Path(a[a.index("-o")+1]).write_text(os.environ["FAKE_BODY"])\nprint("200",end="")\n')
            fake.chmod(0o755)
            env = {'PATH': str(root) + os.pathsep + os.environ['PATH'],
                   'TK_FEISHU_WEBHOOK_URL': 'https://example.invalid/SECRET_WEBHOOK',
                   'TK_FEISHU_SIGNING_SECRET': 'SECRET_SIGNING'}
            for body, expected in [('{"code":0}', 'sent'), ('{"code":19021}', 'failed'),
                                   ('not-json', 'failed'), ('{}', 'failed'), ('{"code":false}', 'failed')]:
                with self.subTest(body=body), patch.dict(os.environ, env | {'FAKE_BODY': body}):
                    receipt = root / 'receipt.json'
                    proc = self.run_script('1.8.300', 'https://106.75.29.139', '--previous-tag', '1.8.298',
                                           '--environment', 'cn-prod', '--result-file', str(receipt))
                    self.assertEqual(proc.returncode, 0, proc.stderr)  # best-effort deployment contract
                    self.assertEqual(json.loads(receipt.read_text())['status'], expected)
                    self.assertNotIn('SECRET_WEBHOOK', proc.stdout + proc.stderr)
                    self.assertNotIn('SECRET_SIGNING', proc.stdout + proc.stderr)

    def test_previous_tag_is_required_for_rollout_notes(self) -> None:
        proc = self.run_script(
            "1.8.163",
            "https://api.tokenkey.dev",
            "--notes",
            "- fix: missing baseline",
            "--dry-run",
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("--previous-tag", proc.stderr)


if __name__ == "__main__":
    unittest.main()
