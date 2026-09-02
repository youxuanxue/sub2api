"""Pure document handling for account model_mapping runtime replacements."""

from __future__ import annotations

import base64
import gzip
import json
from typing import Callable


class RuntimeDocumentError(ValueError):
    pass


def _normalize_mapping(label: str, mapping) -> dict[str, str]:
    if not isinstance(mapping, dict) or not mapping:
        raise RuntimeDocumentError(f"{label}: model_mapping must be a non-empty object")
    out: dict[str, str] = {}
    for key_raw, value_raw in mapping.items():
        if not isinstance(key_raw, str) or not isinstance(value_raw, str):
            raise RuntimeDocumentError(f"{label}: keys and values must be strings")
        key = key_raw.strip()
        value = value_raw.strip()
        if not key or not value:
            raise RuntimeDocumentError(f"{label}: empty key/value is not allowed")
        out[key] = value
    return dict(sorted(out.items()))


def normalize_platform_key(platform: str) -> str:
    key = platform.strip().lower()
    if key == "claude":
        return "anthropic"
    if key == "xai":
        return "grok"
    return key


def normalize(doc) -> dict:
    if not isinstance(doc, dict):
        raise RuntimeDocumentError("runtime document must be a JSON object")
    out: dict[str, dict] = {}
    platforms = doc.get("platforms", {})
    if platforms is None:
        platforms = {}
    if not isinstance(platforms, dict):
        raise RuntimeDocumentError("platforms must be an object")
    clean_platforms: dict[str, dict[str, str]] = {}
    for platform, mapping in platforms.items():
        if not isinstance(platform, str) or not platform.strip():
            raise RuntimeDocumentError("platforms contains an empty platform key")
        key = normalize_platform_key(platform)
        if key in clean_platforms:
            raise RuntimeDocumentError(
                f"platforms.{platform}: duplicate normalized platform key {key!r}"
            )
        clean_platforms[key] = _normalize_mapping(f"platforms.{platform}", mapping)
    if clean_platforms:
        out["platforms"] = dict(sorted(clean_platforms.items()))

    channel_types = doc.get("newapi_channel_types", {})
    if channel_types is None:
        channel_types = {}
    if not isinstance(channel_types, dict):
        raise RuntimeDocumentError("newapi_channel_types must be an object")
    clean_channel_types: dict[str, dict[str, str]] = {}
    for channel_type_raw, mapping in channel_types.items():
        key = str(channel_type_raw).strip()
        if not key.isdigit() or int(key) <= 0:
            raise RuntimeDocumentError(f"invalid newapi channel_type {channel_type_raw!r}")
        clean_channel_types[key] = _normalize_mapping(
            f"newapi_channel_types.{key}", mapping
        )
    if clean_channel_types:
        out["newapi_channel_types"] = dict(
            sorted(clean_channel_types.items(), key=lambda item: int(item[0]))
        )

    if not out:
        raise RuntimeDocumentError("runtime document has no replacement scopes")
    return out


def load(path, *, normalize_doc: Callable[[object], dict] = normalize) -> dict:
    return normalize_doc(json.loads(path.read_text()))


def decode_runtime_value(encoded: str) -> dict:
    encoded = encoded.strip()
    if not encoded:
        return {}
    raw = gzip.decompress(base64.b64decode(encoded)).decode("utf-8").strip()
    return json.loads(raw) if raw else {}
