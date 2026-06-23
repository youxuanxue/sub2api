#!/usr/bin/env python3
"""Tests for Stage0 smoke config model-list validation."""
from __future__ import annotations

import os
import io
import unittest
from unittest import mock

from scripts.stage0 import check_smoke_config


class CheckSmokeConfigTest(unittest.TestCase):
    @mock.patch.dict(
        os.environ,
        {
            "TOKENKEY_BASE_URL": "https://api.example.test",
            "TK_SMOKE_API_KEY": "sk-test",
            "TK_SMOKE_ANTHROPIC_MODELS": "claude-a",
            "TK_SMOKE_GEMINI_MODELS": "gemini-a",
            "TK_SMOKE_OPENAI_OAUTH_MODELS": "gpt-a",
        },
        clear=True,
    )
    @mock.patch.object(
        check_smoke_config,
        "_fetch_models",
        return_value=[{"id": "claude-a"}],
    )
    def test_main_via_edge_only_checks_anthropic_models(self, _fetch: mock.Mock) -> None:
        with mock.patch("sys.argv", ["check_smoke_config.py", "--suite", "main-via-edge"]), \
            mock.patch("sys.stdout", new_callable=io.StringIO), \
            mock.patch("sys.stderr", new_callable=io.StringIO):
            self.assertEqual(check_smoke_config.main(), 0)

    @mock.patch.dict(
        os.environ,
        {
            "TOKENKEY_BASE_URL": "https://api.example.test",
            "TK_SMOKE_API_KEY": "sk-test",
            "TK_SMOKE_ANTHROPIC_MODELS": "claude-a",
            "TK_SMOKE_GEMINI_MODELS": "gemini-a",
            "TK_SMOKE_OPENAI_OAUTH_MODELS": "gpt-a",
        },
        clear=True,
    )
    @mock.patch.object(
        check_smoke_config,
        "_fetch_models",
        return_value=[{"id": "claude-a"}],
    )
    def test_full_checks_all_platform_model_lists(self, _fetch: mock.Mock) -> None:
        with mock.patch("sys.argv", ["check_smoke_config.py", "--suite", "full"]), \
            mock.patch("sys.stdout", new_callable=io.StringIO), \
            mock.patch("sys.stderr", new_callable=io.StringIO):
            self.assertEqual(check_smoke_config.main(), 1)


if __name__ == "__main__":
    unittest.main()
