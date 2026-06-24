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


def _run_helper(fn: str, models_file: Path, model: str) -> subprocess.CompletedProcess[str]:
    script = f"""
set -euo pipefail
source "{SMOKE_LIB}"
{fn} "{models_file}" "{model}"
"""
    return subprocess.run(
        ["bash", "-c", script],
        text=True,
        capture_output=True,
        check=False,
    )


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
