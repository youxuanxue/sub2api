#!/usr/bin/env python3
"""Verdict classification for probe_account_model.sh (unit-testable SSOT)."""

from __future__ import annotations

import json
import base64
import hashlib
import io
import re
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


def _compat_sse_document(endpoint: str, body_text: str) -> dict[str, Any] | None:
    # The replay verifier owns SSE framing (including multiline data and CRLF)
    # and explicit error-event detection; this projection owns protocol fields.
    from prod_replay import sse_events, event_failed

    texts: dict[tuple[int, int], str] = {}
    terminal = False
    saw_done = False
    stopped = False
    completed = None
    for name, raw in sse_events(body_text.encode("utf-8")):
        if event_failed(name, None):
            return None
        if not raw:
            continue
        if terminal:
            # Our Responses adapter emits one optional DONE after completed.
            if endpoint == "responses" and raw == "[DONE]" and not saw_done:
                saw_done = True
                continue
            return None
        if raw == "[DONE]":
            if endpoint != "chat" or not stopped:
                return None
            terminal = True
            saw_done = True
            continue
        event = json.loads(raw)
        if not isinstance(event, dict) or event_failed(name, event):
            return None
        kind = event.get("type", name)
        if endpoint == "chat":
            for choice in event.get("choices", []):
                if choice.get("index", 0) != 0:
                    return None
                key = (0, 0)
                text = choice.get("delta", {}).get("content")
                if text is not None:
                    texts[key] = texts.get(key, "") + text
                reason = choice.get("finish_reason")
                if reason is not None:
                    if reason != "stop":
                        return None
                    stopped = True
        elif endpoint == "messages":
            key = (int(event.get("index", 0)), 0)
            if kind == "content_block_start" and event.get("content_block", {}).get("type") == "text":
                texts[key] = event["content_block"].get("text", "")
            elif kind == "content_block_delta" and event.get("delta", {}).get("type") == "text_delta":
                texts[key] = texts.get(key, "") + event["delta"]["text"]
            elif kind == "message_delta" and event.get("delta", {}).get("stop_reason") is not None:
                if event["delta"]["stop_reason"] != "end_turn":
                    return None
                stopped = True
            elif kind == "message_stop":
                terminal = stopped
                if not terminal:
                    return None
        elif endpoint == "responses":
            key = (int(event.get("output_index", 0)), int(event.get("content_index", 0)))
            if kind == "response.output_text.delta":
                texts[key] = texts.get(key, "") + event["delta"]
            elif kind == "response.output_text.done":
                texts.setdefault(key, event["text"])
            elif kind == "response.completed":
                completed = event["response"]
                if completed.get("status") != "completed" or event_failed("", completed):
                    return None
                terminal = True
        else:
            return None
    if not terminal:
        return None
    text_parts = [texts[key] for key in sorted(texts)]
    if endpoint == "chat":
        return {"choices": [{"finish_reason": "stop", "message": {"content": "".join(text_parts)}}]}
    if endpoint == "messages":
        return {"stop_reason": "end_turn", "content": [{"type": "text", "text": text} for text in text_parts]}
    # Completed Responses snapshots repeat streamed text; use exactly one copy.
    if not texts:
        return completed
    return {"status": "completed", "output": [{"type": "message", "content": [
        {"type": "output_text", "text": text} for text in text_parts]}]}


def compat_image_response_summary(endpoint: str, body_text: str) -> dict[str, Any]:
    """Project legal compatibility text fields into the native image validator."""
    invalid = {"valid": False, "text_parts": 0, "images": []}
    try:
        try:
            body = json.loads(body_text)
        except json.JSONDecodeError:
            body = _compat_sse_document(endpoint, body_text)
        if endpoint == "messages":
            if body.get("stop_reason") != "end_turn":
                return invalid
            texts = [part["text"] for part in body["content"] if part.get("type") == "text"]
        elif endpoint == "chat":
            choice = body["choices"][0]
            if choice.get("finish_reason") != "stop":
                return invalid
            texts = [choice["message"]["content"]]
        elif endpoint == "responses":
            if body.get("status") != "completed":
                return invalid
            texts = [part["text"] for item in body["output"] if item.get("type") == "message"
                     for part in item["content"] if part.get("type") == "output_text"]
        else:
            return invalid
        parts = []
        for text in texts:
            if not isinstance(text, str):
                return invalid
            parts.append({"text": text})
            for mime, data in re.findall(r"!\[[^\]]*\]\(data:([^;,()]+);base64,([^)]*)\)", text):
                parts.append({"inlineData": {"mimeType": mime, "data": data}})
        result = gemini_response_summary(json.dumps({"candidates": [{
            "finishReason": "STOP", "content": {"parts": parts}}]}))
        result["valid"] = result["valid"] and bool(result["images"])
        return result
    except (ValueError, KeyError, IndexError, TypeError, AttributeError):
        return invalid


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
    expect_image_output: bool = False,
) -> str:
    if not http_code or http_code == "000":
        if curl_err:
            return "setup_error"
        return "gateway_rejected"

    status = int(http_code)
    low = body_text.lower()

    if 200 <= status < 300:
        if expect_image_output and endpoint in ("messages", "chat", "responses"):
            if not compat_image_response_summary(endpoint, body_text)["valid"]:
                return "uncorrelated_success"
        if endpoint in ("gemini", "gemini_image"):
            summary = gemini_response_summary(body_text)
            if not summary["valid"] or ((endpoint == "gemini_image" or expect_image_output) and not summary["images"]):
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
