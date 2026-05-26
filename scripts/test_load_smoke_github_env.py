#!/usr/bin/env python3
"""Tests for GitHub Environment smoke config loader."""
from __future__ import annotations

import os
import unittest
from unittest import mock

from scripts.stage0 import load_smoke_github_env as loader


class LoadSmokeGithubEnvTest(unittest.TestCase):
    def test_required_secrets_prod(self) -> None:
        self.assertEqual(
            loader.required_secrets("prod"),
            loader.PROD_SECRETS,
        )

    def test_required_secrets_edge(self) -> None:
        self.assertEqual(
            loader.required_secrets("edge-uk1"),
            loader.EDGE_SECRETS,
        )

    def test_required_secrets_rejects_unknown(self) -> None:
        with self.assertRaises(ValueError):
            loader.required_secrets("staging")

    @mock.patch.dict(
        os.environ,
        {
            "TK_SMOKE_PROD_ANTHROPIC_KEY": "sk-a",
            "TK_SMOKE_PROD_GEMINI_KEY": "sk-g",
            "TK_SMOKE_PROD_OPENAI_OAUTH_KEY": "sk-o",
        },
        clear=True,
    )
    @mock.patch.object(loader, "fetch_tk_smoke_variables", return_value={"TK_SMOKE_PROD_ANTHROPIC_MODEL": "claude-a"})
    @mock.patch.object(loader, "resolve_repo", return_value="owner/repo")
    def test_load_github_env_with_local_secrets(
        self,
        _repo: mock.Mock,
        _fetch: mock.Mock,
    ) -> None:
        out = loader.load_github_env("prod")
        self.assertEqual(out["TK_SMOKE_PROD_ANTHROPIC_MODEL"], "claude-a")

    @mock.patch.dict(os.environ, {}, clear=True)
    @mock.patch.object(loader, "secret_configured", return_value=True)
    @mock.patch.object(loader, "fetch_tk_smoke_variables", return_value={})
    @mock.patch.object(loader, "resolve_repo", return_value="owner/repo")
    def test_load_github_env_missing_local_secrets(
        self,
        _repo: mock.Mock,
        _fetch: mock.Mock,
        _secret: mock.Mock,
    ) -> None:
        with self.assertRaisesRegex(RuntimeError, "not readable via gh/API"):
            loader.load_github_env("prod")


if __name__ == "__main__":
    unittest.main()
