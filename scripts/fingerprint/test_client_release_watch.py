#!/usr/bin/env python3
"""Tests for scripts/fingerprint/client_release_watch.py"""
from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

# Import the module under test via path relative to repo root.
REPO_ROOT = Path(__file__).resolve().parents[2]
import sys

sys.path.insert(0, str(REPO_ROOT / "scripts" / "fingerprint"))
import client_release_watch as crw  # noqa: E402


class VersionHelpersTest(unittest.TestCase):
    def test_normalize_brew_and_github_tags(self) -> None:
        self.assertEqual(crw.normalize_version("v2.1.195"), "2.1.195")
        self.assertEqual(crw.normalize_version("rust-v0.142.3"), "0.142.3")
        self.assertEqual(crw.normalize_version("2.2.1,5287492581195776"), "2.2.1")

    def test_version_gt(self) -> None:
        self.assertTrue(crw.version_gt("0.12.333", "0.11.107"))
        self.assertFalse(crw.version_gt("2.0.11", "2.2.1"))


class PinReadersLiveRepoTest(unittest.TestCase):
    def test_live_pins_are_present(self) -> None:
        self.assertRegex(crw.read_pinned_claude_code(), r"^\d+\.\d+\.\d+")
        self.assertRegex(crw.read_pinned_codex(), r"^\d+\.\d+\.\d+")
        self.assertRegex(crw.read_pinned_antigravity(), r"^\d+\.\d+\.\d+")
        self.assertRegex(crw.read_pinned_kiro(), r"^\d+\.\d+\.\d+")
        self.assertRegex(crw.read_pinned_cc_stainless(), r"^\d+\.\d+\.\d+")
        self.assertRegex(crw.read_pinned_gemini_cli(), r"^\d+\.\d+\.\d+")
        self.assertRegex(crw.read_pinned_grok_cli(), r"^\d+\.\d+\.\d+")
        self.assertRegex(crw.read_pinned_kiro_cli(), r"^\d+\.\d+\.\d+")


class ScanPlatformTest(unittest.TestCase):
    def test_offline_fixture_marks_drift(self) -> None:
        spec = next(p for p in crw.PLATFORM_SPECS if p.id == "codex")
        offline = {
            "codex": {
                "npm @openai/codex": {
                    "version": "9.9.9",
                    "url": "https://example.com/codex",
                    "raw_tag": "9.9.9",
                    "published_at": "",
                },
                "GitHub openai/codex": {
                    "version": "9.9.8",
                    "url": "https://example.com/gh",
                    "raw_tag": "rust-v9.9.8",
                    "published_at": "",
                },
            }
        }
        with mock.patch.dict(crw.PIN_READERS, {"codex": lambda: "0.1.0"}):
            result = crw.scan_platform(spec, offline_upstream=offline)
        self.assertTrue(result.drift)
        self.assertEqual(result.upstream_latest, "9.9.9")
        self.assertEqual(result.status, "drift")

    def test_aligned_when_upstream_not_ahead(self) -> None:
        spec = next(p for p in crw.PLATFORM_SPECS if p.id == "codex")
        offline = {
            "codex": {
                "npm @openai/codex": {
                    "version": "0.1.0",
                    "url": "https://example.com/codex",
                    "raw_tag": "0.1.0",
                    "published_at": "",
                },
                "GitHub openai/codex": {
                    "version": "0.1.0",
                    "url": "https://example.com/gh",
                    "raw_tag": "rust-v0.1.0",
                    "published_at": "",
                },
            }
        }
        with mock.patch.dict(crw.PIN_READERS, {"codex": lambda: "0.1.0"}):
            result = crw.scan_platform(spec, offline_upstream=offline)
        self.assertFalse(result.drift)
        self.assertEqual(result.status, "aligned")


class SkillPlanTest(unittest.TestCase):
    def test_plan_lists_skill_for_drift(self) -> None:
        report = {
            "drift_platform_ids": ["kiro"],
            "platforms": [
                {
                    "id": "kiro",
                    "name": "Kiro IDE",
                    "skill": "tokenkey-kiro-fingerprint-alignment",
                    "pinned": "0.11.107",
                    "upstream_latest": "0.12.333",
                }
            ],
        }
        plan = crw.render_skill_plan(report)
        self.assertIn("tokenkey-kiro-fingerprint-alignment", plan)
        self.assertIn("capture-kiro-fingerprint.sh", plan)


