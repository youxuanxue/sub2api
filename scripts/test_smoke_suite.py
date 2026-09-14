#!/usr/bin/env python3
"""Tests for gateway smoke suite gating and model pick logic."""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[1]
SMOKE_LIB = REPO_ROOT / "ops" / "stage0" / "smoke_lib.sh"


class SmokeSuiteTest(unittest.TestCase):
    """Execute the Bash owner so runtime changes cannot leave a green mirror."""

    @staticmethod
    def _run(suite: str, section: str | None = None) -> subprocess.CompletedProcess[str]:
        command = 'source "$1"; '
        command += 'smoke_suite_runs "$2"' if section is not None else 'printf "%s" "$GATEWAY_SMOKE_SUITE"'
        return subprocess.run(
            ["bash", "-c", command, "smoke-test", str(SMOKE_LIB), section or ""],
            env={"PATH": os.environ["PATH"], "GATEWAY_SMOKE_SUITE": suite},
            capture_output=True, text=True, check=False,
        )

    def test_normalize_aliases(self) -> None:
        for alias, expected in (("prod", "full"), ("edge-via-prod", "main-via-edge"), ("minimal", "quick"), ("", "full")):
            with self.subTest(alias=alias):
                proc = self._run(alias)
                self.assertEqual(proc.returncode, 0, proc.stderr)
                self.assertEqual(proc.stdout, expected)

    def test_unknown_suite_fails(self) -> None:
        proc = self._run("unknown")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("unknown GATEWAY_SMOKE_SUITE", proc.stderr)

    def test_scoped_suites(self) -> None:
        for suite, allowed, blocked in (
            ("main-via-edge", "messages", "chat"),
            ("quick", "chat", "messages"),
        ):
            with self.subTest(suite=suite):
                self.assertEqual(self._run(suite, allowed).returncode, 0)
                self.assertEqual(self._run(suite, blocked).returncode, 1)
                self.assertEqual(self._run(suite, "responses").returncode, 1)

    def test_full_runs_responses(self) -> None:
        # The removed Python mirror silently omitted this live smoke section.
        proc = self._run("full", "responses")
        self.assertEqual(proc.returncode, 0, proc.stderr)


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

    def test_full_403_failover_terminal_soft_skips(self) -> None:
        rc, out = self._run(
            "full", "403", {"error": {"message": "Upstream request could not be completed"}},
        )
        self.assertEqual(rc, 0, out)
        self.assertIn("MARKER=softskip", out)

    def test_full_403_legacy_failover_terminal_soft_skips(self) -> None:
        rc, out = self._run(
            "full", "403", {"error": {"message": "All available accounts exhausted"}},
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
