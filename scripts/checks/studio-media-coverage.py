#!/usr/bin/env python3
"""Guard Studio media presentation coverage.

Backend media membership is catalog-driven: an image/video model becomes public
usable when it is priced in tk_pricing_overlay.json and present in one of the
servable sources (Go allowlists or the newapi served-models manifest). Studio
metadata is presentation-only, but it must not lag backend public media models:
images need explicit presentation plus a safe size/aspect contract, and videos
need explicit presentation plus non-empty discrete durations.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
OVERLAY = REPO / "backend/internal/service/tk_pricing_overlay.json"
GO_ALLOWLIST = REPO / "backend/internal/service/pricing_catalog_supported_models_tk.go"
MANIFEST = REPO / "backend/internal/service/tk_served_models.json"
MEDIA_TIERS = REPO / "frontend/src/constants/mediaTiers.tk.ts"

MODALITIES = ("image", "video")
MEDIA_PRICE_FIELDS = {
    "image": ("image_generation", "output_cost_per_image"),
    "video": ("video_generation", "output_cost_per_second"),
}
GO_SERVABLE_MAPS = (
    "supportedAnthropicCatalogModels",
    "supportedOpenAICatalogModels",
    "supportedGeminiCatalogModels",
    "supportedAntigravityCatalogModels",
    "supportedGrokCatalogModels",
)


def priced_media_ids(overlay_text: str, modality: str) -> set[str]:
    expected_mode, cost_field = MEDIA_PRICE_FIELDS[modality]
    data = json.loads(overlay_text)
    out: set[str] = set()
    for model_id, entry in data.items():
        if model_id.startswith("_") or not isinstance(entry, dict):
            continue
        if entry.get("mode") == expected_mode or (entry.get(cost_field) or 0) > 0:
            out.add(model_id)
    return out


def parse_go_map_ids(go_text: str, name: str) -> set[str]:
    m = re.search(rf"var {re.escape(name)} = map\[string\]struct\{{\}}\{{(.*?)\n\}}", go_text, re.S)
    if not m:
        return set()
    return set(re.findall(r'"([^"]+)":\s*\{\}', m.group(1)))


def manifest_model_ids(manifest_text: str) -> set[str]:
    data = json.loads(manifest_text)
    entries = data.get("entries") if isinstance(data, dict) else None
    if not isinstance(entries, dict):
        return set()
    out: set[str] = set()
    for entry in entries.values():
        if isinstance(entry, dict) and isinstance(entry.get("model_id"), str):
            out.add(entry["model_id"])
    return out


def servable_source_ids(go_text: str, manifest_text: str) -> set[str]:
    out = manifest_model_ids(manifest_text)
    for name in GO_SERVABLE_MAPS:
        out |= parse_go_map_ids(go_text, name)
    return out


def public_servable_media_ids(
    overlay_text: str,
    go_text: str,
    manifest_text: str,
    modality: str,
) -> set[str]:
    return priced_media_ids(overlay_text, modality) & servable_source_ids(go_text, manifest_text)


def _media_models_array(ts_text: str) -> str:
    m = re.search(r"export\s+const\s+MEDIA_MODELS\b[^\n=]*=\s*\[", ts_text)
    if not m:
        return ""
    open_idx = m.end() - 1
    depth = 0
    for i in range(open_idx, len(ts_text)):
        ch = ts_text[i]
        if ch == "[":
            depth += 1
        elif ch == "]":
            depth -= 1
            if depth == 0:
                return ts_text[open_idx + 1 : i]
    return ""


def _top_level_objects(array_text: str) -> list[str]:
    out: list[str] = []
    depth = 0
    start = -1
    for i, ch in enumerate(array_text):
        if ch == "{":
            if depth == 0:
                start = i
            depth += 1
        elif ch == "}":
            depth -= 1
            if depth == 0 and start >= 0:
                out.append(array_text[start : i + 1])
                start = -1
    return out


def _single_quoted_values(text: str) -> set[str]:
    return set(re.findall(r"'([^']+)'", text))


def _has_explicit_image_sizes(obj: str) -> bool:
    return bool(re.search(r"\bimageSizes\s*:\s*(?:[A-Za-z_][A-Za-z0-9_]*|\[[^\]]+\])", obj, re.S))


def frontend_media_presentations(ts_text: str) -> dict[str, dict[str, object]]:
    out: dict[str, dict[str, object]] = {}
    for obj in _top_level_objects(_media_models_array(ts_text)):
        modality_m = re.search(r"modality:\s*'(image|video)'", obj)
        if not modality_m:
            continue
        mid = re.search(r"modelId:\s*'([^']+)'", obj)
        if not mid:
            continue
        model_id = mid.group(1)
        aliases_m = re.search(r"aliasIds:\s*\[([^\]]*)\]", obj, re.S)
        aliases = _single_quoted_values(aliases_m.group(1)) if aliases_m else set()
        durations_m = re.search(r"videoDurations:\s*\[([^\]]*)\]", obj, re.S)
        durations = [int(x) for x in re.findall(r"\b\d+\b", durations_m.group(1))] if durations_m else []
        rec = {
            "model_id": model_id,
            "modality": modality_m.group(1),
            "aliases": aliases,
            "durations": durations,
            "has_image_sizes": _has_explicit_image_sizes(obj),
            "flat_price_per_image": bool(re.search(r"\bflatPricePerImage\s*:\s*true\b", obj)),
        }
        out[model_id] = rec
        for alias in aliases:
            out[alias] = rec
    return out


def coverage_errors(overlay_text: str, go_text: str, manifest_text: str, ts_text: str) -> list[str]:
    public_by_modality = {
        modality: public_servable_media_ids(overlay_text, go_text, manifest_text, modality)
        for modality in MODALITIES
    }
    presentations = frontend_media_presentations(ts_text)
    presentations_by_modality = {
        modality: {
            model_id: rec
            for model_id, rec in presentations.items()
            if rec.get("modality") == modality
        }
        for modality in MODALITIES
    }

    errors: list[str] = []
    for model_id, rec in sorted(presentations.items()):
        if rec.get("model_id") != model_id:
            continue
        modality = rec.get("modality")
        if modality not in public_by_modality:
            continue
        ids = {model_id} | set(rec.get("aliases") or set())
        if ids.isdisjoint(public_by_modality[modality]):
            errors.append(
                f"{model_id}: Studio {modality} presentation exists but no canonical/alias id is backend public servable"
            )

    for modality, ids in public_by_modality.items():
        for model_id in sorted(ids):
            rec = presentations_by_modality[modality].get(model_id)
            if not rec:
                errors.append(f"{model_id}: public servable {modality} lacks explicit Studio presentation")
                continue
            if modality == "image":
                if not rec.get("has_image_sizes") and not rec.get("flat_price_per_image"):
                    errors.append(
                        f"{model_id}: Studio image presentation lacks imageSizes or explicit flatPricePerImage"
                    )
            else:
                durations = rec.get("durations")
                if not isinstance(durations, list) or not durations:
                    errors.append(f"{model_id}: Studio video presentation lacks non-empty videoDurations")

    # The no-prefix Seedance key is pricing parity only; the routed public model
    # is the doubao-* manifest/mapping id.
    direct = presentations.get("seedance-1-0-pro-250528")
    if direct and direct.get("model_id") == "seedance-1-0-pro-250528":
        errors.append("seedance-1-0-pro-250528 must be an alias, not the Studio canonical modelId")
    return errors


def check(quiet: bool = False) -> int:
    try:
        overlay_text = OVERLAY.read_text(encoding="utf-8")
        go_text = GO_ALLOWLIST.read_text(encoding="utf-8")
        manifest_text = MANIFEST.read_text(encoding="utf-8")
        ts_text = MEDIA_TIERS.read_text(encoding="utf-8")
        errors = coverage_errors(overlay_text, go_text, manifest_text, ts_text)
        counts = {
            modality: len(public_servable_media_ids(overlay_text, go_text, manifest_text, modality))
            for modality in MODALITIES
        }
    except Exception as exc:  # noqa: BLE001
        print(f"studio-media-coverage: error: {exc}", file=sys.stderr)
        return 2

    if errors:
        print(f"studio-media-coverage: FAIL ({len(errors)} issue(s))", file=sys.stderr)
        for err in errors:
            print(f"  - {err}", file=sys.stderr)
        return 1
    if not quiet:
        print(
            "studio-media-coverage: ok "
            f"({counts['image']} public image models, {counts['video']} public video models covered)"
        )
    return 0


def selftest() -> int:
    overlay = json.dumps(
        {
            "imagen-4.0-generate-001": {"mode": "image_generation", "output_cost_per_image": 0.04},
            "gemini-3-pro-image": {"mode": "image_generation", "output_cost_per_image": 0.0672},
            "grok-imagine-image": {"mode": "image_generation", "output_cost_per_image": 0.02},
            "doubao-seedream-5-0-260128": {"mode": "image_generation", "output_cost_per_image": 0.0328},
            "veo-3.1-generate-001": {"mode": "video_generation", "output_cost_per_second": 0.6},
            "grok-imagine-video": {"mode": "video_generation", "output_cost_per_second": 0.08},
            "doubao-seedance-1-0-pro-fast-251015": {
                "mode": "video_generation",
                "output_cost_per_second": 0.0305,
            },
            "text": {"mode": "chat", "input_cost_per_token": 1, "output_cost_per_token": 1},
        }
    )
    go = (
        'var supportedGeminiCatalogModels = map[string]struct{}{\n'
        '\t"imagen-4.0-generate-001": {},\n\t"veo-3.1-generate-001": {},\n}\n'
        'var supportedAntigravityCatalogModels = map[string]struct{}{\n\t"gemini-3-pro-image": {},\n}\n'
        'var supportedGrokCatalogModels = map[string]struct{}{\n'
        '\t"grok-imagine-image": {},\n\t"grok-imagine-video": {},\n}\n'
    )
    manifest = json.dumps(
        {
            "entries": {
                "newapi/seedream": {"model_id": "doubao-seedream-5-0-260128"},
                "newapi/fast": {"model_id": "doubao-seedance-1-0-pro-fast-251015"},
            }
        }
    )
    assert public_servable_media_ids(overlay, go, manifest, "image") == {
        "doubao-seedream-5-0-260128",
        "gemini-3-pro-image",
        "grok-imagine-image",
        "imagen-4.0-generate-001",
    }
    assert public_servable_media_ids(overlay, go, manifest, "video") == {
        "doubao-seedance-1-0-pro-fast-251015",
        "grok-imagine-video",
        "veo-3.1-generate-001",
    }
    ts = """