class CompanionDedupTest(unittest.TestCase):
    def test_codex_vscode_suppresses_duplicate_issue(self) -> None:
        codex = crw.PlatformResult(
            id="codex",
            name="Codex CLI",
            skill="tokenkey-codex-fingerprint-alignment",
            pin_path="setting_service.go",
            pinned="0.1.0",
            upstream_latest="9.9.9",
            upstream_sources={},
            status="drift",
            drift=True,
        )
        vscode = crw.PlatformResult(
            id="codex-vscode",
            name="Codex VS Code",
            skill="tokenkey-codex-fingerprint-alignment",
            pin_path="same as codex",
            pinned="0.1.0",
            upstream_latest="9.9.9",
            upstream_sources={},
            status="drift",
            drift=True,
        )
        merged = crw.apply_companion_mirror([codex, vscode])
        report = crw.build_report(merged)
        self.assertIn("codex", report["drift_platform_ids"])
        self.assertNotIn("codex-vscode", report["drift_platform_ids"])
        vscode_row = next(p for p in report["platforms"] if p["id"] == "codex-vscode")
        self.assertTrue(vscode_row["issue_suppressed"])


class MainIntegrationTest(unittest.TestCase):
    def test_main_offline_writes_reports(self) -> None:
        fixture = {
            "claude-code": {
                "GitHub anthropics/claude-code": {
                    "version": "1.0.0",
                    "url": "https://example.com/cc",
                    "raw_tag": "v1.0.0",
                    "published_at": "",
                },
                "npm @anthropic-ai/claude-code": {
                    "version": "1.0.0",
                    "url": "https://example.com/npm",
                    "raw_tag": "1.0.0",
                    "published_at": "",
                },
            },
            "cc-stainless": {
                "npm @anthropic-ai/sdk": {
                    "version": "1.0.0",
                    "url": "https://example.com/sdk",
                    "raw_tag": "1.0.0",
                    "published_at": "",
                },
            },
            "codex": {
                "npm @openai/codex": {
                    "version": "1.0.0",
                    "url": "https://example.com/codex",
                    "raw_tag": "1.0.0",
                    "published_at": "",
                },
                "GitHub openai/codex": {
                    "version": "1.0.0",
                    "url": "https://example.com/gh",
                    "raw_tag": "rust-v1.0.0",
                    "published_at": "",
                },
            },
            "codex-vscode": {
                "npm @openai/codex": {
                    "version": "1.0.0",
                    "url": "https://example.com/codex",
                    "raw_tag": "1.0.0",
                    "published_at": "",
                },
                "GitHub openai/codex": {
                    "version": "1.0.0",
                    "url": "https://example.com/gh",
                    "raw_tag": "rust-v1.0.0",
                    "published_at": "",
                },
            },
            "gemini-cli": {
                "npm @google/gemini-cli": {
                    "version": "1.0.0",
                    "url": "https://example.com/gemini",
                    "raw_tag": "1.0.0",
                    "published_at": "",
                },
            },
            "grok-cli": {
                "npm @xai-official/grok": {
                    "version": "1.0.0",
                    "url": "https://example.com/grok",
                    "raw_tag": "1.0.0",
                    "published_at": "",
                },
            },
            "antigravity": {
                "Homebrew cask antigravity": {
                    "version": "1.0.0",
                    "url": "https://example.com/ag",
                    "raw_tag": "1.0.0",
                    "published_at": "",
                },
            },
            "kiro": {
                "Homebrew cask kiro": {
                    "version": "1.0.0",
                    "url": "https://example.com/kiro",
                    "raw_tag": "1.0.0",
                    "published_at": "",
                },
            },
            "kiro-cli": {
                "Homebrew cask kiro-cli": {
                    "version": "1.0.0",
                    "url": "https://example.com/kiro-cli",
                    "raw_tag": "1.0.0",
                    "published_at": "",
                },
            },
        }
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            fixture_path = tmp_path / "fixture.json"
            fixture_path.write_text(json.dumps(fixture), encoding="utf-8")
            report_json = tmp_path / "report.json"
            report_md = tmp_path / "report.md"
            state = tmp_path / "state.json"
            with mock.patch.dict(
                crw.PIN_READERS,
                {
                    "claude-code": lambda: "0.0.1",
                    "cc-stainless": lambda: "0.0.1",
                    "codex": lambda: "0.0.1",
                    "codex-vscode": lambda: "0.0.1",
                    "gemini-cli": lambda: "0.0.1",
                    "grok-cli": lambda: "0.0.1",
                    "antigravity": lambda: "0.0.1",
                    "kiro": lambda: "0.0.1",
                    "kiro-cli": lambda: "0.0.1",
                },
            ):
                code = crw.main(
                    [
                        "--offline-fixture",
                        str(fixture_path),
                        "--report-json",
                        str(report_json),
                        "--report-md",
                        str(report_md),
                        "--state",
                        str(state),
                        "--quiet",
                    ]
                )
            self.assertEqual(code, 1)
            self.assertTrue(report_json.is_file())
            self.assertTrue(state.is_file())
            data = json.loads(report_json.read_text(encoding="utf-8"))
            self.assertEqual(data["summary"]["drift_count"], 8)
            self.assertEqual(data["summary"]["platform_count"], 9)


if __name__ == "__main__":
    unittest.main()
