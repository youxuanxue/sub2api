#!/usr/bin/env python3
"""Validate current catalog policy against its manifest and pricing owners."""

from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys
from typing import Any


REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
SERVICE_DIR = REPO_ROOT / "backend" / "internal" / "service"
MANIFEST = SERVICE_DIR / "tk_served_models.json"
OVERLAY = SERVICE_DIR / "tk_pricing_overlay.json"
ALLOWLIST_GO = SERVICE_DIR / "pricing_catalog_supported_models_tk.go"

SCHEMA_VERSION = 3
ENTRY_FIELDS = {"channel_type", "scopes", "price_owner", "display"}
SCOPE_FIELDS = {"channel_type", "base_url"}
ALLOWED_SCOPES = {
    (45, "https://ark.cn-beijing.volces.com/api/plan/v3"),
    (46, "https://qianfan.baidubce.com"),
    (54, "https://api.xrtoken.net"),
}
MODEL_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")

MODE_FIELDS = {
    "audio_speech": ("input_cost_per_token", "output_cost_per_token"),
    "audio_transcription": ("input_cost_per_token", "output_cost_per_token"),
    "completion": ("input_cost_per_token", "output_cost_per_token"),
    "embedding": ("input_cost_per_token",),
    "image_generation": ("output_cost_per_image",),
    "realtime": ("input_cost_per_token", "output_cost_per_token"),
    "responses": ("input_cost_per_token", "output_cost_per_token"),
    "video_generation": ("output_cost_per_second",),
    "chat": ("input_cost_per_token", "output_cost_per_token"),
}
ALLOWLIST_PLATFORMS = ("anthropic", "openai", "gemini", "antigravity", "grok")


def _is_pos_number(value: Any) -> bool:
    return (
        isinstance(value, (int, float))
        and not isinstance(value, bool)
        and value > 0
    )


def parse_allowlist_maps(go_text: str) -> dict[str, set[str]]:
    out: dict[str, set[str]] = {}
    for platform in ALLOWLIST_PLATFORMS:
        match = re.search(
            rf"servable-allowlist:begin {re.escape(platform)}"
            rf"(.*?)servable-allowlist:end {re.escape(platform)}",
            go_text,
            re.S,
        )
        out[platform] = set(re.findall(r'"([^"]+)"\s*:', match.group(1))) if match else set()
    return out


def _price_errors(label: str, owner: str, overlay_entries: dict[str, Any]) -> list[str]:
    row = overlay_entries.get(owner)
    if not isinstance(row, dict):
        return [f"{label}: price owner {owner!r} is absent from tk_pricing_overlay.json"]
    fields = MODE_FIELDS.get(row.get("mode"))
    if fields is None:
        return [f"{label}: price owner {owner!r} has unsupported mode {row.get('mode')!r}"]
    return [
        f"{label}: price owner {owner!r} requires {field} > 0, got {row.get(field)!r}"
        for field in fields
        if not _is_pos_number(row.get(field))
    ]


def _scope_errors(label: str, scope: Any) -> list[str]:
    if not isinstance(scope, dict):
        return [f"{label} must be an object"]
    if set(scope) != SCOPE_FIELDS:
        return [f"{label} must contain exactly: " + ", ".join(sorted(SCOPE_FIELDS))]
    channel_type = scope.get("channel_type")
    base_url = scope.get("base_url")
    if (
        not isinstance(channel_type, int)
        or isinstance(channel_type, bool)
        or not isinstance(base_url, str)
        or (channel_type, base_url) not in ALLOWED_SCOPES
    ):
        return [f"{label} is not a supported normalized newapi property scope"]
    return []


