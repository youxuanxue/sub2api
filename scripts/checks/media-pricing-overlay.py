#!/usr/bin/env python3
"""Validate the TK-owned media (image/video) pricing overlay.

Source of truth: backend/internal/service/tk_media_pricing_overlay.json — a small
curated overlay merged (fill-only) into every PricingService load so imagen-*/veo-*
resolve to a real price even though the production runtime source (Wei-Shaw mirror,
a trimmed litellm) drops those keys. Without this overlay imagen silently bills the
$0.134 default and veo bills $0.

This check hardens that against silent regression (CLAUDE.md §5 "upgrade principle":
a soft rule that bit us once becomes a mechanical gate). It asserts:
  1. The overlay parses and is non-empty.
  2. Anchor models are present with a non-zero price in the right field:
       imagen-4.0-generate-001 -> output_cost_per_image > 0
       veo-3.1-generate-001    -> output_cost_per_second > 0
  3. EVERY entry has a recognized mode and a > 0 price in the matching field
     (no silently-shipped $0 media entry, which would deduct nothing).

Usage: python3 scripts/checks/media-pricing-overlay.py [--quiet]
Exit 0 ok, 1 violation, 2 missing dep / file / unparseable.
"""

from __future__ import annotations

import json
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
OVERLAY = REPO_ROOT / "backend" / "internal" / "service" / "tk_media_pricing_overlay.json"

# mode -> the price field that MUST be > 0 for that mode
MODE_FIELD = {
    "image_generation": "output_cost_per_image",
    "video_generation": "output_cost_per_second",
}

ANCHORS = {
    "imagen-4.0-generate-001": "output_cost_per_image",
    "veo-3.1-generate-001": "output_cost_per_second",
}


def main() -> int:
    quiet = "--quiet" in sys.argv

    if not OVERLAY.is_file():
        print(f"  FAIL: media overlay not found: {OVERLAY}", flush=True)
        return 2
    try:
        data = json.loads(OVERLAY.read_text(encoding="utf-8"))
    except (ValueError, OSError) as exc:
        print(f"  FAIL: media overlay unparseable: {exc}", flush=True)
        return 2

    # Entries are bare model -> pricing dict; keys starting with "_" (e.g. _meta) are
    # provenance, not pricing.
    entries = {k: v for k, v in data.items() if not k.startswith("_")}
    errors: list[str] = []

    if not entries:
        errors.append("overlay has zero pricing entries")

    for model, pricing in entries.items():
        if not isinstance(pricing, dict):
            errors.append(f"{model}: entry is not an object")
            continue
        mode = pricing.get("mode")
        field = MODE_FIELD.get(mode)
        if field is None:
            errors.append(f"{model}: unrecognized mode {mode!r} (want one of {sorted(MODE_FIELD)})")
            continue
        price = pricing.get(field)
        if not isinstance(price, (int, float)) or price <= 0:
            errors.append(f"{model}: mode={mode} requires {field} > 0, got {price!r}")

    for model, field in ANCHORS.items():
        pricing = entries.get(model)
        if not isinstance(pricing, dict):
            errors.append(f"anchor {model} missing from overlay")
            continue
        price = pricing.get(field)
        if not isinstance(price, (int, float)) or price <= 0:
            errors.append(f"anchor {model}: {field} must be > 0, got {price!r}")

    if errors:
        print(f"  FAIL: media pricing overlay invalid ({len(errors)} issue(s)):", flush=True)
        for e in errors:
            print(f"    - {e}", flush=True)
        return 1

    if not quiet:
        print(f"  ok: {len(entries)} media overlay entries valid (anchors present, no $0)", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
