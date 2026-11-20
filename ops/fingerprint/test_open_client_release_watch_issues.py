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
    LABEL_DRIFT,
    LABEL_IN_PROGRESS,
    LABEL_BLOCKED,
    LABEL_NEEDS_TRIAGE,
    UMBRELLA_SKILL,
    _is_automated_legacy_drift_issue,
    board_body_path,
    build_board_body,
    converge_board_labels,
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
    def test_blocked_board_rearms_on_next_drift_scan(self) -> None:
        for in_progress in (False, True):
            with self.subTest(in_progress=in_progress):
                state = {LABEL_BOARD, LABEL_BLOCKED, LABEL_NEEDS_TRIAGE}
                if in_progress:
                    state.update({LABEL_IN_PROGRESS, LABEL_DRIFT})

                def fake_sh(args, *, check=True):
                    if "--add-label" in args:
                        state.add(args[args.index("--add-label") + 1])
                    if "--remove-label" in args:
                        state.discard(args[args.index("--remove-label") + 1])
                    return subprocess.CompletedProcess(args, 0, stdout=json.dumps(sorted(state)), stderr="")

                with patch("open_client_release_watch_issues.sh", side_effect=fake_sh):
                    labels = converge_board_labels(
                        "4100", umbrella=False, drifting=True, current=set(state),
                    )
                self.assertEqual(LABEL_DRIFT in labels, not in_progress)
                self.assertEqual(LABEL_BLOCKED in labels, in_progress)
                self.assertNotIn(LABEL_NEEDS_TRIAGE, labels)
                self.assertEqual(LABEL_IN_PROGRESS in labels, in_progress)

    def test_issue_body_path_defaults_to_board(self) -> None:
        cache = Path(".cache/fingerprint/client-release-watch")
        self.assertEqual(issue_body_path(cache).name, "issue-board.md")
        self.assertEqual(board_body_path(cache).name, "issue-board.md")
        self.assertEqual(
            issue_body_path(cache, "claude-code").name,
            f"issue-{filename_safe('claude-code')}.md",
        )

    def test_build_board_body_uses_umbrella_skill_and_platform_table(self) -> None:
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
        self.assertIn("## Platforms", body)
        self.assertIn("| Client | Pin | Upstream | Drift | Evidence | Block |", body)
        self.assertIn("`claude-code`", body)
        self.assertIn("release-metadata-only", body)
        self.assertIn(UMBRELLA_SKILL, body)
        self.assertNotIn("tokenkey-cc-fingerprint-alignment", body)
        self.assertIn(f"`{LABEL_DRIFT}`", body)
        self.assertIn("## When NOT to open a PR", body)
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
        label_state = {LABEL_BOARD}

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
            if args[:3] == ["gh", "issue", "list"] and LABEL_DRIFT in args:
                return subprocess.CompletedProcess(args, 0, stdout="[]", stderr="")
            if args[:3] == ["gh", "issue", "edit"] and "--add-label" in args:
                label_state.add(args[args.index("--add-label") + 1])
            if args[:3] == ["gh", "issue", "edit"] and "--remove-label" in args:
                label_state.discard(args[args.index("--remove-label") + 1])
            if args[:3] == ["gh", "issue", "view"] and "--jq" in args and "labels" in args:
                return subprocess.CompletedProcess(
                    args, 0, stdout=json.dumps(sorted(label_state)), stderr=""
                )
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
                self.assertTrue(link["forge_actionable"])
                self.assertEqual(link["url"], "https://github.com/youxuanxue/sub2api/issues/4000")
            self.assertTrue((Path(tmp) / "issue-board.md").is_file())

    def test_sync_issues_creates_board_and_closes_legacy_per_platform(self) -> None:
        report = _drift_report("claude-code")
        calls: list[list[str]] = []
        label_state: set[str] = set()

        def fake_sh(args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
            calls.append(list(args))
            if args[:3] == ["gh", "issue", "list"] and LABEL_BOARD in args:
                return subprocess.CompletedProcess(args, 0, stdout="[]", stderr="")
            if args[:3] == ["gh", "issue", "list"] and LABEL_DRIFT in args:
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
                # Mirror create --label CSV into stateful label set.
                if "--label" in args:
                    for name in args[args.index("--label") + 1].split(","):
                        label_state.add(name)
                return subprocess.CompletedProcess(
                    args,
                    0,
                    stdout="https://github.com/youxuanxue/sub2api/issues/4400",
                    stderr="",
                )
            if args[:3] == ["gh", "issue", "edit"] and "--add-label" in args:
                label_state.add(args[args.index("--add-label") + 1])
            if args[:3] == ["gh", "issue", "edit"] and "--remove-label" in args:
                label_state.discard(args[args.index("--remove-label") + 1])
            if args[:3] == ["gh", "issue", "view"] and "--jq" in args and "labels" in args:
                return subprocess.CompletedProcess(
                    args, 0, stdout=json.dumps(sorted(label_state)), stderr=""
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
        self.assertTrue(links[0]["forge_actionable"])
        self.assertTrue(any(call[:4] == ["gh", "issue", "close", "3317"] for call in calls))
        self.assertTrue(any(call[:4] == ["gh", "issue", "close", "3318"] for call in calls))
        self.assertFalse(any(call[:4] == ["gh", "issue", "close", "3399"] for call in calls))
        create = next(call for call in calls if call[:3] == ["gh", "issue", "create"])
        self.assertIn(LABEL_BOARD, ",".join(create))
        self.assertIn(LABEL_DRIFT, ",".join(create))

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

    def test_sync_issues_disarms_drift_when_in_progress(self) -> None:
        report = _drift_report("kiro-cli")
        calls: list[list[str]] = []
        label_state = {LABEL_BOARD, LABEL_DRIFT, LABEL_IN_PROGRESS}

        def fake_sh(args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
            calls.append(list(args))
            if args[:3] == ["gh", "issue", "list"] and LABEL_BOARD in args:
                return subprocess.CompletedProcess(
                    args,
                    0,
                    stdout=json.dumps([
                        {"number": 4100, "state": "OPEN", "updatedAt": "2026-11-18T00:00:00Z"},
                    ]),
                    stderr="",
                )
            if args[:3] == ["gh", "issue", "list"] and LABEL_DRIFT in args:
                return subprocess.CompletedProcess(args, 0, stdout="[]", stderr="")
            if args[:3] == ["gh", "issue", "edit"] and "--add-label" in args:
                label_state.add(args[args.index("--add-label") + 1])
            if args[:3] == ["gh", "issue", "edit"] and "--remove-label" in args:
                label_state.discard(args[args.index("--remove-label") + 1])
            if args[:3] == ["gh", "issue", "view"] and "--jq" in args and "labels" in args:
                return subprocess.CompletedProcess(
                    args,
                    0,
                    stdout=json.dumps(sorted(label_state)),
                    stderr="",
                )
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
            self.assertEqual(links[0]["status"], "updated")
            self.assertFalse(links[0]["forge_actionable"])
            self.assertTrue(
                any(
                    call[:4] == ["gh", "issue", "edit", "4100"] and "--remove-label" in call and LABEL_DRIFT in call
                    for call in calls
                )
            )
            body = (Path(tmp) / "issue-board.md").read_text(encoding="utf-8")
            self.assertIn(LABEL_IN_PROGRESS, body)
            self.assertIn(UMBRELLA_SKILL, body)

    def test_sync_issues_reopens_closed_board_on_new_drift(self) -> None:
        report = _drift_report("kiro-cli")
        calls: list[list[str]] = []
        label_state: set[str] = set()

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
            if args[:3] == ["gh", "issue", "list"] and LABEL_DRIFT in args:
                return subprocess.CompletedProcess(args, 0, stdout="[]", stderr="")
            if args[:3] == ["gh", "issue", "edit"] and "--add-label" in args:
                label_state.add(args[args.index("--add-label") + 1])
            if args[:3] == ["gh", "issue", "edit"] and "--remove-label" in args:
                label_state.discard(args[args.index("--remove-label") + 1])
            if args[:3] == ["gh", "issue", "view"] and "--jq" in args and "labels" in args:
                return subprocess.CompletedProcess(
                    args, 0, stdout=json.dumps(sorted(label_state)), stderr=""
                )
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

    def test_converge_board_labels_rereads_after_failed_add(self) -> None:
        """R-001: check=False edit failures must not inflate forge_actionable."""
        label_state = {LABEL_BOARD}

        def fake_sh(args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
            if args[:3] == ["gh", "issue", "edit"] and "--add-label" in args:
                name = args[args.index("--add-label") + 1]
                # Simulate GitHub rejecting drift arm while identity labels succeed.
                if name == LABEL_DRIFT:
                    return subprocess.CompletedProcess(args, 1, stdout="", stderr="denied")
                label_state.add(name)
                return subprocess.CompletedProcess(args, 0, stdout="", stderr="")
            if args[:3] == ["gh", "issue", "edit"] and "--remove-label" in args:
                label_state.discard(args[args.index("--remove-label") + 1])
                return subprocess.CompletedProcess(args, 0, stdout="", stderr="")
            if args[:3] == ["gh", "issue", "view"] and "--jq" in args and "labels" in args:
                return subprocess.CompletedProcess(
                    args, 0, stdout=json.dumps(sorted(label_state)), stderr=""
                )
            return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

        with patch("open_client_release_watch_issues.sh", side_effect=fake_sh):
            result = converge_board_labels(
                "5000",
                umbrella=True,
                drifting=True,
                current={LABEL_BOARD},
            )

        self.assertNotIn(LABEL_DRIFT, result)
        self.assertIn(LABEL_BOARD, result)

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
            if args[:3] == ["gh", "issue", "list"] and LABEL_DRIFT in args:
                return subprocess.CompletedProcess(args, 0, stdout="[]", stderr="")
            if args[:3] == ["gh", "issue", "view"] and "--jq" in args and "labels" in args:
                return subprocess.CompletedProcess(
                    args, 0, stdout=json.dumps([LABEL_BOARD, LABEL_DRIFT, "needs-triage"]), stderr=""
                )
            return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

        with tempfile.TemporaryDirectory() as tmp, \
            patch("open_client_release_watch_issues.ensure_base_labels"), \
            patch("open_client_release_watch_issues.ensure_label"), \
            patch("open_client_release_watch_issues.sh", side_effect=fake_sh):
            links = sync_issues(report, cache_dir=Path(tmp), umbrella=True)

        self.assertEqual(links, [])
        self.assertTrue(any(call[:4] == ["gh", "issue", "close", "4200"] for call in calls))
        self.assertTrue(
            any(
                call[:4] == ["gh", "issue", "edit", "4200"] and "--remove-label" in call and LABEL_DRIFT in call
                for call in calls
            )
        )


if __name__ == "__main__":
    unittest.main()
