"""mitmproxy addon for sanitized Kiro CLI HTTP and TLS evidence.

The addon emits only allowlisted request identity/protocol metadata, response
status, and parsed ClientHello structure. It never emits credentials, profile
ARNs, user content, raw request/response bodies, or key-share bytes.
"""
from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Any

KIRO_HOSTS = {
    "runtime.us-east-1.kiro.dev",
    "management.us-east-1.kiro.dev",
    "codewhisperer.us-east-1.amazonaws.com",
    "q.us-east-1.amazonaws.com",
}
HEADER_ALLOWLIST = {
    "accept",
    "content-type",
    "host",
    "user-agent",
    "x-amz-target",
    "x-amz-user-agent",
    "x-amzn-codewhisperer-optout",
    "x-amzn-kiro-agent-mode",
}
BODY_KEY_ALLOWLIST = {"fileContext", "maxResults", "origin", "conversationState"}
SECRET_HEADER_NAMES = {"authorization", "cookie", "proxy-authorization", "x-amz-security-token"}


def _u16(data: bytes, offset: int) -> int:
    if offset + 2 > len(data):
        raise ValueError("truncated uint16")
    return int.from_bytes(data[offset : offset + 2], "big")


def _u16_vector(data: bytes) -> list[int]:
    size = _u16(data, 0)
    if size % 2 or size + 2 > len(data):
        raise ValueError("invalid uint16 vector")
    return [_u16(data, pos) for pos in range(2, 2 + size, 2)]


def _u8_vector(data: bytes) -> list[int]:
    if not data:
        raise ValueError("missing uint8 vector length")
    size = data[0]
    if size + 1 > len(data):
        raise ValueError("invalid uint8 vector")
    return list(data[1 : 1 + size])


def _supported_versions(data: bytes) -> list[int]:
    if not data:
        return []
    size = data[0]
    if size % 2 or size + 1 > len(data):
        raise ValueError("invalid supported_versions")
    return [_u16(data, pos) for pos in range(1, 1 + size, 2)]


def _key_share_groups(data: bytes) -> list[int]:
    size = _u16(data, 0)
    end = 2 + size
    if end > len(data):
        raise ValueError("invalid key_share vector")
    groups: list[int] = []
    pos = 2
    while pos < end:
        group = _u16(data, pos)
        key_len = _u16(data, pos + 2)
        pos += 4
        if pos + key_len > end:
            raise ValueError("truncated key_share")
        groups.append(group)
        pos += key_len
    return groups


def _alpn(data: bytes) -> list[str]:
    size = _u16(data, 0)
    end = 2 + size
    if end > len(data):
        raise ValueError("invalid ALPN vector")
    values: list[str] = []
    pos = 2
    while pos < end:
        item_len = data[pos]
        pos += 1
        if pos + item_len > end:
            raise ValueError("truncated ALPN item")
        values.append(data[pos : pos + item_len].decode("ascii", "strict"))
        pos += item_len
    return values


def parse_client_hello(client_hello: Any) -> dict[str, Any]:
    """Project mitmproxy ClientHello onto non-secret fingerprint fields."""
    parsed: dict[str, Any] = {
        "server_name": str(client_hello.sni or ""),
        "version": 771,
        "cipher_suites": [int(value) for value in client_hello.cipher_suites],
        "extensions": [],
        "curves": [],
        "point_formats": [],
        "signature_algorithms": [],
        "alpn_protocols": [],
        "supported_versions": [],
        "key_share_groups": [],
        "psk_modes": [],
    }
    for ext_type, raw in client_hello.extensions:
        ext_type = int(ext_type)
        raw = bytes(raw)
        parsed["extensions"].append(ext_type)
        if ext_type == 10:
            parsed["curves"] = _u16_vector(raw)
        elif ext_type == 11:
            parsed["point_formats"] = _u8_vector(raw)
        elif ext_type == 13:
            parsed["signature_algorithms"] = _u16_vector(raw)
        elif ext_type == 16:
            parsed["alpn_protocols"] = _alpn(raw)
        elif ext_type == 43:
            parsed["supported_versions"] = _supported_versions(raw)
        elif ext_type == 45:
            parsed["psk_modes"] = _u8_vector(raw)
        elif ext_type == 51:
            parsed["key_share_groups"] = _key_share_groups(raw)
    return parsed


def _safe_body_metadata(body_text: str) -> dict[str, Any]:
    result: dict[str, Any] = {
        "body_keys": [],
        "origin": "",
        "model_id": "",
        "chat_trigger_type": "",
    }
    try:
        body = json.loads((body_text or "").strip())
    except (json.JSONDecodeError, ValueError):
        return result
    if not isinstance(body, dict):
        return result
    result["body_keys"] = sorted(set(body) & BODY_KEY_ALLOWLIST)
    origin = body.get("origin")
    if isinstance(origin, str):
        result["origin"] = origin
    conversation = body.get("conversationState")
    if isinstance(conversation, dict):
        trigger = conversation.get("chatTriggerType")
        if isinstance(trigger, str):
            result["chat_trigger_type"] = trigger
        message = conversation.get("currentMessage")
        if isinstance(message, dict):
            user_input = message.get("userInputMessage")
            if isinstance(user_input, dict):
                nested_origin = user_input.get("origin")
                model_id = user_input.get("modelId")
                if isinstance(nested_origin, str):
                    result["origin"] = nested_origin
                if isinstance(model_id, str):
                    result["model_id"] = model_id
    return result


def build_http_record(flow: Any) -> dict[str, Any] | None:
    host = str(flow.request.host or "")
    if host not in KIRO_HOSTS or getattr(flow, "response", None) is None:
        return None
    path = str(flow.request.path or "").split("?", 1)[0]
    headers: dict[str, str] = {}
    for key in flow.request.headers:
        lower = str(key).lower()
        if lower in HEADER_ALLOWLIST:
            headers[lower] = str(flow.request.headers.get(key, ""))
    status = int(flow.response.status_code)
    return {
        "host": host,
        "path": path,
        "method": str(flow.request.method),
        "headers": headers,
        **_safe_body_metadata(flow.request.get_text(strict=False) or ""),
        "status_code": status,
        "success": 200 <= status < 300,
    }


def _append_jsonl(env_name: str, record: dict[str, Any]) -> None:
    target = os.environ.get(env_name, "").strip()
    if not target:
        return
    path = Path(target)
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as handle:
        handle.write(json.dumps(record, ensure_ascii=False, sort_keys=True) + "\n")


def tls_clienthello(data: Any) -> None:
    hello = data.client_hello
    if str(hello.sni or "") not in KIRO_HOSTS:
        return
    _append_jsonl("KIRO_CAPTURE_TLS_LOG", parse_client_hello(hello))


def response(flow: Any) -> None:
    record = build_http_record(flow)
    if record is not None:
        _append_jsonl("KIRO_CAPTURE_HTTP_LOG", record)
