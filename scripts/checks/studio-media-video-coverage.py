#!/usr/bin/env python3
"""Guard Studio video presentation coverage.

Backend membership is catalog-driven: a video model becomes public/usable when it
is both priced in tk_pricing_overlay.json and present in one of the servable
sources (Gemini/Grok Go allowlists or the newapi served-models manifest). The
Studio frontend may add friendly presentation metadata, but it must not lag the
backend for public video models: every public video needs explicit presentation
and non-empty discrete durations so the UI never falls back to an unsafe default.
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


def priced_video_ids(overlay_text: str) -> set[str]:
    data = json.loads(overlay_text)
    out: set[str] = set()
    for model_id, entry in data.items():
        if model_id.startswith("_") or not isinstance(entry, dict):
            continue
        if entry.get("mode") == "video_generation" or (entry.get("output_cost_per_second") or 0) > 0:
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


def public_servable_video_ids(overlay_text: str, go_text: str, manifest_text: str) -> set[str]:
    priced = priced_video_ids(overlay_text)
    gemini = parse_go_map_ids(go_text, "supportedGeminiCatalogModels")
    grok = parse_go_map_ids(go_text, "supportedGrokCatalogModels")
    manifest = manifest_model_ids(manifest_text)
    return priced & (gemini | grok | manifest)


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


def frontend_video_presentations(ts_text: str) -> dict[str, dict[str, object]]:
    out: dict[str, dict[str, object]] = {}
    for obj in _top_level_objects(_media_models_array(ts_text)):
        if not re.search(r"modality:\s*'video'", obj):
            continue
        mid = re.search(r"modelId:\s*'([^']+)'", obj)
        if not mid:
            continue
        model_id = mid.group(1)
        aliases_m = re.search(r"aliasIds:\s*\[([^\]]*)\]", obj, re.S)
        aliases = _single_quoted_values(aliases_m.group(1)) if aliases_m else set()
        durations_m = re.search(r"videoDurations:\s*\[([^\]]*)\]", obj, re.S)
        durations = []
        if durations_m:
            durations = [int(x) for x in re.findall(r"\b\d+\b", durations_m.group(1))]
        rec = {"model_id": model_id, "aliases": aliases, "durations": durations}
        out[model_id] = rec
        for alias in aliases:
            out[alias] = rec
    return out


def check(quiet: bool = False) -> int:
    try:
        public_video = public_servable_video_ids(
            OVERLAY.read_text(encoding="utf-8"),
            GO_ALLOWLIST.read_text(encoding="utf-8"),
            MANIFEST.read_text(encoding="utf-8"),
        )
        presentations = frontend_video_presentations(MEDIA_TIERS.read_text(encoding="utf-8"))
    except Exception as exc:  # noqa: BLE001
        print(f"studio-media-video-coverage: error: {exc}", file=sys.stderr)
        return 2

    errors: list[str] = []
    frontend_video_ids = {
        model_id for model_id, rec in presentations.items() if rec.get("model_id") == model_id
    }
    extra = frontend_video_ids - public_video
    for model_id in sorted(extra):
        errors.append(f"{model_id}: Studio video presentation exists but backend is not public servable")
    for model_id in sorted(public_video):
        rec = presentations.get(model_id)
        if not rec:
            errors.append(f"{model_id}: public servable video lacks explicit Studio presentation")
            continue
        durations = rec.get("durations")
        if not isinstance(durations, list) or not durations:
            errors.append(f"{model_id}: Studio presentation lacks non-empty videoDurations")
    # The no-prefix Seedance key is pricing parity only; the routed public model
    # is the doubao-* manifest/mapping id.
    direct = presentations.get("seedance-1-0-pro-250528")
    if direct and direct.get("model_id") == "seedance-1-0-pro-250528":
        errors.append("seedance-1-0-pro-250528 must be an alias, not the Studio canonical modelId")

    if errors:
        print(f"studio-media-video-coverage: FAIL ({len(errors)} issue(s))", file=sys.stderr)
        for err in errors:
            print(f"  - {err}", file=sys.stderr)
        return 1
    if not quiet:
        print(f"studio-media-video-coverage: ok ({len(public_video)} public video models covered)")
    return 0


def selftest() -> int:
    overlay = json.dumps(
        {
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
        'var supportedGeminiCatalogModels = map[string]struct{}{\n\t"veo-3.1-generate-001": {},\n}\n'
        'var supportedGrokCatalogModels = map[string]struct{}{\n\t"grok-imagine-video": {},\n}\n'
    )
    manifest = json.dumps(
        {
            "entries": {
                "newapi/x": {"model_id": "missing-video"},
                "newapi/fast": {"model_id": "doubao-seedance-1-0-pro-fast-251015"},
            }
        }
    )
    assert public_servable_video_ids(overlay, go, manifest) == {
        "doubao-seedance-1-0-pro-fast-251015",
        "veo-3.1-generate-001",
        "grok-imagine-video",
    }
    ts = """
export const MEDIA_MODELS: MediaModel[] = [
  { modelId: 'veo-3.1-generate-001', modality: 'video', videoDurations: [4, 6, 8] },
  { modelId: 'grok-imagine-video', modality: 'video', videoDurations: [5] },
  { modelId: 'doubao-seedance-1-0-pro-fast-251015', modality: 'video', videoDurations: [5] },
]
"""
    presentations = frontend_video_presentations(ts)
    assert presentations["veo-3.1-generate-001"]["durations"] == [4, 6, 8]
    assert presentations["grok-imagine-video"]["durations"] == [5]
    assert presentations["doubao-seedance-1-0-pro-fast-251015"]["durations"] == [5]
    assert set(presentations) == {
        "doubao-seedance-1-0-pro-fast-251015",
        "grok-imagine-video",
        "veo-3.1-generate-001",
    }
    alias_ts = """
export const MEDIA_MODELS: MediaModel[] = [
  { modelId: 'doubao-seedance-1-0-pro-250528', aliasIds: ['seedance-1-0-pro-250528'], modality: 'video', videoDurations: [5] },
]
"""
    alias_presentations = frontend_video_presentations(alias_ts)
    assert alias_presentations["seedance-1-0-pro-250528"]["model_id"] == "doubao-seedance-1-0-pro-250528"
    print("studio-media-video-coverage selftest: PASS")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--quiet", action="store_true")
    parser.add_argument("--selftest", action="store_true")
    args = parser.parse_args()
    return selftest() if args.selftest else check(args.quiet)


if __name__ == "__main__":
    raise SystemExit(main())
