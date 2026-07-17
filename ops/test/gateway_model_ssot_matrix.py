#!/usr/bin/env python3
"""Derive and optionally probe the universal-key model/protocol matrix.

The source is the user-visible priced catalog projection
(`/api/v1/public/pricing`), which is built from TokenKey's servable allowlists,
curated newapi manifest, and pricing sources. This tool deliberately does not
maintain a second hand-written model list.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

_OPS_TEST_DIR = os.path.dirname(os.path.abspath(__file__))
if _OPS_TEST_DIR not in sys.path:
    sys.path.insert(0, _OPS_TEST_DIR)
from ssot_recent_success import load_skip_keys, parse_recent_success_tsv

DEFAULT_BASE_URL = os.environ.get("TK_FULLTEST_BASE_URL", "https://api.tokenkey.dev")
DEFAULT_TIMEOUT = float(os.environ.get("TK_FULLTEST_TIMEOUT", "90"))
REPO = Path(__file__).resolve().parents[2]
LOCAL_FALLBACK_PRICING = REPO / "backend/resources/model-pricing/model_prices_and_context_window.json"
LOCAL_TK_OVERLAY = REPO / "backend/internal/service/tk_pricing_overlay.json"
LOCAL_ALLOWLIST_GO = REPO / "backend/internal/service/pricing_catalog_supported_models_tk.go"
LOCAL_SERVED_MANIFEST = REPO / "backend/internal/service/tk_served_models.json"

VENDOR_PLATFORM = {
    "anthropic": "anthropic",
    "openai": "openai",
    "azure_openai": "openai",
    "gemini": "gemini",
    "google": "gemini",
    "vertex_ai": "gemini",
    "vertex_ai-language-models": "gemini",
    "antigravity": "antigravity",
    "newapi": "newapi",
    "volcengine": "newapi",
    "deepseek": "newapi",
    "dashscope": "newapi",
    "alibaba": "newapi",
    "zhipu": "newapi",
    "bigmodel": "newapi",
    "zai": "newapi",
    "xai": "grok",
    "x-ai": "grok",
}

PLATFORM_CHOICES = ("anthropic", "openai", "gemini", "antigravity", "newapi", "grok", "kiro")
GEMINI_NATIVE_PLATFORMS = {"antigravity"}

# Deploy-canary: one golden path per platform (+ Anthropic count_tokens).
DEPLOY_CANARY_PROTOCOLS: dict[str, list[str]] = {
    "anthropic": ["chat", "count_tokens"],
    "openai": ["chat"],
    "gemini": ["chat"],
    "antigravity": ["chat"],
    "newapi": ["chat"],
    "grok": ["chat"],
    "kiro": ["chat"],
}
DEFAULT_DEPLOY_CANARY_MODEL: dict[str, str] = {
    "anthropic": "claude-sonnet-4-6",
    "openai": "gpt-5.4",
    "gemini": "gemini-2.5-flash",
    "antigravity": "gemini-3.5-flash",
    "newapi": "qwen3-8b",
    "grok": "grok-code-fast-1",
}
MESSAGE_POLICY_PLATFORMS = {"openai", "newapi"}


@dataclass(frozen=True)
class MatrixRow:
    platform: str
    vendor: str
    model: str
    modality: str
    protocol: str
    method: str
    path: str
    paid: bool
    source: str
    note: str = ""


@dataclass(frozen=True)
class ExcludedRow:
    vendor: str
    model: str
    reason: str
    modality: str = ""
    paid: bool = False


def fetch_json(url: str, timeout: float) -> dict[str, Any]:
    req = urllib.request.Request(url, headers={"user-agent": "tokenkey-ssot-matrix/1"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:  # noqa: S310 fixed/operator URL
        return json.loads(resp.read().decode("utf-8"))


def priced(row: dict[str, Any]) -> bool:
    pricing = row.get("pricing") or {}
    tiers = pricing.get("tiers") or []
    base = tiers[0] if tiers else pricing
    return any(
        (base.get(field) or 0) > 0
        for field in (
            "input_per_1k_tokens",
            "output_per_1k_tokens",
            "output_cost_per_image",
            "output_cost_per_second",
        )
    )


def is_embedding_model(vendor: str, model: str) -> bool:
    v = vendor.lower()
    m = model.lower()
    return "embedding" in v or "embedding" in m or m.startswith("text-embedding")


def is_antigravity_image_model(model: str) -> bool:
    m = model.lower()
    return m.startswith("gemini-") and "image" in m


def modality_for(row: dict[str, Any]) -> str:
    pricing = row.get("pricing") or {}
    billing_mode = (pricing.get("billing_mode") or "").strip().lower()
    if billing_mode in {"image", "video"}:
        return billing_mode
    vendor = str(row.get("vendor") or "")
    model = str(row.get("model_id") or row.get("id") or "")
    if is_embedding_model(vendor, model):
        return "embeddings"
    return "text"


def paid_probe_for_modality(modality: str) -> bool:
    return modality in {"image", "video"}


def text_protocols(platform: str, model: str) -> list[tuple[str, str, str, str]]:
    rows: list[tuple[str, str, str, str]] = [
        ("messages", "POST", "/v1/messages", ""),
        ("count_tokens", "POST", "/v1/messages/count_tokens", ""),
        ("chat", "POST", "/v1/chat/completions", ""),
        ("responses", "POST", "/v1/responses", ""),
    ]
    if platform in GEMINI_NATIVE_PLATFORMS:
        rows.append(("gemini_generate", "POST", "/v1beta/models/{model}:generateContent", "native gemini shape"))
    if platform in MESSAGE_POLICY_PLATFORMS:
        rows[0] = (rows[0][0], rows[0][1], rows[0][2], "requires group allow_messages_dispatch")
        rows[1] = (rows[1][0], rows[1][1], rows[1][2], "requires group allow_messages_dispatch")
    return rows


def rows_from_public_catalog(payload: dict[str, Any], source: str) -> tuple[list[MatrixRow], list[ExcludedRow]]:
    rows: list[MatrixRow] = []
    excluded: list[ExcludedRow] = []
    for item in payload.get("data") or []:
        model = str(item.get("model_id") or item.get("id") or "").strip()
        vendor = str(item.get("vendor") or "").strip()
        if not model:
            continue
        modality = modality_for(item)
        paid_probe = paid_probe_for_modality(modality)
        if not priced(item):
            excluded.append(ExcludedRow(vendor, model, "not_priced_in_public_catalog", modality, paid_probe))
            continue
        platform = VENDOR_PLATFORM.get(vendor)
        if not platform:
            excluded.append(ExcludedRow(vendor, model, "vendor_not_mapped_to_universal_platform", modality, paid_probe))
            continue

        if modality == "text":
            for protocol, method, path, note in text_protocols(platform, model):
                rows.append(MatrixRow(platform, vendor, model, modality, protocol, method, path, False, source, note))
        elif modality == "embeddings":
            if platform in {"openai", "newapi"}:
                rows.append(MatrixRow(platform, vendor, model, modality, "embeddings", "POST", "/v1/embeddings", False, source))
            else:
                excluded.append(ExcludedRow(vendor, model, "embeddings_not_in_universal_endpoint_candidates", modality, False))
        elif modality == "image":
            if platform == "antigravity" and is_antigravity_image_model(model):
                rows.append(
                    MatrixRow(
                        platform,
                        vendor,
                        model,
                        modality,
                        "chat_image",
                        "POST",
                        "/v1/chat/completions",
                        True,
                        source,
                        "antigravity Studio image path",
                    )
                )
            else:
                rows.append(MatrixRow(platform, vendor, model, modality, "image", "POST", "/v1/images/generations", True, source))
        elif modality == "video":
            rows.append(MatrixRow(platform, vendor, model, modality, "video", "POST", "/v1/video/generations", True, source))
        else:
            excluded.append(ExcludedRow(vendor, model, f"unknown_modality:{modality}", modality, paid_probe))
    rows.sort(key=lambda r: (r.platform, r.modality, r.model, r.protocol))
    excluded.sort(key=lambda r: (r.vendor, r.model, r.reason))
    return rows, excluded


def load_json_file(path: Path) -> dict[str, Any]:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}
    return data if isinstance(data, dict) else {}


def local_media_billing_mode(entry: dict[str, Any]) -> str:
    has_token_price = "input_cost_per_token" in entry or "output_cost_per_token" in entry
    pure_media_without_mode = not entry.get("mode") and not has_token_price
    if (entry.get("output_cost_per_second") or 0) > 0 and (
        entry.get("mode") == "video_generation" or pure_media_without_mode
    ):
        return "video"
    if (entry.get("output_cost_per_image") or 0) > 0 and (
        entry.get("mode") == "image_generation" or pure_media_without_mode
    ):
        return "image"
    return ""


def local_entry_has_catalog_price(entry: dict[str, Any]) -> bool:
    return (
        "input_cost_per_token" in entry
        or "output_cost_per_token" in entry
        or bool(local_media_billing_mode(entry))
    )


def local_entry_effectively_priced(entry: dict[str, Any]) -> bool:
    return (
        (entry.get("input_cost_per_token") or 0) > 0
        or (entry.get("output_cost_per_token") or 0) > 0
        or (entry.get("output_cost_per_image") or 0) > 0
        or (entry.get("output_cost_per_second") or 0) > 0
    )


def local_pricing_from_entry(entry: dict[str, Any]) -> dict[str, Any]:
    pricing: dict[str, Any] = {
        "currency": "USD",
        "input_per_1k_tokens": (entry.get("input_cost_per_token") or 0) * 1000,
        "output_per_1k_tokens": (entry.get("output_cost_per_token") or 0) * 1000,
    }
    if (entry.get("thinking_output_cost_per_token") or 0) > 0:
        pricing["thinking_output_per_1k_tokens"] = entry["thinking_output_cost_per_token"] * 1000
    if (entry.get("cache_read_input_token_cost") or 0) > 0:
        pricing["cache_read_per_1k"] = entry["cache_read_input_token_cost"] * 1000
    if (entry.get("cache_creation_input_token_cost") or 0) > 0:
        pricing["cache_write_per_1k"] = entry["cache_creation_input_token_cost"] * 1000
    media_mode = local_media_billing_mode(entry)
    if media_mode == "image":
        pricing["billing_mode"] = "image"
        pricing["output_cost_per_image"] = entry.get("output_cost_per_image") or 0
    elif media_mode == "video":
        pricing["billing_mode"] = "video"
        pricing["output_cost_per_second"] = entry.get("output_cost_per_second") or 0
    return pricing


def local_item_from_entry(model: str, entry: dict[str, Any]) -> dict[str, Any]:
    return {
        "model_id": model,
        "vendor": str(entry.get("litellm_provider") or "").strip(),
        "pricing": local_pricing_from_entry(entry),
    }


def local_item_effectively_unpriced(item: dict[str, Any]) -> bool:
    return not priced(item)


def parse_local_allowlists() -> dict[str, set[str]]:
    text = LOCAL_ALLOWLIST_GO.read_text(encoding="utf-8")
    out: dict[str, set[str]] = {}
    for platform in ("anthropic", "openai", "gemini", "antigravity", "grok"):
        m = re.search(
            rf"servable-allowlist:begin {platform}(.*?)servable-allowlist:end {platform}",
            text,
            re.S,
        )
        out[platform] = set(re.findall(r'"([^"]+)":\s*\{\}', m.group(1))) if m else set()
    return out


def load_local_served_manifest() -> tuple[set[str], set[str]]:
    manifest = load_json_file(LOCAL_SERVED_MANIFEST)
    entries = manifest.get("entries") if isinstance(manifest.get("entries"), dict) else {}
    listed: set[str] = set()
    displayed: set[str] = set()
    for entry in entries.values():
        if not isinstance(entry, dict):
            continue
        model = str(entry.get("model_id") or "").strip()
        if not model:
            continue
        listed.add(model)
        if entry.get("display"):
            displayed.add(model)
    return listed, displayed


def is_local_newapi_longtail_vendor(vendor: str) -> bool:
    return VENDOR_PLATFORM.get(vendor) == "newapi"


def local_presentation_vendor(model: str, vendor: str, allowlists: dict[str, set[str]]) -> str:
    antigravity = allowlists.get("antigravity", set())
    gemini = allowlists.get("gemini", set())
    if model in antigravity and model not in gemini and VENDOR_PLATFORM.get(vendor) == "gemini":
        return "antigravity"
    return vendor


def local_public_catalog_supported(
    vendor: str,
    model: str,
    allowlists: dict[str, set[str]],
    manifest_displayed: set[str],
) -> bool:
    if is_local_newapi_longtail_vendor(vendor):
        return model in manifest_displayed
    platform = VENDOR_PLATFORM.get(vendor)
    if platform in {"anthropic", "openai", "gemini", "antigravity", "grok"}:
        allowed = allowlists.get(platform, set())
        return not allowed or model in allowed
    return False


def local_pricing_payload() -> dict[str, Any]:
    fallback = load_json_file(LOCAL_FALLBACK_PRICING)
    overlay = load_json_file(LOCAL_TK_OVERLAY)
    allowlists = parse_local_allowlists()
    manifest_listed, manifest_displayed = load_local_served_manifest()

    items: dict[str, dict[str, Any]] = {}
    for model, entry in fallback.items():
        if model == "sample_spec" or not isinstance(entry, dict):
            continue
        if not local_entry_has_catalog_price(entry):
            continue
        items[model] = local_item_from_entry(model, entry)

    for model in sorted(overlay):
        if model == "_meta":
            continue
        entry = overlay.get(model)
        if not isinstance(entry, dict):
            continue
        vendor = str(entry.get("litellm_provider") or "").strip()
        if is_local_newapi_longtail_vendor(vendor) and model not in manifest_listed:
            continue
        if not local_entry_effectively_priced(entry):
            continue
        if model not in items or local_item_effectively_unpriced(items[model]):
            items[model] = local_item_from_entry(model, entry)

    data: list[dict[str, Any]] = []
    for model in sorted(items):
        item = dict(items[model])
        vendor = local_presentation_vendor(model, str(item.get("vendor") or ""), allowlists)
        item["vendor"] = vendor
        if local_public_catalog_supported(vendor, model, allowlists, manifest_displayed):
            data.append(item)
    return {"object": "list", "data": data}


def load_matrix(args) -> tuple[list[MatrixRow], list[ExcludedRow]]:
    if args.source == "live-pricing":
        base = args.base_url.rstrip("/")
        payload = fetch_json(f"{base}/api/v1/public/pricing", args.timeout)
        return rows_from_public_catalog(payload, f"live-pricing:{base}/api/v1/public/pricing")
    if args.source == "local-pricing":
        return rows_from_public_catalog(local_pricing_payload(), "local-pricing:checkout")
    raise SystemExit(f"unsupported source: {args.source}")


def preferred_deploy_canary_model(platform: str) -> str:
    env_key = f"TK_SSOT_CANARY_{platform.upper()}_MODEL"
    return os.environ.get(env_key, DEFAULT_DEPLOY_CANARY_MODEL.get(platform, "")).strip()


def select_deploy_canary_rows(rows: list[MatrixRow]) -> list[MatrixRow]:
    by_platform: dict[str, list[MatrixRow]] = {}
    for row in rows:
        by_platform.setdefault(row.platform, []).append(row)
    selected: list[MatrixRow] = []
    for platform, protocols in DEPLOY_CANARY_PROTOCOLS.items():
        platform_rows = by_platform.get(platform) or []
        if not platform_rows:
            continue
        preferred = preferred_deploy_canary_model(platform)
        for protocol in protocols:
            candidates = [r for r in platform_rows if r.protocol == protocol]
            if preferred:
                preferred_rows = [r for r in candidates if r.model == preferred]
                if preferred_rows:
                    candidates = preferred_rows
            if candidates:
                selected.append(candidates[0])
    selected.sort(key=lambda r: (r.platform, r.protocol, r.model))
    return selected


def filter_rows(rows: list[MatrixRow], args) -> list[MatrixRow]:
    if args.only_platform:
        rows = [r for r in rows if r.platform == args.only_platform]
    if args.only_protocol:
        rows = [r for r in rows if r.protocol == args.only_protocol]
    wanted = requested_models(args)
    if wanted:
        rows = [r for r in rows if r.model in wanted]
    shard_count = int(getattr(args, "shard_count", 0) or 0)
    if shard_count > 1:
        shard_index = int(getattr(args, "shard_index", 0) or 0)
        rows = [row for i, row in enumerate(rows) if i % shard_count == shard_index]
    return rows


def filter_excluded(excluded: list[ExcludedRow], args) -> list[ExcludedRow]:
    rows = excluded
    wanted = requested_models(args)
    if wanted:
        rows = [r for r in rows if r.model in wanted]
    if args.only_protocol:
        rows = [r for r in rows if excluded_matches_protocol(r, args.only_protocol)]
    if args.only_platform and not wanted:
        return []
    if args.limit and not wanted and not args.only_protocol:
        return []
    return rows


def excluded_matches_protocol(row: ExcludedRow, protocol: str) -> bool:
    if protocol == "embeddings":
        return row.modality == "embeddings"
    if protocol == "image":
        return row.modality == "image"
    if protocol == "video":
        return row.modality == "video"
    if protocol == "chat_image":
        return row.modality == "image"
    if protocol in {"messages", "count_tokens", "chat", "responses", "gemini_generate"}:
        return row.modality == "text"
    return False


def requested_models(args) -> set[str]:
    out: set[str] = set()
    for raw in getattr(args, "model", []) or []:
        for part in re.split(r"[,\s]+", raw):
            if part:
                out.add(part)
    return out


def required_row_misses(args, rows: list[MatrixRow]) -> list[str]:
    if not getattr(args, "require_rows", False):
        return []
    wanted = requested_models(args)
    if not wanted:
        return []
    present = {row.model for row in rows}
    return sorted(wanted - present)


def compact_body(protocol: str, model: str) -> dict[str, Any]:
    if protocol == "messages":
        return {"model": model, "max_tokens": 8, "messages": [{"role": "user", "content": "hi"}]}
    if protocol == "count_tokens":
        body: dict[str, Any] = {"model": model, "messages": [{"role": "user", "content": "hi"}]}
        if "fable" in model.lower():
            body["max_tokens"] = 8
        return body
    if protocol == "chat":
        body: dict[str, Any] = {"model": model, "max_tokens": 8, "messages": [{"role": "user", "content": "hi"}]}
        if chat_requires_stream(model):
            body["stream"] = True
        if model in {"qwen3.7-max-preview", "qwen3.7-max-2026-05-17"}:
            body["enable_thinking"] = True
        elif re.match(r"qwen3[-.]", model):
            body["enable_thinking"] = False
        return body
    if protocol == "chat_image":
        return {
            "model": model,
            "max_tokens": 1024,
            "stream": False,
            "messages": [{"role": "user", "content": "Create a simple image of a small red circle on white."}],
            "extra_body": {"google": {"image_config": {"aspect_ratio": "1:1"}}},
        }
    if protocol == "responses":
        return {
            "model": model,
            "instructions": "You are helpful.",
            "input": [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Say OK"}]}],
            "stream": False,
        }
    if protocol == "gemini_generate":
        return {"contents": [{"role": "user", "parts": [{"text": "Reply with OK"}]}], "generationConfig": {"maxOutputTokens": 8}}
    if protocol == "image":
        size = "2048x2048" if "seedream" in model.lower() else "1024x1024"
        return {"model": model, "prompt": "a small red circle on white", "n": 1, "size": size}
    if protocol == "video":
        prompt = "a small red ball rolling on a table"
        if "seedance" in model.lower():
            prompt += " --resolution 480p --duration 5"
        return {"model": model, "prompt": prompt, "seconds": "4"}
    if protocol == "embeddings":
        return {"model": model, "input": "hello"}
    raise ValueError(f"unsupported protocol: {protocol}")


def chat_requires_stream(model: str) -> bool:
    return model in {
        "glm-4.5",
        "glm-4.5-air",
        "qwen3.7-max-preview",
        "qwen3.7-max-2026-05-17",
    }


def shape_ok(protocol: str, code: int, body: dict[str, Any], body_text: str = "") -> bool:
    if code != 200:
        return False
    if protocol == "messages":
        return body.get("type") == "message" or "content" in body
    if protocol == "count_tokens":
        return isinstance(body.get("input_tokens"), (int, float))
    if protocol == "chat":
        if body_text.lstrip().startswith("data:"):
            return True
        return bool((body.get("choices") or [{}])[0].get("message"))
    if protocol == "chat_image":
        return bool(body)
    if protocol == "responses":
        return bool(body.get("id") or body.get("output") or body.get("usage"))
    if protocol == "gemini_generate":
        return bool((body.get("candidates") or [{}])[0].get("content"))
    if protocol == "image":
        return bool((body.get("data") or [None])[0])
    if protocol == "video":
        return bool(body.get("id") or body.get("task_id"))
    if protocol == "embeddings":
        data = body.get("data") or []
        return bool(data and data[0].get("embedding") is not None)
    return False


def classify(code: int, body_text: str, ok: bool) -> tuple[str, str]:
    haystack = body_text.lower()
    if code == 200:
        return ("PASS", "") if ok else ("FAIL", "200 response shape mismatch")
    if code == 403:
        if re.search(r"universal_no_entitled_group|no platform in your plan|group_not_allowed|not allowed", haystack):
            return "SKIP", "not authorized for platform/group"
        if re.search(r"does not have access to responses api|accessdenied", haystack):
            return "SKIP", "model/protocol not provisioned"
        return "FAIL", "unexpected 403"
    if code == 429:
        if re.search(r"no available accounts|available accounts exhausted", haystack):
            return "SKIP", "empty schedulable pool"
        return "SKIP", "upstream throttle/transient"
    if code in {400, 404}:
        if re.search(
            r"retired|sunset|not[_ ]found|does not exist|invalid model|unknown model|model_not_found|not supported|"
            r"unsupported model|upstream rejected the request|does not support|not a valid|no endpoints|"
            r"invalid[_ ]argument|missing scopes|insufficient permission|not available on the serving account|"
            r"channel_type|requested capability|does not have access to responses api|only support stream mode|"
            r"enable_thinking parameter is restricted",
            haystack,
        ):
            return "SKIP", "model/protocol not provisioned"
        return "FAIL", f"unexpected {code}"
    if code in {500, 502, 503, 504}:
        return "SKIP", f"{code} upstream/gateway transient"
    if code == 401:
        return "FAIL", "401 auth failure"
    if code == 0:
        return "SKIP", "timeout/connection interrupted"
    return "FAIL", f"unexpected HTTP {code}"


def display_gate_decision(result: str, note: str) -> tuple[str, str]:
    """Translate a probe verdict into the minimal display action.

    This is intentionally derived from live probe evidence. It is not a fourth
    catalog fact to maintain by hand.
    """
    reason = note.lower()
    if result == "PASS":
        return "keep_displayed", ""
    if result == "FAIL":
        return "hide_or_fix_gateway", note or "gateway failure"
    if "not authorized" in reason:
        return "hide_or_fix_entitlement", note
    if "model/protocol not provisioned" in reason:
        return "hide_or_provision", note
    if "empty schedulable pool" in reason:
        return "reprobe_required", note
    if "throttle" in reason or "transient" in reason or "timeout" in reason or "interrupted" in reason:
        return "reprobe_required", note
    return "hide_or_classify_skip", note or "unclassified SKIP"


def excluded_display_decision(row: ExcludedRow, include_paid: bool) -> tuple[bool, str, str]:
    if row.reason == "not_priced_in_public_catalog":
        return False, "not_displayed", row.reason
    if row.paid and not include_paid:
        return False, "paid_probe_not_in_scope", row.reason
    return True, "hide_or_map_vendor", row.reason


def post_json(base_url: str, path: str, key: str, body: dict[str, Any], timeout: float) -> tuple[int, str]:
    url = base_url.rstrip("/") + path
    data = json.dumps(body, separators=(",", ":")).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=data,
        method="POST",
        headers={
            "authorization": f"Bearer {key}",
            "anthropic-version": "2023-06-01",
            "content-type": "application/json",
            "user-agent": "tokenkey-ssot-matrix/1",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:  # noqa: S310 operator URL
            return resp.status, resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")
    except Exception as e:  # noqa: BLE001
        return 0, str(e)


def render_path(row: MatrixRow) -> str:
    return row.path.replace("{model}", urllib.parse.quote(row.model, safe=""))


def row_to_record(row: MatrixRow) -> dict[str, Any]:
    return asdict(row)


def probe_matrix_row(args, row: MatrixRow) -> tuple[int, str, str, str]:
    path = render_path(row)
    code, body_text = post_json(args.base_url, path, args.key, compact_body(row.protocol, row.model), args.timeout)
    try:
        body = json.loads(body_text) if body_text else {}
    except json.JSONDecodeError:
        body = {}
    result, note = classify(code, body_text, shape_ok(row.protocol, code, body, body_text))
    return code, result, note, body_text


def cmd_list(args) -> int:
    rows, excluded = load_matrix(args)
    rows = filter_rows(rows, args)
    if args.include_paid is False:
        rows = [r for r in rows if not r.paid]
    if args.limit:
        rows = rows[: args.limit]
    scoped_excluded = filter_excluded(excluded, args)

    if args.format == "json":
        print(json.dumps({"rows": [row_to_record(r) for r in rows], "excluded": [asdict(e) for e in scoped_excluded]}, indent=2, sort_keys=True))
        return 0
    print("platform\tvendor\tmodel\tmodality\tprotocol\tmethod\tpath\tpaid\tnote")
    for r in rows:
        print(f"{r.platform}\t{r.vendor}\t{r.model}\t{r.modality}\t{r.protocol}\t{r.method}\t{r.path}\t{int(r.paid)}\t{r.note}")
    if args.show_excluded and scoped_excluded:
        print("\n# excluded")
        print("vendor\tmodel\treason")
        for e in scoped_excluded:
            print(f"{e.vendor}\t{e.model}\t{e.reason}")
    return 0


def cmd_run(args) -> int:
    key = args.key or os.environ.get("TK_FULLTEST_KEY", "")
    if not key:
        print("ERROR: TK_FULLTEST_KEY is required for run", file=sys.stderr)
        return 2
    args.key = key
    rows, excluded = load_matrix(args)
    rows = filter_rows(rows, args)
    if not args.include_paid:
        rows = [r for r in rows if not r.paid]
    if args.limit:
        rows = rows[: args.limit]

    pass_count = skip_count = fail_count = 0
    print("platform\tmodality\tprotocol\tmodel\thttp\tresult\tnote")
    for row in rows:
        code, result, note, _body_text = probe_matrix_row(args, row)
        if result == "PASS":
            pass_count += 1
        elif result == "SKIP":
            skip_count += 1
        else:
            fail_count += 1
        print(f"{row.platform}\t{row.modality}\t{row.protocol}\t{row.model}\t{code}\t{result}\t{note}")
    print(f"PASS={pass_count} SKIP={skip_count} FAIL={fail_count} EXCLUDED={len(filter_excluded(excluded, args))}")
    return 1 if fail_count else 0


def cmd_gate(args) -> int:
    key = args.key or os.environ.get("TK_FULLTEST_KEY", "")
    if not key:
        print("ERROR: TK_FULLTEST_KEY is required for gate", file=sys.stderr)
        return 2
    args.key = key
    rows, excluded = load_matrix(args)
    rows = filter_rows(rows, args)
    if not args.include_paid:
        rows = [r for r in rows if not r.paid]
    if getattr(args, "deploy_canary", False):
        rows = select_deploy_canary_rows(rows)
    if args.limit:
        rows = rows[: args.limit]
    missing_required = required_row_misses(args, rows)

    skip_keys: set[tuple[str, str]] = set()
    skip_file = getattr(args, "skip_recent_file", "") or os.environ.get("TK_SSOT_SKIP_RECENT_FILE", "")
    if skip_file:
        skip_keys = load_skip_keys(
            skip_file,
            min_count=int(os.environ.get("TK_SSOT_SKIP_MIN_COUNT", "1")),
        )

    scoped_excluded = filter_excluded(excluded, args)
    no_rows_count = 1 if not rows and not scoped_excluded else 0
    keep_count = block_count = reprobe_count = fail_count = excluded_block_count = log_skip_count = 0
    print("platform\tmodality\tprotocol\tmodel\thttp\tresult\tdisplay_gate\taction")
    if no_rows_count:
        print("n/a\tn/a\tn/a\tn/a\t0\tSKIP\tnot_in_public_pricing_scope\tno displayed+priced matrix rows matched")
    for row in rows:
        if skip_keys and (row.model, row.modality) in skip_keys:
            log_skip_count += 1
            keep_count += 1
            print(
                f"{row.platform}\t{row.modality}\t{row.protocol}\t{row.model}\t0\t"
                "LOG_SKIP\tkeep_displayed\trecent_success_24h"
            )
            continue
        code, result, note, _body_text = probe_matrix_row(args, row)
        gate, action = display_gate_decision(result, note)
        if gate == "keep_displayed":
            keep_count += 1
        elif gate == "reprobe_required":
            reprobe_count += 1
        else:
            block_count += 1
        if result == "FAIL":
            fail_count += 1
        print(f"{row.platform}\t{row.modality}\t{row.protocol}\t{row.model}\t{code}\t{result}\t{gate}\t{action}")

    scoped_excluded = []
    for excluded_row in filter_excluded(excluded, args):
        blocks, gate, action = excluded_display_decision(excluded_row, args.include_paid)
        if blocks:
            excluded_block_count += 1
        if blocks or args.show_nonblocking_excluded:
            scoped_excluded.append((excluded_row, gate, action))
    if args.show_excluded and scoped_excluded:
        if scoped_excluded:
            print("\n# excluded")
            print("vendor\tmodality\tmodel\tpaid\tdisplay_gate\taction")
            for excluded_row, gate, action in scoped_excluded:
                print(f"{excluded_row.vendor}\t{excluded_row.modality}\t{excluded_row.model}\t{int(excluded_row.paid)}\t{gate}\t{action}")

    print(
        "DISPLAY_KEEP="
        f"{keep_count} DISPLAY_BLOCK={block_count + excluded_block_count} "
        f"REPROBE_REQUIRED={reprobe_count} FAIL={fail_count} "
        f"EXCLUDED_BLOCK={excluded_block_count} NO_ROWS={no_rows_count} "
        f"LOG_SKIP={log_skip_count}"
    )
    if missing_required:
        print("REQUIRE_ROWS_MISSING=" + ",".join(missing_required))
        return 1
    if getattr(args, "deploy_closeout", False):
        return 1 if fail_count else 0
    return 1 if (block_count or fail_count or excluded_block_count) else 0


def cmd_selftest(_args) -> int:
    payload = {
        "data": [
            {"vendor": "openai", "model_id": "gpt-5.1", "pricing": {"input_per_1k_tokens": 1}},
            {"vendor": "vertex_ai-language-models", "model_id": "gemini-2.5-pro", "pricing": {"input_per_1k_tokens": 1}},
            {"vendor": "antigravity", "model_id": "gemini-3.5-flash", "pricing": {"input_per_1k_tokens": 1}},
            {"vendor": "xai", "model_id": "grok-code-fast-1", "pricing": {"input_per_1k_tokens": 1}},
            {"vendor": "dashscope", "model_id": "qwen3-8b", "pricing": {"input_per_1k_tokens": 1}},
            {"vendor": "volcengine", "model_id": "doubao-seedream-5-0-260128", "pricing": {"billing_mode": "image", "output_cost_per_image": 0.03}},
            {"vendor": "volcengine", "model_id": "doubao-seedance-2-0-260128", "pricing": {"billing_mode": "video", "output_cost_per_second": 0.3}},
            {"vendor": "antigravity", "model_id": "gemini-3-pro-image", "pricing": {"billing_mode": "image", "output_cost_per_image": 0.1}},
            {"vendor": "vertex_ai-embedding-models", "model_id": "gemini-embedding-001", "pricing": {"input_per_1k_tokens": 0.1}},
            {"vendor": "bedrock", "model_id": "bedrock-x", "pricing": {"input_per_1k_tokens": 0.1}},
            {"vendor": "openai", "model_id": "free-x", "pricing": {}},
        ]
    }
    rows, excluded = rows_from_public_catalog(payload, "fixture")
    got = {(r.platform, r.model, r.protocol, r.path, r.paid) for r in rows}
    assert ("openai", "gpt-5.1", "chat", "/v1/chat/completions", False) in got
    assert ("gemini", "gemini-2.5-pro", "gemini_generate", "/v1beta/models/{model}:generateContent", False) not in got
    assert ("antigravity", "gemini-3.5-flash", "gemini_generate", "/v1beta/models/{model}:generateContent", False) in got
    assert ("antigravity", "gemini-3.5-flash", "chat", "/v1/chat/completions", False) in got
    assert ("antigravity", "gemini-3.5-flash", "responses", "/v1/responses", False) in got
    assert ("grok", "grok-code-fast-1", "messages", "/v1/messages", False) in got
    assert ("newapi", "qwen3-8b", "responses", "/v1/responses", False) in got
    assert ("newapi", "doubao-seedream-5-0-260128", "image", "/v1/images/generations", True) in got
    assert ("newapi", "doubao-seedance-2-0-260128", "video", "/v1/video/generations", True) in got
    assert ("antigravity", "gemini-3-pro-image", "chat_image", "/v1/chat/completions", True) in got
    assert any(e.model == "gemini-embedding-001" and e.reason == "vendor_not_mapped_to_universal_platform" for e in excluded)
    assert any(e.model == "bedrock-x" and e.reason == "vendor_not_mapped_to_universal_platform" for e in excluded)
    assert any(e.model == "free-x" and e.reason == "not_priced_in_public_catalog" for e in excluded)
    assert compact_body("chat", "qwen3-8b")["enable_thinking"] is False
    assert compact_body("chat", "qwen3.7-max-preview")["stream"] is True
    assert compact_body("chat", "qwen3.7-max-preview")["enable_thinking"] is True
    assert compact_body("chat", "glm-4.5")["stream"] is True
    assert classify(429, '{"error":"No available accounts"}', False)[0] == "SKIP"
    assert classify(403, '{"error":"universal_no_entitled_group"}', False)[0] == "SKIP"
    assert classify(403, '{"error":{"message":"does not have access to responses api"}}', False)[0] == "SKIP"
    assert classify(400, '{"error":{"message":"Upstream rejected the request"}}', False)[0] == "SKIP"
    assert classify(400, '{"error":{"message":"This model only support stream mode"}}', False)[0] == "SKIP"
    assert shape_ok("chat", 200, {}, "data: {\"choices\":[]}\n\ndata: [DONE]\n")
    assert display_gate_decision("PASS", "") == ("keep_displayed", "")
    assert display_gate_decision("SKIP", "model/protocol not provisioned")[0] == "hide_or_provision"
    assert display_gate_decision("SKIP", "empty schedulable pool")[0] == "reprobe_required"
    assert display_gate_decision("SKIP", "upstream throttle/transient")[0] == "reprobe_required"
    assert display_gate_decision("FAIL", "unexpected 400")[0] == "hide_or_fix_gateway"
    mapped_block = ExcludedRow("bedrock", "bedrock-x", "vendor_not_mapped_to_universal_platform", "text", False)
    assert excluded_display_decision(mapped_block, include_paid=False)[0] is True
    not_priced = ExcludedRow("openai", "free-x", "not_priced_in_public_catalog", "text", False)
    assert excluded_display_decision(not_priced, include_paid=False)[0] is False
    assert excluded_matches_protocol(ExcludedRow("vertex", "e", "x", "embeddings", False), "embeddings")
    assert not excluded_matches_protocol(ExcludedRow("vertex", "e", "x", "embeddings", False), "chat")
    require_args = argparse.Namespace(model=["gpt-5.1 missing-model"], require_rows=True)
    assert required_row_misses(require_args, rows) == ["missing-model"]
    soft_args = argparse.Namespace(model=["missing-model"], require_rows=False)
    assert required_row_misses(soft_args, rows) == []
    local_rows, _local_excluded = rows_from_public_catalog(local_pricing_payload(), "local-fixture")
    local_require = argparse.Namespace(model=["gpt-5.4"], require_rows=True)
    assert not required_row_misses(local_require, local_rows)
    assert "kiro" in PLATFORM_CHOICES, "deploy-stage0 sharded gate includes kiro; argparse must accept it"
    canary = select_deploy_canary_rows(rows)
    assert canary, "deploy-canary must select at least one row from fixture catalog"
    assert all(r.protocol in DEPLOY_CANARY_PROTOCOLS.get(r.platform, []) for r in canary)
    skip_keys = parse_recent_success_tsv("gpt-5.1\ttext\t10\n", min_count=1)
    assert ("gpt-5.1", "text") in skip_keys
    print("gateway_model_ssot_matrix selftest: PASS")
    return 0


def cmd_platforms(_args) -> int:
    for platform in PLATFORM_CHOICES:
        print(platform)
    return 0


def add_source_args(p: argparse.ArgumentParser) -> None:
    p.add_argument("--source", choices=["live-pricing", "local-pricing"], default="live-pricing")
    p.add_argument("--base-url", default=DEFAULT_BASE_URL)
    p.add_argument("--timeout", type=float, default=DEFAULT_TIMEOUT)
    p.add_argument("--only-platform", choices=PLATFORM_CHOICES)
    p.add_argument(
        "--only-protocol",
        choices=["messages", "count_tokens", "chat", "responses", "gemini_generate", "chat_image", "image", "video", "embeddings"],
    )
    p.add_argument("--model", action="append", default=[], help="model id to include; may be repeated or comma/space separated")
    p.add_argument("--limit", type=int, default=0)
    p.add_argument("--shard-index", type=int, default=0, help="0-based shard index for long gate runs")
    p.add_argument("--shard-count", type=int, default=0, help="split filtered rows into N sequential shards")


def main() -> int:
    try:
        sys.stdout.reconfigure(line_buffering=True)
    except AttributeError:
        pass

    parser = argparse.ArgumentParser(description="TokenKey universal model/protocol matrix from the public pricing SSOT projection")
    sub = parser.add_subparsers(dest="cmd", required=True)

    list_p = sub.add_parser("list", help="print the derived matrix without sending model requests")
    add_source_args(list_p)
    list_p.add_argument("--format", choices=["tsv", "json"], default="tsv")
    list_p.add_argument("--include-paid", action="store_true", help="include image/video rows in the listing")
    list_p.add_argument("--show-excluded", action="store_true", help="show priced catalog rows excluded from the universal matrix")
    list_p.set_defaults(func=cmd_list)

    run_p = sub.add_parser("run", help="probe universal key rows derived from the matrix")
    add_source_args(run_p)
    run_p.add_argument("--key", default="")
    run_p.add_argument("--include-paid", action="store_true", help="actually send image/video requests")
    run_p.set_defaults(func=cmd_run)

    gate_p = sub.add_parser("gate", help="fail unless displayed+priced rows in scope are live-supported")
    add_source_args(gate_p)
    gate_p.add_argument("--key", default="")
    gate_p.add_argument("--include-paid", action="store_true", help="include image/video rows in the display gate")
    gate_p.add_argument("--show-excluded", action="store_true", help="show public-pricing rows that cannot map to a universal endpoint")
    gate_p.add_argument("--show-nonblocking-excluded", action="store_true", help="also show excluded rows outside the current gate scope")
    gate_p.add_argument(
        "--deploy-closeout",
        action="store_true",
        help="deploy/release closeout: fail only on gateway FAIL rows, not DISPLAY_BLOCK backlog",
    )
    gate_p.add_argument(
        "--deploy-canary",
        action="store_true",
        help="deploy closeout canary: probe one golden path per platform instead of the full matrix",
    )
    gate_p.add_argument(
        "--skip-recent-file",
        default="",
        help="TSV of model/modality/count rows to skip (normally serving in recent usage_logs)",
    )
    gate_p.add_argument(
        "--require-rows",
        action="store_true",
        help="fail when explicitly requested models do not map to display-gate rows",
    )
    gate_p.set_defaults(func=cmd_gate)

    selftest_p = sub.add_parser("selftest", help="run offline unit tests")
    selftest_p.set_defaults(func=cmd_selftest)

    platforms_p = sub.add_parser("platforms", help="print platform shards accepted by --only-platform")
    platforms_p.set_defaults(func=cmd_platforms)

    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
