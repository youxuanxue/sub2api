#!/usr/bin/env python3
"""Preflight helper: verify configured smoke model lists for the smoke key.

Checks TK_SMOKE_ANTHROPIC_MODELS, TK_SMOKE_GEMINI_MODELS, and
TK_SMOKE_OPENAI_OAUTH_MODELS against the single TK_SMOKE_API_KEY /v1/models
view. Gemini is opt-in because the native Google One pool was retired on
2026-07-04; configured Gemini models must appear in the list. Anthropic and OpenAI OAuth may
use empty model_mapping passthrough on prod (ids absent from /v1/models); those
are warnings only — post_deploy_smoke defers to /v1/messages and openai oauth
chat probes respectively.
"""
from __future__ import annotations

import json
import sys
import urllib.error
import urllib.request
import argparse
from pathlib import Path

# Allow `python3 scripts/stage0/check_smoke_config.py` from repo root.
sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from scripts.stage0.smoke_env import (
    anthropic_models,
    gemini_models,
    openai_oauth_models,
    smoke_api_key,
)

# Platforms whose prod accounts may passthrough upstream model ids with empty
# model_mapping — same defer semantics as ops/stage0/smoke_lib.sh warn helpers.
_DEFER_MODEL_LIST_LABELS = frozenset({"anthropic", "openai_oauth"})


def _fetch_models(base: str, api_key: str) -> list[dict] | None:
    req = urllib.request.Request(
        f"{base}/v1/models",
        headers={"Authorization": f"Bearer {api_key}", "Accept": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            payload = json.loads(resp.read().decode())
    except urllib.error.HTTPError as exc:
        print(f"check_smoke_config: GET /v1/models -> HTTP {exc.code}", file=sys.stderr)
        return None

    models = payload.get("data") or []
    if not isinstance(models, list):
        print("check_smoke_config: /v1/models data is not a list", file=sys.stderr)
        return None
    return models


def _check_models(label: str, available_ids: set[str], configured: list[str]) -> int:
    rc = 0
    for model in configured:
        if model in available_ids:
            print(f"check_smoke_config[{label}]: OK model={model}")
            continue
        if label in _DEFER_MODEL_LIST_LABELS:
            print(
                f"::warning::check_smoke_config[{label}]: configured model {model!r} "
                "not listed — empty model_mapping passthrough; defer to smoke probe",
                file=sys.stderr,
            )
            continue
        print(f"::error::check_smoke_config[{label}]: configured model {model!r} not listed", file=sys.stderr)
        rc = 1
    return rc


def main() -> int:
    import os

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--suite",
        default=os.environ.get("GATEWAY_SMOKE_SUITE", "full"),
        choices=("full", "prod", "main-via-edge", "quick"),
        help="Smoke suite to validate model lists for.",
    )
    args = parser.parse_args()

    base = (os.environ.get("TOKENKEY_BASE_URL") or os.environ.get("TK_GATEWAY_URL") or "").rstrip("/")

    api_key = smoke_api_key()
    if not base or not api_key:
        print(
            "check_smoke_config: set TOKENKEY_BASE_URL and TK_SMOKE_API_KEY",
            file=sys.stderr,
        )
        return 1

    models = _fetch_models(base, api_key)
    if models is None:
        return 1

    available_ids = {str(m.get("id") or "") for m in models if m.get("id")}
    if not available_ids:
        print("check_smoke_config: /v1/models returned no ids", file=sys.stderr)
        return 1

    rc = 0
    rc |= _check_models("anthropic", available_ids, anthropic_models())
    if args.suite in {"full", "prod"}:
        rc |= _check_models("gemini", available_ids, gemini_models())
        rc |= _check_models("openai_oauth", available_ids, openai_oauth_models())

    return rc


if __name__ == "__main__":
    raise SystemExit(main())
