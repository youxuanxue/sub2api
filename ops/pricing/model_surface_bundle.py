#!/usr/bin/env python3
"""Validate the generated TokenKey model-surface release artifact."""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_BUNDLE_PATH = REPO_ROOT / "ops" / "pricing" / "model-surface-bundle.json"
SCHEMA_VERSION = 3
SUPPORTED_SCHEMA_VERSIONS = {1, SCHEMA_VERSION}
BUNDLE_FIELDS = {
    "schema_version",
    "floor_sha256",
    "account_model_mapping",
}
BASE_FLOOR_FIELDS = {
    "platforms",
    "newapi_channel_types",
    "antigravity_group_scopes",
    "forbidden_model_mapping_keys",
    "forbidden_model_mapping_prefixes",
}
ACCOUNT_OVERRIDE_FIELDS = {"platform", "channel_type", "base_url", "model_mapping"}


def canonical_json(value: Any) -> str:
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":"))


def floor_sha256(floor: dict[str, Any]) -> str:
    return hashlib.sha256(canonical_json(floor).encode("utf-8")).hexdigest()


def _validate_mapping(label: str, raw: Any) -> dict[str, str]:
    if not isinstance(raw, dict) or not raw:
        raise RuntimeError(f"{label} must be a non-empty object")
    for key, value in raw.items():
        if not isinstance(key, str) or not key.strip():
            raise RuntimeError(f"{label} contains an empty or non-string key")
        if not isinstance(value, str) or not value.strip():
            raise RuntimeError(f"{label}.{key} has an empty or non-string target")
        if key != key.strip() or value != value.strip():
            raise RuntimeError(f"{label}.{key} contains surrounding whitespace")
    return raw


def _validate_policy_map(label: str, raw: Any) -> dict[str, list[str]]:
    if raw is None:
        return {}
    if not isinstance(raw, dict):
        raise RuntimeError(f"{label} must be an object")
    for scope, values in raw.items():
        if not isinstance(scope, str) or not scope.strip() or scope != scope.strip():
            raise RuntimeError(f"{label} contains an invalid scope")
        if not isinstance(values, list):
            raise RuntimeError(f"{label}.{scope} must be an array")
        normalized: list[str] = []
        for value in values:
            if not isinstance(value, str) or not value.strip() or value != value.strip():
                raise RuntimeError(f"{label}.{scope} contains an invalid value")
            normalized.append(value)
        if len(normalized) != len(set(normalized)):
            raise RuntimeError(f"{label}.{scope} contains duplicate values")
    return raw


def normalize_account_override_base_url(raw: Any) -> str:
    if not isinstance(raw, str):
        return ""
    return raw.strip().lower().rstrip("/")


def account_override_scope(override: dict[str, Any]) -> str:
    platform = str(override.get("platform") or "").strip().lower()
    channel_type = override.get("channel_type")
    channel = str(channel_type) if channel_type is not None else "0"
    base_url = normalize_account_override_base_url(override.get("base_url"))
    return f"account_override:{platform}:{channel}:{base_url}"


def _validate_account_overrides(raw: Any) -> list[dict[str, Any]]:
    if not isinstance(raw, list):
        raise RuntimeError("model surface bundle omitted account_model_mapping.account_overrides array")
    seen: set[str] = set()
    for index, override in enumerate(raw):
        label = f"account_model_mapping.account_overrides[{index}]"
        if not isinstance(override, dict):
            raise RuntimeError(f"{label} must be an object")
        missing = sorted({"platform", "base_url", "model_mapping"} - set(override))
        if missing:
            raise RuntimeError(f"{label} omitted fields: " + ", ".join(missing))
        unknown = sorted(set(override) - ACCOUNT_OVERRIDE_FIELDS)
        if unknown:
            raise RuntimeError(f"{label} has unknown fields: " + ", ".join(unknown))
        platform = override.get("platform")
        if not isinstance(platform, str) or not platform.strip() or platform != platform.strip().lower():
            raise RuntimeError(f"{label}.platform must be a normalized non-empty string")
        base_url = override.get("base_url")
        if (
            not isinstance(base_url, str)
            or not base_url.strip()
            or base_url != normalize_account_override_base_url(base_url)
        ):
            raise RuntimeError(f"{label}.base_url must be a normalized non-empty string")
        channel_type = override.get("channel_type")
        if platform == "newapi":
            if not isinstance(channel_type, int) or isinstance(channel_type, bool) or channel_type <= 0:
                raise RuntimeError(f"{label}.channel_type must be a positive integer for newapi")
        elif channel_type is not None:
            raise RuntimeError(f"{label}.channel_type is only valid for newapi")
        _validate_mapping(f"{label}.model_mapping", override.get("model_mapping"))
        scope = account_override_scope(override)
        if scope in seen:
            raise RuntimeError(f"{label} duplicates selector {scope!r}")
        seen.add(scope)
    return raw


