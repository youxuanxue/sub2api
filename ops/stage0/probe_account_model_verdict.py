#!/usr/bin/env python3
"""Verdict classification for probe_account_model.sh (unit-testable SSOT)."""

from __future__ import annotations

import json
import base64
import hashlib
import io
from typing import Any


def gemini_response_summary(body_text: str) -> dict[str, Any]:
    """No prompts, answers or image bytes in probe output."""
    result: dict[str, Any] = {"valid": False, "text_parts": 0, "images": []}
    try:
        body = json.loads(body_text)
        candidate = body["candidates"][0]
        if candidate.get("finishReason") != "STOP":
            return result
        for part in candidate["content"]["parts"]:
            if isinstance(part.get("text"), str) and part["text"]:
                result["text_parts"] += 1
            if "inlineData" in part:
                inline = part["inlineData"]
                data = base64.b64decode(inline["data"], validate=True)
                if len(data) < 16 or inline["mimeType"] not in ("image/jpeg", "image/png", "image/webp"):
                    return result
                from PIL import Image
                with Image.open(io.BytesIO(data)) as image:
                    if (Image.MIME.get(image.format) != inline["mimeType"]
                            or image.width * image.height > 32_000_000):
                        return result
                    image.verify()
                with Image.open(io.BytesIO(data)) as image:
                    image.load()
                result["images"].append({"mime_type": inline["mimeType"], "bytes": len(data),
                                         "sha256": hashlib.sha256(data).hexdigest()})
        result["valid"] = bool(result["text_parts"] or result["images"])
    except (ValueError, KeyError, IndexError, TypeError, AttributeError, OSError, ImportError):
        return result
    return result


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
        if endpoint in ("gemini", "gemini_image"):
            summary = gemini_response_summary(body_text)
            if not summary["valid"] or (endpoint == "gemini_image" and not summary["images"]):
                return "uncorrelated_success"
        if endpoint == "transcriptions":
            try:
                transcript = json.loads(body_text)
            except json.JSONDecodeError:
                return "uncorrelated_success"
            if not isinstance(transcript, dict) or not isinstance(transcript.get("text"), str):
                return "uncorrelated_success"
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
