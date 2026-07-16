#!/usr/bin/env python3
"""Unit tests for interactive REPL HTTP log validation (cli cohort)."""
from __future__ import annotations

import importlib.util
import json
import pathlib
import tempfile
import unittest

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "capture_cc_fingerprint.py"
_spec = importlib.util.spec_from_file_location("capture_cc_fingerprint", _MOD_PATH)
mod = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
import sys

sys.modules[_spec.name] = mod
_spec.loader.exec_module(mod)


def _write_log(path: pathlib.Path, records: list[dict]) -> None:
    path.write_text(
        "\n".join(json.dumps(r, ensure_ascii=False) for r in records) + "\n",
        encoding="utf-8",
    )


class ValidateInteractiveHTTPLogTest(unittest.TestCase):
    def test_accepts_repl_cohort(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            log = pathlib.Path(tmp) / "http.log"
            _write_log(
                log,
                [
                    {
                        "user_agent": "claude-cli/2.1.202 (external, cli)",
                        "system_anchors": [
                            {
                                "text_head": (
                                    "You are Claude Code, Anthropic's official CLI for Claude."
                                )
                            }
                        ],
                    }
                ],
            )
            summary = mod.validate_interactive_http_log(log)
            self.assertEqual(summary["request_count"], 1)
            self.assertIn("(external, cli)", summary["user_agent"])

    def test_rejects_sdk_cli_suffix(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            log = pathlib.Path(tmp) / "http.log"
            _write_log(
                log,
                [
                    {
                        "user_agent": "claude-cli/2.1.202 (external, sdk-cli)",
                        "system_anchors": [
                            {
                                "text_head": (
                                    "You are Claude Code, Anthropic's official CLI for Claude."
                                )
                            }
                        ],
                    }
                ],
            )
            with self.assertRaisesRegex(ValueError, "expected UA suffix"):
                mod.validate_interactive_http_log(log)

    def test_rejects_missing_repl_banner(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            log = pathlib.Path(tmp) / "http.log"
            _write_log(
                log,
                [
                    {
                        "user_agent": "claude-cli/2.1.202 (external, cli)",
                        "system_anchors": [
                            {
                                "text_head": (
                                    "You are a Claude agent, built on Anthropic's Claude Agent SDK."
                                )
                            }
                        ],
                    }
                ],
            )
            with self.assertRaisesRegex(ValueError, "missing Claude Code REPL identity banner"):
                mod.validate_interactive_http_log(log)


class InteractiveDriverContractTest(unittest.TestCase):
    def test_timeout_exits_instead_of_restarting_forever(self) -> None:
        script = (_MOD_PATH.parent / "capture_interactive_repl.exp").read_text(
            encoding="utf-8"
        )
        timeout_body = script.split("timeout {", 1)[1].split("}", 1)[0]
        self.assertIn("exit 124", timeout_body)
        self.assertNotIn("exp_continue", timeout_body)

    def test_pcap_requires_pre_authorized_noninteractive_sudo(self) -> None:
        script = (_MOD_PATH.parent / "capture-cc-interactive.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("sudo -n true", script)
        self.assertIn("sudo -n tcpdump", script)
        self.assertNotIn("sudo tcpdump", script)

    def test_headless_capture_declares_allowed_tools(self) -> None:
        script = (_MOD_PATH.parent / "http_capture_invoke.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("--allowedTools ''", script)


if __name__ == "__main__":
    unittest.main()