def _validate_floor(floor: dict[str, Any], schema_version: int) -> None:
    floor_fields = set(BASE_FLOOR_FIELDS)
    if schema_version >= 2:
        floor_fields.add("account_overrides")
    missing = sorted(floor_fields - set(floor))
    if missing:
        raise RuntimeError("model surface bundle omitted account_model_mapping fields: " + ", ".join(missing))
    unknown = sorted(set(floor) - floor_fields)
    if unknown:
        raise RuntimeError("model surface bundle has unknown account_model_mapping fields: " + ", ".join(unknown))
    platforms = floor.get("platforms")
    if not isinstance(platforms, dict) or not platforms:
        raise RuntimeError("model surface bundle account_model_mapping.platforms must be non-empty")
    for scope, mapping in platforms.items():
        if not isinstance(scope, str) or not scope.strip() or scope != scope.strip():
            raise RuntimeError("model surface bundle platforms contains an invalid scope")
        _validate_mapping(f"account_model_mapping.platforms.{scope}", mapping)

    channel_types = floor.get("newapi_channel_types")
    if not isinstance(channel_types, dict):
        raise RuntimeError("model surface bundle omitted account_model_mapping.newapi_channel_types")
    for channel_type, mapping in channel_types.items():
        if not isinstance(channel_type, str) or not channel_type.isdigit() or int(channel_type) <= 0:
            raise RuntimeError(f"account_model_mapping.newapi_channel_types has invalid key {channel_type!r}")
        _validate_mapping(f"account_model_mapping.newapi_channel_types.{channel_type}", mapping)

    account_overrides = (
        _validate_account_overrides(floor.get("account_overrides"))
        if schema_version >= 2 else {}
    )

    scopes = floor.get("antigravity_group_scopes")
    if not isinstance(scopes, list) or not scopes:
        raise RuntimeError("model surface bundle omitted account_model_mapping.antigravity_group_scopes")
    if any(not isinstance(scope, str) or not scope.strip() or scope != scope.strip() for scope in scopes):
        raise RuntimeError("account_model_mapping.antigravity_group_scopes contains an invalid scope")
    if len(scopes) != len(set(scopes)):
        raise RuntimeError("account_model_mapping.antigravity_group_scopes contains duplicates")

    forbidden_keys = _validate_policy_map(
        "account_model_mapping.forbidden_model_mapping_keys",
        floor.get("forbidden_model_mapping_keys"),
    )
    forbidden_prefixes = _validate_policy_map(
        "account_model_mapping.forbidden_model_mapping_prefixes",
        floor.get("forbidden_model_mapping_prefixes"),
    )
    for scope, mapping in platforms.items():
        blocked = set(forbidden_keys.get(scope) or [])
        prefixes = forbidden_prefixes.get(scope) or []
        conflicts = sorted(
            key for key in mapping
            if key in blocked or any(key.startswith(prefix) for prefix in prefixes)
        )
        if conflicts:
            raise RuntimeError(
                f"account_model_mapping.platforms.{scope} requires forbidden keys: "
                + ", ".join(conflicts)
            )
    blocked = set(forbidden_keys.get("newapi") or [])
    prefixes = forbidden_prefixes.get("newapi") or []
    for channel_type, mapping in channel_types.items():
        conflicts = sorted(
            key for key in mapping
            if key in blocked or any(key.startswith(prefix) for prefix in prefixes)
        )
        if conflicts:
            raise RuntimeError(
                f"account_model_mapping.newapi_channel_types.{channel_type} requires forbidden keys: "
                + ", ".join(conflicts)
            )
    for override in account_overrides:
        scope = override["platform"]
        mapping = override["model_mapping"]
        blocked = set(forbidden_keys.get(scope) or [])
        prefixes = forbidden_prefixes.get(scope) or []
        conflicts = sorted(
            key for key in mapping
            if key in blocked or any(key.startswith(prefix) for prefix in prefixes)
        )
        if conflicts:
            raise RuntimeError(
                f"account_model_mapping.account_overrides selector {account_override_scope(override)!r} "
                "model_mapping requires forbidden keys: "
                + ", ".join(conflicts)
            )


def load_bundle(path: Path) -> dict[str, Any]:
    path = path.expanduser().resolve()
    try:
        bundle = json.loads(path.read_text(encoding="utf-8"))
    except OSError as e:
        raise RuntimeError(f"cannot read model surface bundle {path}: {e}") from e
    except json.JSONDecodeError as e:
        raise RuntimeError(f"invalid model surface bundle JSON {path}: {e}") from e
    if not isinstance(bundle, dict):
        raise RuntimeError("model surface bundle must be a JSON object")
    missing = sorted(BUNDLE_FIELDS - set(bundle))
    if missing:
        raise RuntimeError("model surface bundle omitted fields: " + ", ".join(missing))
    unknown = sorted(set(bundle) - BUNDLE_FIELDS)
    if unknown:
        raise RuntimeError("model surface bundle has unknown fields: " + ", ".join(unknown))
    schema_version = bundle.get("schema_version")
    if (
        not isinstance(schema_version, int)
        or isinstance(schema_version, bool)
        or schema_version not in SUPPORTED_SCHEMA_VERSIONS
    ):
        raise RuntimeError(
            f"unsupported model surface bundle schema {schema_version!r}; "
            f"supported {sorted(SUPPORTED_SCHEMA_VERSIONS)}"
        )
    floor = bundle.get("account_model_mapping")
    if not isinstance(floor, dict):
        raise RuntimeError("model surface bundle omitted account_model_mapping")
    got_digest = floor_sha256(floor)
    if bundle.get("floor_sha256") != got_digest:
        raise RuntimeError(
            "model surface bundle floor_sha256 mismatch: "
            f"got {bundle.get('floor_sha256')!r}, computed {got_digest}"
        )
    _validate_floor(floor, schema_version)
    return bundle
