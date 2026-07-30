#!/usr/bin/env python3
"""Apply official video_price_tiers to tk_pricing_overlay.json (SSOT).

Pre-tax USD/s; volcengine rows get ×official_list_base_tax at Go read time.
Run after tier matrix changes: python3 scripts/checks/apply-video-overlay-tiers.py
"""

from __future__ import annotations

import json
import pathlib
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent.parent
OVERLAY = REPO / "backend" / "internal" / "service" / "tk_pricing_overlay.json"

TK_CNY_PER_USD = 6.7
T480 = 50220.0 / 5
T720 = 21600.0
T1080 = 48600.0
T4K = 194400.0


def seedance_usd(tokens_per_sec: float, cny_per_m: float) -> float:
    return tokens_per_sec * cny_per_m / 1e6 / TK_CNY_PER_USD


def tier(
    resolution: str,
    per_second: float,
    *,
    silent: float | None = None,
    image_surcharge: float | None = None,
    default: bool = False,
) -> dict:
    row: dict = {
        "resolution": resolution,
        "output_cost_per_second": per_second,
    }
    if silent is not None:
        row["output_cost_per_second_silent"] = silent
    if image_surcharge is not None:
        row["input_image_surcharge_per_second"] = image_surcharge
    if default:
        row["default_for_model"] = True
    return row


def seedance_tiers(
    specs: list[tuple[str, float, float | None]],
    *,
    default_resolution: str,
    audio_cny: tuple[float, float] | None = None,
) -> tuple[list[dict], str]:
    """specs: (resolution, tokens_per_sec, cny_per_m) — cny ignored when audio_cny set."""
    out: list[dict] = []
    for res, tok, cny in specs:
        if audio_cny:
            with_a, silent = audio_cny
            out.append(
                tier(
                    res,
                    seedance_usd(tok, with_a),
                    silent=seedance_usd(tok, silent),
                    default=res == default_resolution,
                )
            )
        else:
            out.append(
                tier(
                    res,
                    seedance_usd(tok, cny),
                    default=res == default_resolution,
                )
            )
    return out, default_resolution


def min_pre_tax(tiers: list[dict]) -> float:
    vals: list[float] = []
    for t in tiers:
        vals.append(t["output_cost_per_second"])
        if s := t.get("output_cost_per_second_silent"):
            vals.append(s)
    return min(vals)


SEEDANCE: dict[str, tuple[list[dict], str]] = {}

for model, specs, default_res in [
    (
        "doubao-seedance-1-0-pro-250528",
        [("480p", T480, 15), ("720p", T720, 15), ("1080p", T1080, 15)],
        "1080p",
    ),
    (
        "doubao-seedance-1-0-pro-fast-251015",
        [("480p", T480, 4.2), ("720p", T720, 4.2)],
        "720p",
    ),
    (
        "doubao-seedance-1-5-pro-251215",
        [("480p", T480, 0), ("720p", T720, 0), ("1080p", T1080, 0)],
        "1080p",
    ),
    (
        "doubao-seedance-2-0-260128",
        [("480p", T480, 46), ("720p", T720, 46), ("1080p", T1080, 51), ("4k", T4K, 26)],
        "1080p",
    ),
    (
        "doubao-seedance-2-0-fast-260128",
        [("480p", T480, 37), ("720p", T720, 37)],
        "720p",
    ),
]:
    if model == "doubao-seedance-1-5-pro-251215":
        tiers, dr = seedance_tiers(specs, default_resolution=default_res, audio_cny=(16, 8))
    else:
        tiers, dr = seedance_tiers(specs, default_resolution=default_res)
    SEEDANCE[model] = (tiers, dr)

SEEDANCE["seedance-1-0-pro-250528"] = SEEDANCE["doubao-seedance-1-0-pro-250528"]

VEO: dict[str, tuple[list[dict], str]] = {
    "veo-2.0-generate-001": (
        [tier("720p", 0.50, default=True)],
        "720p",
    ),
    "veo-3.0-generate-001": (
        [
            tier("720p", 0.40, silent=0.20),
            tier("1080p", 0.40, silent=0.20, default=True),
        ],
        "1080p",
    ),
    "veo-3.0-fast-generate-001": (
        [
            tier("720p", 0.10),
            tier("1080p", 0.12, default=True),
        ],
        "1080p",
    ),
    "veo-3.1-generate-001": (
        [
            tier("720p", 0.40, silent=0.20),
            tier("1080p", 0.40, silent=0.20, default=True),
            tier("4k", 0.60, silent=0.40),
        ],
        "1080p",
    ),
    "veo-3.1-generate-preview": (
        [
            tier("720p", 0.40, silent=0.20),
            tier("1080p", 0.40, silent=0.20, default=True),
            tier("4k", 0.60, silent=0.40),
        ],
        "1080p",
    ),
    "veo-3.1-fast-generate-001": (
        [
            tier("720p", 0.10),
            tier("1080p", 0.12, default=True),
            tier("4k", 0.30, silent=0.25),
        ],
        "1080p",
    ),
    "veo-3.1-fast-generate-preview": (
        [
            tier("720p", 0.10),
            tier("1080p", 0.12, default=True),
            tier("4k", 0.30, silent=0.25),
        ],
        "1080p",
    ),
    "veo-3.1-lite-generate-preview": (
        [
            tier("720p", 0.05, silent=0.03),
            tier("1080p", 0.08, silent=0.05, default=True),
        ],
        "1080p",
    ),
}

GROK: dict[str, tuple[list[dict], str]] = {
    "grok-imagine-video": (
        [
            tier("480p", 0.05, image_surcharge=0.01, default=True),
            tier("720p", 0.07, image_surcharge=0.01),
        ],
        "480p",
    ),
    "grok-imagine-video-1.5": (
        [
            tier("480p", 0.08, default=True),
            tier("720p", 0.14),
            tier("1080p", 0.25),
        ],
        "480p",
    ),
}


def apply(data: dict) -> int:
    updated = 0
    for model, (tiers, default_res) in {**SEEDANCE, **VEO, **GROK}.items():
        entry = data.get(model)
        if not isinstance(entry, dict):
            print(f"  WARN: missing overlay entry {model!r}, skipping", file=sys.stderr)
            continue
        entry["video_price_tiers"] = tiers
        entry["default_video_resolution"] = default_res
        entry["output_cost_per_second"] = min_pre_tax(tiers)
        updated += 1
    return updated


def main() -> int:
    data = json.loads(OVERLAY.read_text(encoding="utf-8"))
    n = apply(data)
    OVERLAY.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"updated {n} video models in {OVERLAY.relative_to(REPO)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
