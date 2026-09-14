#!/usr/bin/env python3
"""Contract tests for Feishu prod rollout changelog baselines."""
from __future__ import annotations

import os
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
