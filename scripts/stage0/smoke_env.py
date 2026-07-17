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


def smoke_api_key() -> str:
    return _env("TK_SMOKE_API_KEY")


def anthropic_models() -> list[str]:
    raw = _env("TK_SMOKE_ANTHROPIC_MODELS")
    return _split_models(raw, ["claude-sonnet-4-6"])


def gemini_models() -> list[str]:
    raw = _env("TK_SMOKE_GEMINI_MODELS")
    return _split_models(raw, [])


def openai_oauth_models() -> list[str]:
    raw = _env("TK_SMOKE_OPENAI_OAUTH_MODELS")
    return _split_models(raw, ["gpt-5.4"])


def _split_models(raw: str, default: list[str]) -> list[str]:
    value = raw.strip()
    if not value:
        return default
    return [part for part in value.replace(",", " ").split() if part]


def edge_canary_base_url() -> str:
    return _env("TK_SMOKE_EDGE_CANARY_BASE_URL") or DEFAULT_PROD_BASE_URL


def edge_local_chat_model() -> str:
    models = edge_local_chat_models()
    return models[0] if models else DEFAULT_EDGE_LOCAL_ANTHROPIC_MODEL


def edge_local_chat_models() -> list[str]:
    raw = _env("TK_SMOKE_EDGE_LOCAL_CHAT_MODELS")
    return _split_models(raw, [DEFAULT_EDGE_LOCAL_ANTHROPIC_MODEL])
