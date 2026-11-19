#!/usr/bin/env python3
"""Unit tests for client-release-watch sticky board helpers."""
from __future__ import annotations

import json
import sys
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))

from open_client_release_watch_issues import (
    LABEL_BOARD,
    _is_automated_legacy_drift_issue,
    board_body_path,
    build_board_body,
    filename_safe,
    issue_body_path,
    sync_issues,
)


def _drift_report(*platform_ids: str) -> dict:
    platforms = []
    for platform_id in platform_ids:
        platforms.append({
            "id": platform_id,
            "name": platform_id,
            "pinned": "1.0.0",
            "upstream_latest": "1.0.1",
            "status": "drift",
            "pin_path": f"pins/{platform_id}",
            "skill": f"tokenkey-{platform_id}-fingerprint-alignment",
            "upstream_sources": {"npm": {"version": "1.0.1", "url": "https://example.com"}},
            "drift": True,
        })
    return {
        "run_url": "https://github.com/youxuanxue/sub2api/actions/runs/1",
        "platforms": platforms,
    }


class OpenClientReleaseWatchIssuesTest(unittest.TestCase):
    def test_issue_body_path_defaults_to_board(self) -> None:
        cache = Path(".cache/fingerprint/client-release-watch")
        self.assertEqual(issue_body_path(cache).name, "issue-board.md")
        self.assertEqual(board_body_path(cache).name, "issue-board.md")
        self.assertEqual(
            issue_body_path(cache, "claude-code").name,
            f"issue-{filename_safe('claude-code')}.md",
        )

    def test_build_board_body_lists_drift_and_aligned(self) -> None:
        report = {
            "run_url": "https://example/run",
            "platforms": [
                {
                    "id": "claude-code",
                    "name": "Claude Code",
                    "pinned": "2.1.327",
                    "upstream_latest": "2.1.328",
                    "status": "drift",
                    "skill": "tokenkey-cc-fingerprint-alignment",
                    "upstream_sources": {"npm": {"version": "2.1.328", "url": "https://npm"}},
                    "drift": True,
                },
                {
                    "id": "codex",
                    "name": "Codex",
                    "pinned": "0.170.0",
                    "upstream_latest": "0.170.0",
                    "status": "aligned",
                    "drift": False,
                },
            ],
        }
        body = build_board_body(report)
        self.assertIn("## Current drift", body)
        self.assertIn("`claude-code`", body)
        self.assertIn("tokenkey-cc-fingerprint-alignment", body)
        self.assertIn("## Aligned / non-actionable", body)
        self.assertIn("`codex`", body)
        self.assertIn("https://example/run", body)

    def test_sync_issues_returns_no_links_for_aligned_report(self) -> None:
        report = {
            "platforms": [
                {
                    "id": "codex-cli",
                    "name": "Codex CLI",
                    "pinned": "1",
                    "upstream_latest": "1",
                    "drift": False,
                }
            ]
        }
        with tempfile.TemporaryDirectory() as tmp, \
            patch("open_client_release_watch_issues.ensure_base_labels"), \
            patch("open_client_release_watch_issues.ensure_label"), \
            patch("open_client_release_watch_issues.sh") as mock_sh:
            mock_sh.return_value.stdout = "[]"
            self.assertEqual(sync_issues(report, cache_dir=Path(tmp), umbrella=True), [])

    def test_sync_issues_updates_existing_open_board_and_links_each_platform(self) -> None:
        report = _drift_report("claude-code", "gemini-cli")

        def fake_sh(args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
            if args[:3] == ["gh", "issue", "list"] and LABEL_BOARD in args:
                return subprocess.CompletedProcess(
                    args,
                    0,
                    stdout=json.dumps([
                        {"number": 4000, "state": "OPEN", "updatedAt": "2026-11-19T00:00:00Z"},
                    ]),
                    stderr="",
                )
            if args[:3] == ["gh", "issue", "list"] and "client-release:drift" in args:
                return subprocess.CompletedProcess(args, 0, stdout="[]", stderr="")
            if args[:3] == ["gh", "issue", "view"]:
                return subprocess.CompletedProcess(
                    args,
                    0,
                    stdout="https://github.com/youxuanxue/sub2api/issues/4000\n",
                    stderr="",
                )
            return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

        with tempfile.TemporaryDirectory() as tmp, \
            patch("open_client_release_watch_issues.ensure_base_labels"), \
            patch("open_client_release_watch_issues.ensure_label"), \
            patch("open_client_release_watch_issues.sh", side_effect=fake_sh):
            links = sync_issues(report, cache_dir=Path(tmp), umbrella=True)
            self.assertEqual(len(links), 2)
            self.assertEqual({link["platform_id"] for link in links}, {"claude-code", "gemini-cli"})
            for link in links:
                self.assertEqual(link["number"], 4000)
                self.assertEqual(link["status"], "updated")
                self.assertTrue(link["board"])
                self.assertEqual(link["url"], "https://github.com/youxuanxue/sub2api/issues/4000")
            self.assertTrue((Path(tmp) / "issue-board.md").is_file())

    def test_sync_issues_creates_board_and_closes_legacy_per_platform(self) -> None:
        report = _drift_report("claude-code")
        calls: list[list[str]] = []

        def fake_sh(args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
            calls.append(list(args))
            if args[:3] == ["gh", "issue", "list"] and LABEL_BOARD in args:
                return subprocess.CompletedProcess(args, 0, stdout="[]", stderr="")
            if args[:3] == ["gh", "issue", "list"] and "client-release:drift" in args:
                return subprocess.CompletedProcess(
                    args,
                    0,
                    stdout=json.dumps([
                        {
                            "number": 3317,
                            "title": "[client-release] Claude Code upstream 2.1.328 > pin 2.1.327",
                            "labels": [
                                {"name": "automated"},
                                {"name": "client-release:drift"},
                                {"name": "client-release-watch"},
                                {"name": "client-release:claude-code"},
                                {"name": "cr-sig:claude-code-2.1.328"},
                            ],
                        },
                        {
                            "number": 3318,
                            "title": "[client-release] Gemini CLI ...",
                            "labels": [
                                {"name": "automated"},
                                {"name": "client-release:drift"},
                                {"name": "client-release-watch"},
                                {"name": "client-release:gemini-cli"},
                                {"name": "cr-sig:gemini-cli-0.69.0"},
                            ],
                        },
                        {
                            "number": 3399,
                            "title": "[client-release] manual: kiro TLS investigation",
                            "labels": [
                                {"name": "client-release:drift"},
                                {"name": "client-release-watch"},
                                {"name": "client-release:kiro-cli"},
                                {"name": "needs-triage"},
                            ],
                        },
                    ]),
                    stderr="",
                )
            if args[:3] == ["gh", "issue", "create"]:
                return subprocess.CompletedProcess(
                    args,
                    0,
                    stdout="https://github.com/youxuanxue/sub2api/issues/4400",
                    stderr="",
                )
            if args[:3] == ["gh", "issue", "view"]:
                return subprocess.CompletedProcess(
                    args,
                    0,
                    stdout="https://github.com/youxuanxue/sub2api/issues/4400\n",
                    stderr="",
                )
            return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

        with tempfile.TemporaryDirectory() as tmp, \
            patch("open_client_release_watch_issues.ensure_base_labels"), \
            patch("open_client_release_watch_issues.ensure_label"), \
            patch("open_client_release_watch_issues.sh", side_effect=fake_sh):
            links = sync_issues(report, cache_dir=Path(tmp), umbrella=True)

        self.assertEqual(len(links), 1)
        self.assertEqual(links[0]["number"], 4400)
        self.assertEqual(links[0]["status"], "created")
        self.assertTrue(any(call[:4] == ["gh", "issue", "close", "3317"] for call in calls))
        self.assertTrue(any(call[:4] == ["gh", "issue", "close", "3318"] for call in calls))
        self.assertFalse(any(call[:4] == ["gh", "issue", "close", "3399"] for call in calls))
        create = next(call for call in calls if call[:3] == ["gh", "issue", "create"])
        self.assertIn(LABEL_BOARD, ",".join(create))

    def test_is_automated_legacy_drift_issue_requires_bot_markers(self) -> None:
        self.assertTrue(_is_automated_legacy_drift_issue({
            "automated",
            "client-release-watch",
            "client-release:drift",
            "client-release:claude-code",
            "cr-sig:claude-code-2.1.328",
        }))
        self.assertFalse(_is_automated_legacy_drift_issue({
            "client-release-watch",
            "client-release:drift",
            "client-release:kiro-cli",
            "needs-triage",
        }))
        self.assertFalse(_is_automated_legacy_drift_issue({
            "automated",
            "client-release-watch",
            "client-release:drift",
            LABEL_BOARD,
        }))

    def test_sync_issues_reopens_closed_board_on_new_drift(self) -> None:
        report = _drift_report("kiro-cli")
        calls: list[list[str]] = []

        def fake_sh(args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
            calls.append(list(args))
            if args[:3] == ["gh", "issue", "list"] and LABEL_BOARD in args:
                return subprocess.CompletedProcess(
                    args,
                    0,
                    stdout=json.dumps([
                        {"number": 4100, "state": "CLOSED", "updatedAt": "2026-11-18T00:00:00Z"},
                    ]),
                    stderr="",
                )
            if args[:3] == ["gh", "issue", "list"] and "client-release:drift" in args:
                return subprocess.CompletedProcess(args, 0, stdout="[]", stderr="")
            if args[:3] == ["gh", "issue", "view"]:
                return subprocess.CompletedProcess(
                    args,
                    0,
                    stdout="https://github.com/youxuanxue/sub2api/issues/4100\n",
                    stderr="",
                )
            return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

        with tempfile.TemporaryDirectory() as tmp, \
            patch("open_client_release_watch_issues.ensure_base_labels"), \
            patch("open_client_release_watch_issues.ensure_label"), \
            patch("open_client_release_watch_issues.sh", side_effect=fake_sh):
            links = sync_issues(report, cache_dir=Path(tmp), umbrella=False)

        self.assertEqual(links[0]["status"], "reopened")
        self.assertTrue(any(call[:3] == ["gh", "issue", "reopen"] and "4100" in call for call in calls))

    def test_sync_issues_closes_open_board_when_aligned(self) -> None:
        report = {
            "run_url": "https://github.com/youxuanxue/sub2api/actions/runs/1",
            "platforms": [
                {
                    "id": "codex",
                    "pinned": "1",
                    "upstream_latest": "1",
                    "status": "aligned",
                    "drift": False,
                }
            ],
        }
        calls: list[list[str]] = []

        def fake_sh(args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
            calls.append(list(args))
            if args[:3] == ["gh", "issue", "list"] and LABEL_BOARD in args:
                return subprocess.CompletedProcess(
                    args,
                    0,
                    stdout=json.dumps([
                        {"number": 4200, "state": "OPEN", "updatedAt": "2026-11-19T00:00:00Z"},
                    ]),
                    stderr="",
                )
            if args[:3] == ["gh", "issue", "list"] and "client-release:drift" in args:
                return subprocess.CompletedProcess(args, 0, stdout="[]", stderr="")
            return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

        with tempfile.TemporaryDirectory() as tmp, \
            patch("open_client_release_watch_issues.ensure_base_labels"), \
            patch("open_client_release_watch_issues.ensure_label"), \
            patch("open_client_release_watch_issues.sh", side_effect=fake_sh):
            links = sync_issues(report, cache_dir=Path(tmp), umbrella=True)

        self.assertEqual(links, [])
        self.assertTrue(any(call[:4] == ["gh", "issue", "close", "4200"] for call in calls))


if __name__ == "__main__":
    unittest.main()
