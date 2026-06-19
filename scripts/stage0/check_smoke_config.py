#!/usr/bin/env python3
"""Preflight helper: verify the configured smoke models are listed for their keys.

Checks TK_SMOKE_PROD_ANTHROPIC_MODEL (always, against the anthropic key).
A model that is configured but not listed is a drift signal and fails the gate.
"""
from __future__ import annotations

import json
import sys
import urllib.error
import urllib.request
from pathlib import Path

# Allow `python3 scripts/stage0/check_smoke_config.py` from repo root.
sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from scripts.stage0.smoke_env import (
    prod_anthropic_key,
    prod_anthropic_model,
)
from scripts.stage0.smoke_suite import pick_model


def _check_model(base: str, label: str, api_key: str, override: str) -> int:
    """Return 0 if `override` is listed for `api_key`, non-zero on drift/error."""
    req = urllib.request.Request(
        f"{base}/v1/models",
        headers={"Authorization": f"Bearer {api_key}", "Accept": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            payload = json.loads(resp.read().decode())
    except urllib.error.HTTPError as exc:
        print(f"check_smoke_config[{label}]: GET /v1/models -> HTTP {exc.code}", file=sys.stderr)
        return 1

    models = payload.get("data") or []
    try:
        model, warn = pick_model(models, override)
    except ValueError as exc:
        print(f"check_smoke_config[{label}]: {exc}", file=sys.stderr)
        return 1

    if warn:
        print(f"::warning::check_smoke_config[{label}]: {warn}", file=sys.stderr)
        print("available models:", file=sys.stderr)
        for m in models:
            print(f"  - {m.get('id')}", file=sys.stderr)
        return 1

    print(f"check_smoke_config[{label}]: OK model={model}")
    return 0


def main() -> int:
    import os

    base = (os.environ.get("TOKENKEY_BASE_URL") or os.environ.get("TK_GATEWAY_URL") or "").rstrip("/")

    anthropic_override = prod_anthropic_model()
    if not anthropic_override:
        print("check_smoke_config: TK_SMOKE_PROD_ANTHROPIC_MODEL unset — skip")
        return 0
    anthropic_key = prod_anthropic_key()
    if not base or not anthropic_key:
        print(
            "check_smoke_config: set TOKENKEY_BASE_URL and TK_SMOKE_PROD_ANTHROPIC_KEY",
            file=sys.stderr,
        )
        return 1

    rc = _check_model(base, "anthropic", anthropic_key, anthropic_override)

    return rc


if __name__ == "__main__":
    raise SystemExit(main())