export const MEDIA_MODELS: MediaModel[] = [
  { modelId: 'imagen-4.0-generate-001', modality: 'image', imageSizes: IMAGEN_IMAGE_SIZES },
  { modelId: 'gemini-3-pro-image-preview', aliasIds: ['gemini-3-pro-image'], modality: 'image', flatImageBilling: true, imageSizes: GEMINI_IMAGE_SIZES },
  { modelId: 'grok-imagine-image', modality: 'image', flatPricePerImage: true },
  { modelId: 'doubao-seedream-5-0-260128', modality: 'image', imageSizes: SEEDREAM_IMAGE_SIZES },
  { modelId: 'veo-3.1-generate-001', modality: 'video', videoDurations: [4, 6, 8] },
  { modelId: 'grok-imagine-video', modality: 'video', videoDurations: [5] },
  { modelId: 'doubao-seedance-1-0-pro-fast-251015', modality: 'video', videoDurations: [5] },
]
"""
    presentations = frontend_media_presentations(ts)
    assert presentations["gemini-3-pro-image"]["model_id"] == "gemini-3-pro-image-preview"
    assert presentations["grok-imagine-image"]["flat_price_per_image"] is True
    assert presentations["veo-3.1-generate-001"]["durations"] == [4, 6, 8]
    assert not coverage_errors(overlay, go, manifest, ts)

    bad_ts = """
export const MEDIA_MODELS: MediaModel[] = [
  { modelId: 'doubao-seedream-5-0-260128', modality: 'image' },
]
"""
    bad_errors = coverage_errors(overlay, "", manifest, bad_ts)
    assert any("imageSizes or explicit flatPricePerImage" in err for err in bad_errors), bad_errors

    alias_ts = """
export const MEDIA_MODELS: MediaModel[] = [
  { modelId: 'doubao-seedance-1-0-pro-250528', aliasIds: ['seedance-1-0-pro-250528'], modality: 'video', videoDurations: [5] },
]
"""
    alias_presentations = frontend_media_presentations(alias_ts)
    assert alias_presentations["seedance-1-0-pro-250528"]["model_id"] == "doubao-seedance-1-0-pro-250528"
    print("studio-media-coverage selftest: PASS")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--quiet", action="store_true")
    parser.add_argument("--selftest", action="store_true")
    args = parser.parse_args()
    return selftest() if args.selftest else check(args.quiet)


if __name__ == "__main__":
    raise SystemExit(main())
