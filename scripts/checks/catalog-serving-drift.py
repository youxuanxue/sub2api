#!/usr/bin/env python3
"""Validate current catalog policy against its manifest and pricing owners."""

from __future__ import annotations

import argparse
import importlib.util
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
# The Go owner retains the ``servable-allowlist:begin`` marker blocks parsed below.

PRICING_DIR = REPO_ROOT / "ops" / "pricing"
if str(PRICING_DIR) not in sys.path:
    sys.path.insert(0, str(PRICING_DIR))
from pricing_registry import MODE_FIELDS, has_complete_price, resolve_price_owner
from servable_allowlist import ALLOWLIST_PLATFORMS, parse_allowlist_maps

_manifest_spec = importlib.util.spec_from_file_location(
    "tk_served_models_manifest",
    REPO_ROOT / "ops" / "pricing" / "served_models_manifest.py",
)
_MANIFEST = importlib.util.module_from_spec(_manifest_spec)
_manifest_spec.loader.exec_module(_MANIFEST)

def _price_errors(label: str, owner: str, overlay_entries: dict[str, Any]) -> list[str]:
    row = overlay_entries.get(owner)
    if not isinstance(row, dict):
        return [f"{label}: price owner {owner!r} is absent from tk_pricing_overlay.json"]
    mode = row.get("mode")
    alternatives = MODE_FIELDS.get(mode)
    if alternatives is None:
        return [f"{label}: price owner {owner!r} has unsupported mode {row.get('mode')!r}"]
    if has_complete_price(row):
        return []
    shapes = " or ".join("+".join(fields) for fields in alternatives)
    return [f"{label}: price owner {owner!r} requires one complete positive shape ({shapes})"]


def evaluate(
    manifest: dict[str, Any],
    overlay: dict[str, Any],
    allowlists: dict[str, set[str]],
) -> list[str]:
    errors: list[str] = []
    try:
        entries = _MANIFEST.parse_manifest_document(manifest).entries
    except _MANIFEST.ManifestError as exc:
        return list(exc.errors)

    overlay_entries = {key: value for key, value in overlay.items() if not key.startswith("_")}
    aliases = overlay.get("_aliases")
    if not isinstance(aliases, dict):
        return errors + ["tk_pricing_overlay.json _aliases must be an object"]

    for entry in entries:
        model_id = entry.model_id
        label = f"manifest {model_id!r}"
        price_owner = resolve_price_owner(entry.price_owner, overlay)
        errors.extend(_price_errors(label, price_owner, overlay_entries))
        if entry.display:
            resolved_owner = resolve_price_owner(model_id, overlay)
            if resolved_owner != price_owner:
                errors.append(
                    f"{label}: displayed model must resolve to {price_owner!r} through a direct owner or _aliases; got {resolved_owner!r}"
                )

    for platform, model_ids in allowlists.items():
        for model_id in sorted(model_ids):
            owner = resolve_price_owner(model_id, overlay)
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
        "schema_version": _MANIFEST.SCHEMA_VERSION,
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
    found = evaluate(invalid, overlay, {platform: set() for platform in ALLOWLIST_PLATFORMS})
    assert any("unknown fields: notes" in error for error in found)
    assert any("not a supported normalized" in error for error in found)
    found = evaluate(valid, overlay, {"openai": {"unpriced"}})
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
