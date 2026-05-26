#!/usr/bin/env python3
"""Tests for smoke env resolution."""
from __future__ import annotations

import os
import unittest
from unittest import mock

from scripts.stage0 import smoke_env


class SmokeEnvTest(unittest.TestCase):
    def test_prod_key(self) -> None:
        with mock.patch.dict(os.environ, {"TK_SMOKE_PROD_ANTHROPIC_KEY": "sk-test"}, clear=True):
            self.assertEqual(smoke_env.prod_anthropic_key(), "sk-test")

    def test_edge_canary_key(self) -> None:
        with mock.patch.dict(os.environ, {"TK_SMOKE_EDGE_CANARY_KEY": "sk-canary"}, clear=True):
            self.assertEqual(smoke_env.edge_canary_key(), "sk-canary")

    def test_anthropic_model(self) -> None:
        with mock.patch.dict(os.environ, {"TK_SMOKE_PROD_ANTHROPIC_MODEL": "claude-a"}, clear=True):
            self.assertEqual(smoke_env.prod_anthropic_model(), "claude-a")

    def test_edge_defaults_when_unset(self) -> None:
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertEqual(smoke_env.edge_canary_base_url(), smoke_env.DEFAULT_PROD_BASE_URL)
            self.assertEqual(
                smoke_env.edge_local_chat_model(),
                smoke_env.DEFAULT_EDGE_LOCAL_ANTHROPIC_MODEL,
            )


if __name__ == "__main__":
    unittest.main()