def evaluate(
    manifest: dict[str, Any],
    overlay: dict[str, Any],
    allowlists: dict[str, set[str]],
) -> list[str]:
    errors: list[str] = []
    if manifest.get("schema_version") != SCHEMA_VERSION:
        errors.append(
            f"manifest schema_version must be {SCHEMA_VERSION}, got {manifest.get('schema_version')!r}"
        )
    if set(manifest) != {"schema_version", "entries"}:
        errors.append("manifest top level must contain exactly schema_version and entries")

    entries = manifest.get("entries")
    if not isinstance(entries, dict) or not entries:
        return errors + ["manifest entries must be a non-empty object"]

    overlay_entries = {key: value for key, value in overlay.items() if not key.startswith("_")}
    aliases = overlay.get("_aliases")
    if not isinstance(aliases, dict):
        return errors + ["tk_pricing_overlay.json _aliases must be an object"]

    for model_id, entry in entries.items():
        label = f"manifest {model_id!r}"
        if not isinstance(model_id, str) or MODEL_ID_RE.fullmatch(model_id) is None:
            errors.append(f"{label}: invalid model id key")
            continue
        if not isinstance(entry, dict):
            errors.append(f"{label}: entry must be an object")
            continue
        unknown = sorted(set(entry) - ENTRY_FIELDS)
        if unknown:
            errors.append(f"{label}: unknown fields: " + ", ".join(unknown))
        if not isinstance(entry.get("display"), bool):
            errors.append(f"{label}: display must be a bool")

        channel_type = entry.get("channel_type")
        if channel_type is not None and (
            not isinstance(channel_type, int)
            or isinstance(channel_type, bool)
            or channel_type <= 0
        ):
            errors.append(f"{label}: channel_type must be a positive integer")

        scopes = entry.get("scopes", [])
        if not isinstance(scopes, list):
            errors.append(f"{label}: scopes must be a list")
            scopes = []
        scope_keys: list[tuple[int, str]] = []
        for index, scope in enumerate(scopes):
            scope_errors = _scope_errors(f"{label} scopes[{index}]", scope)
            errors.extend(scope_errors)
            if not scope_errors:
                scope_keys.append((scope["channel_type"], scope["base_url"]))
        if len(scope_keys) != len(set(scope_keys)):
            errors.append(f"{label}: duplicate property scope")
        if channel_type is None and not scopes:
            errors.append(f"{label}: at least one channel_type or property scope is required")

        price_owner = entry.get("price_owner", model_id)
        if not isinstance(price_owner, str) or not price_owner:
            errors.append(f"{label}: price_owner must be a non-empty string")
            continue
        errors.extend(_price_errors(label, price_owner, overlay_entries))
        if entry.get("display"):
            resolved_owner = model_id if model_id in overlay_entries else aliases.get(model_id)
            if resolved_owner != price_owner:
                errors.append(
                    f"{label}: displayed model must resolve to {price_owner!r} through a direct owner or _aliases; got {resolved_owner!r}"
                )

    for platform, model_ids in allowlists.items():
        for model_id in sorted(model_ids):
            owner = model_id if model_id in overlay_entries else aliases.get(model_id)
            if not isinstance(owner, str):
                errors.append(f"{platform} allowlist {model_id!r}: no direct price owner or _aliases entry")
                continue
            errors.extend(_price_errors(f"{platform} allowlist {model_id!r}", owner, overlay_entries))
    return errors


def cmd_selftest() -> int:
    overlay = {
        "_aliases": {"alias-chat": "good-chat"},
        "good-chat": {"mode": "chat", "input_cost_per_token": 1, "output_cost_per_token": 2},
    }
    valid = {
        "schema_version": SCHEMA_VERSION,
        "entries": {
            "alias-chat": {
                "channel_type": 17,
                "price_owner": "good-chat",
                "display": True,
            },
            "scoped-chat": {
                "scopes": [{"channel_type": 46, "base_url": "https://qianfan.baidubce.com"}],
                "price_owner": "good-chat",
                "display": False,
            },
        },
    }
    assert evaluate(valid, overlay, {platform: set() for platform in ALLOWLIST_PLATFORMS}) == []

    invalid = json.loads(json.dumps(valid))
    invalid["entries"]["alias-chat"]["notes"] = "history"
    invalid["entries"]["scoped-chat"]["scopes"][0]["base_url"] += "/"
    found = evaluate(invalid, overlay, {"openai": {"unpriced"}})
    assert any("unknown fields: notes" in error for error in found)
    assert any("not a supported normalized" in error for error in found)
    assert any("openai allowlist 'unpriced'" in error for error in found)
    print("catalog-serving-drift selftest: PASS")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true")
    parser.add_argument("--selftest", action="store_true")
    args = parser.parse_args()
    if args.selftest:
        return cmd_selftest()
    try:
        manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
        overlay = json.loads(OVERLAY.read_text(encoding="utf-8"))
        allowlists = parse_allowlist_maps(ALLOWLIST_GO.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        print(f"catalog-serving-drift: error: {exc}", file=sys.stderr)
        return 2
    errors = evaluate(manifest, overlay, allowlists)
    if errors:
        for error in errors:
            print(f"catalog-serving-drift: FAIL: {error}", file=sys.stderr)
        return 1
    if not args.quiet:
        print("catalog-serving-drift: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
