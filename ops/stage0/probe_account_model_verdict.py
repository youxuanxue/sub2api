#!/usr/bin/env python3
"""Verdict classification for probe_account_model.sh (unit-testable SSOT)."""

from __future__ import annotations

import json
from typing import Any


def embedding_response_valid(body_text: str) -> bool:
    try:
        parsed = json.loads(body_text)
    except json.JSONDecodeError:
        return False
    data = parsed.get("data")
    return (
        isinstance(data, list)
        and bool(data)
        and isinstance(data[0], dict)
        and "embedding" in data[0]
    )


def classify_probe_verdict(
    *,
    endpoint: str,
    http_code: str,
    body_text: str,
    target_account_id: int,
    usage_row: dict[str, Any] | None,
    curl_err: str,
) -> str:
    if not http_code or http_code == "000":
        if curl_err:
            return "setup_error"
        return "gateway_rejected"

    status = int(http_code)
    low = body_text.lower()

    if 200 <= status < 300:
        if endpoint == "count_tokens":
            return "servable"
        if endpoint == "embeddings":
            if not embedding_response_valid(body_text):
                return "uncorrelated_success"
            usage_account_id = int((usage_row or {}).get("account_id") or 0)
            if usage_row and usage_account_id == target_account_id:
                return "servable"
            if usage_row:
                return "wrong_account"
            return "servable"
        usage_account_id = int((usage_row or {}).get("account_id") or 0)
        if usage_row and usage_account_id == target_account_id:
            return "servable"
        if usage_row:
            return "wrong_account"
        return "uncorrelated_success"

    if status in (401, 403):
        return "upstream_rejected"
    if status == 429 and "no available accounts" in low:
        return "gateway_rejected"
    if status in (400, 404) and any(
        token in low
        for token in (
            "invalid model",
            "model_not_found",
            "not supported",
            "does not exist",
            "not a valid",
        )
    ):
        return "upstream_rejected"
    if status >= 500:
        return "gateway_rejected"
    return "gateway_rejected"
