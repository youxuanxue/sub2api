"""Canonical TK_SMOKE_* resolution (mirrors ops/stage0/smoke_env.sh)."""
from __future__ import annotations

import os

from scripts.stage0.load_smoke_github_env import apply_github_env

DEFAULT_PROD_BASE_URL = "https://api.tokenkey.dev"
DEFAULT_EDGE_LOCAL_ANTHROPIC_MODEL = "claude-sonnet-4-6"


def _ensure_github_env_loaded() -> None:
    env_name = os.environ.get("TK_SMOKE_GITHUB_ENV", "").strip()
    if env_name:
        apply_github_env(env_name)


def _env(name: str) -> str:
    _ensure_github_env_loaded()
    return os.environ.get(name, "").strip()


def prod_anthropic_key() -> str:
    return _env("TK_SMOKE_PROD_ANTHROPIC_KEY")


def prod_anthropic_model() -> str:
    return _env("TK_SMOKE_PROD_ANTHROPIC_MODEL")


def edge_canary_key() -> str:
    return _env("TK_SMOKE_EDGE_CANARY_KEY")


def edge_canary_base_url() -> str:
    return _env("TK_SMOKE_EDGE_CANARY_BASE_URL") or DEFAULT_PROD_BASE_URL


def edge_local_chat_model() -> str:
    return _env("TK_SMOKE_EDGE_LOCAL_CHAT_MODEL") or DEFAULT_EDGE_LOCAL_ANTHROPIC_MODEL
