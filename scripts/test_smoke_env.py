#!/usr/bin/env python3
"""Tests for smoke env resolution."""
from __future__ import annotations

import os
import unittest
from unittest import mock

from scripts.stage0 import smoke_env


class SmokeEnvTest(unittest.TestCase):
    def test_smoke_api_key(self) -> None:
        with mock.patch.dict(os.environ, {"TK_SMOKE_API_KEY": "sk-test"}, clear=True):
            self.assertEqual(smoke_env.smoke_api_key(), "sk-test")

    def test_model_lists(self) -> None:
        with mock.patch.dict(
            os.environ,
            {
                "TK_SMOKE_ANTHROPIC_MODELS": "claude-a, claude-b",
                "TK_SMOKE_GEMINI_MODELS": "gemini-a gemini-b",
                "TK_SMOKE_OPENAI_OAUTH_MODELS": "gpt-a",
            },
            clear=True,
        ):
            self.assertEqual(smoke_env.anthropic_models(), ["claude-a", "claude-b"])
            self.assertEqual(smoke_env.gemini_models(), ["gemini-a", "gemini-b"])
            self.assertEqual(smoke_env.openai_oauth_models(), ["gpt-a"])

    def test_edge_defaults_when_unset(self) -> None:
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertEqual(smoke_env.edge_canary_base_url(), smoke_env.DEFAULT_PROD_BASE_URL)
            self.assertEqual(
                smoke_env.edge_local_chat_model(),
                smoke_env.DEFAULT_EDGE_LOCAL_ANTHROPIC_MODEL,
            )

    def test_edge_model_list(self) -> None:
        with mock.patch.dict(os.environ, {"TK_SMOKE_EDGE_LOCAL_CHAT_MODELS": "claude-edge,claude-backup"}, clear=True):
            self.assertEqual(smoke_env.edge_local_chat_models(), ["claude-edge", "claude-backup"])


if __name__ == "__main__":
    unittest.main()
