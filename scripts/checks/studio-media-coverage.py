#!/usr/bin/env python3
"""Guard Studio media presentation coverage.

Backend media membership is catalog-driven: an image/video model becomes public
usable when the public pricing catalog carries `billing_mode=image|video` and
the model id is present in one of the servable sources (Go allowlists or the
newapi served-models manifest). Studio metadata is presentation-only, but it
must not lag backend public media models: images need explicit presentation
plus a safe size/aspect contract, and videos need explicit presentation plus
non-empty discrete durations.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
BASE_CATALOG = REPO / "backend/resources/model-pricing/model_prices_and_context_window.json"
OVERLAY = REPO / "backend/internal/service/tk_pricing_overlay.json"
GO_ALLOWLIST = REPO / "backend/internal/service/pricing_catalog_supported_models_tk.go"
MANIFEST = REPO / "backend/internal/service/tk_served_models.json"
MEDIA_PRESENTATIONS = REPO / "frontend/src/constants/studioMediaPresentations.tk.ts"

MODALITIES = ("image", "video")
TOKEN_PRICE_FIELDS = ("input_cost_per_token", "output_cost_per_token")
GO_SERVABLE_MAPS = (
    "supportedAnthropicCatalogModels",
    "supportedOpenAICatalogModels",
    "supportedGeminiCatalogModels",
    "supportedAntigravityCatalogModels",
    "supportedGrokCatalogModels",
)


def _positive(entry: dict[str, object], field: str) -> bool:
    value = entry.get(field)
    return isinstance(value, (int, float)) and value > 0


def _has_price_field(entry: dict[str, object], field: str) -> bool:
    return field in entry and entry.get(field) is not None


def _catalog_media_modality(entry: dict[str, object]) -> str | None:
    mode = entry.get("mode")
    has_token_price = any(_has_price_field(entry, field) for field in TOKEN_PRICE_FIELDS)
    pure_media_without_mode = not mode and not has_token_price
    if _positive(entry, "output_cost_per_second") and (
        mode == "video_generation" or pure_media_without_mode
    ):
        return "video"
    if _positive(entry, "output_cost_per_image") and (
        mode == "image_generation" or pure_media_without_mode
    ):
        return "image"
    return None


def _priced_catalog_row(entry: dict[str, object]) -> bool:
    return any(
        _has_price_field(entry, field)
        for field in (*TOKEN_PRICE_FIELDS, "output_cost_per_image", "output_cost_per_second")
    )


def _overlay_catalog_entry(entry: dict[str, object]) -> dict[str, object]:
    # Matches applyCatalogOverlayPricing's synthetic catalogRichEntry: overlay
    # entries get token fields (zero when absent) plus explicit media fields.
    out: dict[str, object] = {
        "litellm_provider": entry.get("litellm_provider", ""),
        "mode": entry.get("mode", ""),
        "input_cost_per_token": entry.get("input_cost_per_token", 0),
        "output_cost_per_token": entry.get("output_cost_per_token", 0),
    }
    for field in ("output_cost_per_image", "output_cost_per_second"):
        if _positive(entry, field):
            out[field] = entry[field]
    return out


def catalog_media_ids(catalog_text: str, overlay_text: str, modality: str) -> set[str]:
    catalog = json.loads(catalog_text)
    overlay = json.loads(overlay_text)
    rows: dict[str, dict[str, object]] = {}
    for model_id, entry in catalog.items():
        if model_id == "sample_spec" or not isinstance(entry, dict):
            continue
        if _priced_catalog_row(entry):
            rows[model_id] = dict(entry)

    for model_id, entry in overlay.items():
        if model_id.startswith("_") or not isinstance(entry, dict):
            continue
        is_media = _positive(entry, "output_cost_per_image") or _positive(entry, "output_cost_per_second")
        if not _positive(entry, "input_cost_per_token") and not _positive(entry, "output_cost_per_token") and not is_media:
            continue
        if model_id in rows:
            continue
        rows[model_id] = _overlay_catalog_entry(entry)

    out: set[str] = set()
    for model_id, entry in rows.items():
        if _catalog_media_modality(entry) == modality:
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
    catalog_text: str,
    overlay_text: str,
    go_text: str,
    manifest_text: str,
    modality: str,
) -> set[str]:
    return catalog_media_ids(catalog_text, overlay_text, modality) & servable_source_ids(go_text, manifest_text)


def _media_presentations_array(ts_text: str) -> str:
    m = re.search(r"export\s+const\s+MEDIA_MODEL_PRESENTATIONS\b[^\n=]*=\s*\[", ts_text)
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
    for obj in _top_level_objects(_media_presentations_array(ts_text)):
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


def coverage_errors(catalog_text: str, overlay_text: str, go_text: str, manifest_text: str, ts_text: str) -> list[str]:
    public_by_modality = {
        modality: public_servable_media_ids(catalog_text, overlay_text, go_text, manifest_text, modality)
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
        catalog_text = BASE_CATALOG.read_text(encoding="utf-8")
        overlay_text = OVERLAY.read_text(encoding="utf-8")
        go_text = GO_ALLOWLIST.read_text(encoding="utf-8")
        manifest_text = MANIFEST.read_text(encoding="utf-8")
        ts_text = MEDIA_PRESENTATIONS.read_text(encoding="utf-8")
        errors = coverage_errors(catalog_text, overlay_text, go_text, manifest_text, ts_text)
        counts = {
            modality: len(public_servable_media_ids(catalog_text, overlay_text, go_text, manifest_text, modality))
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
    catalog = json.dumps(
        {
            "base-catalog-image": {
                "mode": "image_generation",
                "output_cost_per_image": 0.011,
                "litellm_provider": "vertex_ai",
            },
            "gemini-3.1-pro-low": {
                "mode": "chat",
                "input_cost_per_token": 0.000002,
                "output_cost_per_token": 0.000012,
                "output_cost_per_image": 0.00012,
                "litellm_provider": "vertex_ai-language-models",
            },
        }
    )
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
        '\t"base-catalog-image": {},\n\t"imagen-4.0-generate-001": {},\n\t"veo-3.1-generate-001": {},\n}\n'
        'var supportedAntigravityCatalogModels = map[string]struct{}{\n'
        '\t"gemini-3-pro-image": {},\n\t"gemini-3.1-pro-low": {},\n}\n'
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
    assert public_servable_media_ids(catalog, overlay, go, manifest, "image") == {
        "base-catalog-image",
        "doubao-seedream-5-0-260128",
        "gemini-3-pro-image",
        "grok-imagine-image",
        "imagen-4.0-generate-001",
    }
    assert "gemini-3.1-pro-low" not in public_servable_media_ids(catalog, overlay, go, manifest, "image")
    assert public_servable_media_ids(catalog, overlay, go, manifest, "video") == {
        "doubao-seedance-1-0-pro-fast-251015",
        "grok-imagine-video",
        "veo-3.1-generate-001",
    }
    ts = """
export const MEDIA_MODEL_PRESENTATIONS: MediaModelPresentation[] = [
  { modelId: 'base-catalog-image', modality: 'image', imageSizes: IMAGEN_IMAGE_SIZES },
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
    assert not coverage_errors(catalog, overlay, go, manifest, ts)

    bad_ts = """
export const MEDIA_MODEL_PRESENTATIONS: MediaModelPresentation[] = [
  { modelId: 'doubao-seedream-5-0-260128', modality: 'image' },
]
"""
    bad_errors = coverage_errors(catalog, overlay, "", manifest, bad_ts)
    assert any("imageSizes or explicit flatPricePerImage" in err for err in bad_errors), bad_errors

    alias_ts = """
export const MEDIA_MODEL_PRESENTATIONS: MediaModelPresentation[] = [
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
