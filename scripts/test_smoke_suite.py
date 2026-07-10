#!/usr/bin/env python3
"""Tests for gateway smoke suite gating and model pick logic."""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import unittest

from scripts.stage0.smoke_suite import (
    edge_phase_gateway_suite,
    edge_phase_runs_native_oauth,
    needs_chat_model,
    normalize_suite,
    pick_model,
    suite_runs,
)

REPO_ROOT = pathlib.Path(__file__).resolve().parents[1]
SMOKE_LIB = REPO_ROOT / "ops" / "stage0" / "smoke_lib.sh"


class SmokeSuiteTest(unittest.TestCase):
    def test_normalize_aliases(self) -> None:
        self.assertEqual(normalize_suite("prod"), "full")
        self.assertEqual(normalize_suite("edge-via-prod"), "main-via-edge")
        self.assertEqual(normalize_suite("minimal"), "quick")

    def test_main_via_edge_skips_chat(self) -> None:
        self.assertTrue(suite_runs("messages", "main-via-edge"))
        self.assertFalse(suite_runs("chat", "main-via-edge"))
        self.assertFalse(suite_runs("gemini", "main-via-edge"))

    def test_full_runs_all_sections(self) -> None:
        for section in (
            "public",
            "frontend",
            "models",
            "chat",
            "messages",
            "gemini",
            "openai_oauth",
        ):
            self.assertTrue(suite_runs(section, "full"))

    def test_pick_model_fallback_when_override_missing(self) -> None:
        models = [{"id": "gpt-4o"}, {"id": "claude-sonnet-4-6"}]
        model, warn = pick_model(models, "claude-sonnet-4-6")
        self.assertEqual(model, "claude-sonnet-4-6")
        self.assertIsNone(warn)

        model, warn = pick_model(models, "claude-opus-4")
        self.assertEqual(model, "claude-sonnet-4-6")
        self.assertIn("not listed", warn or "")

    def test_edge_chat_model_gate(self) -> None:
        self.assertFalse(needs_chat_model("main-via-edge", "infra"))
        self.assertFalse(needs_chat_model("main-via-edge", "api"))
        self.assertFalse(needs_chat_model("infra", "api"))
        self.assertFalse(needs_chat_model("full", "infra"))

    def test_edge_phase_gateway_suite(self) -> None:
        self.assertEqual(edge_phase_gateway_suite("main-via-edge"), "main-via-edge")
        self.assertIsNone(edge_phase_gateway_suite("full"))
        self.assertIsNone(edge_phase_gateway_suite("infra"))

    def test_edge_phase_native_oauth(self) -> None:
        self.assertTrue(edge_phase_runs_native_oauth("full"))
        self.assertTrue(edge_phase_runs_native_oauth("edge-native-oauth"))
        self.assertFalse(edge_phase_runs_native_oauth("infra"))
        self.assertFalse(edge_phase_runs_native_oauth("main-via-edge"))


class SoftDegradeOrExitTest(unittest.TestCase):
    """Pins the soft/hard-fail contract of ops/stage0/smoke_lib.sh::soft_degrade_or_exit.

    The function is bash; we exercise it via `bash -c "source ...; soft_degrade_or_exit ..."`.
    Stdout marker (`MARKER=continue` / `MARKER=softskip`) tells us which branch
    the caller pattern took; exit code separates soft-skip (0) from hard-fail (1).
    """

    @staticmethod
    def _run(suite: str, http: str, body: dict, label: str = "/v1/x") -> tuple[int, str]:
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump(body, f)
            resp_path = f.name
        script = (
            f'source "{SMOKE_LIB}"\n'
            f'if soft_degrade_or_exit "{label}" "{http}" "{resp_path}"; then\n'
            f'  echo MARKER=continue\n'
            f'else\n'
            f'  echo MARKER=softskip\n'
            f'fi\n'
        )
        env = os.environ.copy()
        env["GATEWAY_SMOKE_SUITE"] = suite
        try:
            proc = subprocess.run(
                ["bash", "-c", script],
                capture_output=True,
                text=True,
                env=env,
                check=False,
            )
        finally:
            pathlib.Path(resp_path).unlink(missing_ok=True)
        return proc.returncode, proc.stdout + proc.stderr

    def test_full_403_claude_code_only_soft_skips(self) -> None:
        rc, out = self._run(
            "full", "403",
            {"error": {"message": "This group is restricted to Claude Code clients (/v1/messages only)"}},
        )
        self.assertEqual(rc, 0, out)
        self.assertIn("MARKER=softskip", out)
        self.assertIn("soft-skipped", out)

    def test_main_via_edge_403_claude_code_only_hard_fails(self) -> None:
        rc, out = self._run(
            "main-via-edge", "403",
            {"error": {"message": "This group is restricted to Claude Code clients (/v1/messages only)"}},
        )
        self.assertEqual(rc, 1, out)
        self.assertNotIn("MARKER=continue", out)

    def test_full_503_no_available_accounts_soft_skips(self) -> None:
        rc, out = self._run(
            "full", "503", {"error": {"message": "no available accounts"}},
        )
        self.assertEqual(rc, 0, out)
        self.assertIn("MARKER=softskip", out)

    def test_full_200_continues(self) -> None:
        rc, out = self._run("full", "200", {"ok": True})
        self.assertEqual(rc, 0, out)
        self.assertIn("MARKER=continue", out)

    def test_full_403_unrelated_hard_fails(self) -> None:
        rc, out = self._run(
            "full", "403", {"error": {"message": "invalid api key"}},
        )
        self.assertEqual(rc, 1, out)
        self.assertNotIn("MARKER=continue", out)

    def test_full_chat_400_unsupported_model_hard_fails(self) -> None:
        rc, out = self._run(
            "full",
            "400",
            {"error": {"message": "Unsupported model: claude-sonnet-4-6"}},
            label="/v1/chat/completions",
        )
        self.assertEqual(rc, 1, out)
        self.assertNotIn("MARKER=continue", out)

    def test_full_messages_400_unsupported_model_hard_fails(self) -> None:
        rc, out = self._run(
            "full",
            "400",
            {"error": {"message": "Unsupported model: claude-sonnet-4-6"}},
            label="/v1/messages",
        )
        self.assertEqual(rc, 1, out)
        self.assertNotIn("MARKER=continue", out)


if __name__ == "__main__":
    unittest.main()
