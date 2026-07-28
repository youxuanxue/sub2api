#!/usr/bin/env python3
"""Behavior tests for ops/stage0/smoke_lib.sh helpers (no network)."""

from __future__ import annotations

import json
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
SMOKE_LIB = REPO_ROOT / "ops" / "stage0" / "smoke_lib.sh"


def _run_bash(source: str) -> subprocess.CompletedProcess[str]:
    script = f"""
set -euo pipefail
source "{SMOKE_LIB}"
{source}
"""
    return subprocess.run(
        ["bash", "-c", script],
        text=True,
        capture_output=True,
        check=False,
    )


def _run_helper(fn: str, models_file: Path, model: str) -> subprocess.CompletedProcess[str]:
    return _run_bash(f'{fn} "{models_file}" "{model}"')


class SmokeLibHumanBaseURLTest(unittest.TestCase):
    def test_derives_apex_from_api_subdomain(self) -> None:
        proc = _run_bash('smoke_human_base_url "https://api.tokenkey.dev"')
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(proc.stdout.strip(), "https://tokenkey.dev")

    def test_keeps_localhost_single_host(self) -> None:
        proc = _run_bash('smoke_human_base_url "http://127.0.0.1:8088"')
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(proc.stdout.strip(), "http://127.0.0.1:8088")

    def test_respects_explicit_site_override(self) -> None:
        proc = subprocess.run(
            [
                "bash",
                "-c",
                f"""
set -euo pipefail
source "{SMOKE_LIB}"
export TOKENKEY_SITE_BASE_URL=https://custom.example.test
smoke_human_base_url "https://api.tokenkey.dev"
""",
            ],
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(proc.stdout.strip(), "https://custom.example.test")


class SmokeLibAnthropicModelListTest(unittest.TestCase):
    def test_anthropic_warns_when_missing_from_universal_model_list(self) -> None:
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as fh:
            json.dump({"object": "list", "data": [{"id": "deepseek-chat"}]}, fh)
            models_path = Path(fh.name)

        proc = _run_helper("smoke_assert_anthropic_model_listed_or_warn", models_path, "claude-sonnet-4-6")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("::warning::", proc.stderr)
        self.assertIn("kiro mirror stubs", proc.stderr)
        self.assertNotIn("::error::", proc.stderr)

    def test_anthropic_passes_when_listed(self) -> None:
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as fh:
            json.dump(
                {"object": "list", "data": [{"id": "claude-sonnet-4-6"}, {"id": "gpt-5.4"}]},
                fh,
            )
            models_path = Path(fh.name)

        proc = _run_helper("smoke_assert_anthropic_model_listed_or_warn", models_path, "claude-sonnet-4-6")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(proc.stderr, "")

    def test_openai_oauth_warns_when_missing_from_universal_model_list(self) -> None:
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as fh:
            json.dump({"object": "list", "data": [{"id": "gemini-2.5-flash"}]}, fh)
            models_path = Path(fh.name)

        proc = _run_helper(
            "smoke_assert_openai_oauth_model_listed_or_warn", models_path, "gpt-5.4"
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("::warning::", proc.stderr)
        self.assertIn("empty model_mapping passthrough", proc.stderr)
        self.assertNotIn("::error::", proc.stderr)

    def test_openai_oauth_passes_when_listed(self) -> None:
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as fh:
            json.dump({"object": "list", "data": [{"id": "gpt-5.4"}]}, fh)
            models_path = Path(fh.name)

        proc = _run_helper(
            "smoke_assert_openai_oauth_model_listed_or_warn", models_path, "gpt-5.4"
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(proc.stderr, "")

    def test_strict_assert_fails_when_missing(self) -> None:
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as fh:
            json.dump({"object": "list", "data": [{"id": "gemini-2.5-flash"}]}, fh)
            models_path = Path(fh.name)

        script = f"""
set -euo pipefail
source "{SMOKE_LIB}"
smoke_assert_model_listed "{models_path}" "gemini" "gemini-3.1-pro-preview"
"""
        proc = subprocess.run(["bash", "-c", script], text=True, capture_output=True, check=False)
        self.assertEqual(proc.returncode, 1)
        self.assertIn("::error::", proc.stderr)


if __name__ == "__main__":
    unittest.main()
