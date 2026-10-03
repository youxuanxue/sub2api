#!/usr/bin/env python3
"""Discover upstream model IDs and compare them to TokenKey multi-channel SSOT.

Approved contract:
- Discovery uses **public** channels only by default:
  1) committed ``model-release-public-catalog.json`` seeds (works offline / geo-blocked),
  2) live docs/changelog/blog fetches when reachable,
  3) optional vendor API keys as enrichment only — never required.
- "Served" = explicit requestable key on any supply surface AND a price owner.
  Wildcard mappings (e.g. cloudwise ``claude-*``) do not count.
- Issue candidates are only ``missing`` / ``unpriced`` / ``narrow`` that also pass
  the current-generation scope filter AND are newly seen vs the watch state.
- Baseline is repo SSOT only; prod live capacity is out of scope for P0.
- Issue sync is opt-in (workflow defaults to dry-run / closed gate).
- Probe / supplier enablement happen after triage — not in this watch.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_REPORT_JSON = REPO_ROOT / ".cache/model-release-watch/report.json"
DEFAULT_REPORT_MD = REPO_ROOT / ".cache/model-release-watch/report.md"
DEFAULT_STATE = REPO_ROOT / ".cache/model-release-watch/state.json"
PUBLIC_CATALOG = REPO_ROOT / "ops/pricing/model-release-public-catalog.json"
BUNDLE_PATH = REPO_ROOT / "ops/pricing/model-surface-bundle.json"
MANIFEST_PATH = REPO_ROOT / "backend/internal/service/tk_served_models.json"
OVERLAY_PATH = REPO_ROOT / "backend/internal/service/tk_pricing_overlay.json"
CATALOG_GO = REPO_ROOT / "backend/internal/service/pricing_catalog_supported_models_tk.go"
STATE_VERSION = 1

MODEL_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$")
WILDCARD_RE = re.compile(r"[*]")

# Primary supply surfaces for "full" vs "narrow" coverage.
PRIMARY_SURFACES = frozenset({
    "platform:anthropic",
    "platform:openai",
    "platform:gemini",
    "platform:antigravity",
    "platform:grok",
    "platform:kiro",
    "platform:anthropic_tokensea_relay",
    "platform:openai_tokensea_relay",
    "platform:openai_cloudwise_relay",
    "allowlist:anthropic",
    "allowlist:openai",
    "allowlist:gemini",
    "allowlist:antigravity",
    "allowlist:grok",
    "catalog:openai_tokensea_relay",
    "catalog:anthropic_tokensea_relay",
    "manifest",
    "override:newapi:14:https://agentn.global.api5.cursor.sh",
})

# Vendor → accepted upstream id prefixes (chat/coding/agent main surface).
VENDOR_PREFIXES: dict[str, tuple[str, ...]] = {
    "openai": ("gpt-", "o1", "o3", "o4", "codex-", "chatgpt-"),
    "anthropic": ("claude-",),
    # Google: text/pro/image only. veo/embedding scraped noise stays out via scope.
    "google": ("gemini-", "nano-"),
    "deepseek": ("deepseek-",),
    "glm": ("glm-",),
    "kimi": ("kimi-", "moonshot-"),
    "doubao": ("doubao-",),
}

# Peripheral / non-chat denylist substrings (matched against normalized id).
DENYLIST_SUBSTRINGS = (
    "-live",
    "-tts",
    "lyria",
    "robotics",
    "transcribe",
    "deep-research",
    "antigravity-preview",
    "imagen-",
    "computer-use",
    "native-audio",
    "vision-exp",
    "-asr-",
    "glm-asr",
    "glm-ocr",
    "glm-image",
    "flashx",
    "embedding",
    "veo-",
)

# Google image marketing aliases → same family as gemini-3.1-flash-image / Nano Banana Pro.
GOOGLE_IMAGE_ALIASES_EXACT = frozenset({"nano-2"})
GOOGLE_IMAGE_ALIAS_PREFIXES = ("nano-banana-2", "nano-banana-pro")

ACTIONABLE_STATUSES = frozenset({"missing", "unpriced", "narrow"})


def now_utc() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def http_get_urllib(url: str, *, headers: dict[str, str] | None = None, timeout: float = 15.0) -> str:
    req_headers = {
        "User-Agent": "TokenKey-model-release-watch",
        "Accept": "text/html,application/json",
    }
    if headers:
        req_headers.update(headers)
    req = urllib.request.Request(url, headers=req_headers)
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.read().decode("utf-8", errors="replace")


def http_get_curl(url: str, *, timeout: float = 15.0) -> str:
    """Public-page fallback when Python SSL stacks fail on some hosts."""
    import subprocess

    proc = subprocess.run(
        [
            "curl", "-fsSL",
            "-A", "TokenKey-model-release-watch",
            "--connect-timeout", "10",
            "--max-time", str(int(timeout)),
            url,
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        err = (proc.stderr or proc.stdout or f"curl exit {proc.returncode}").strip()
        raise RuntimeError(err[:300])
    return proc.stdout


def http_get(url: str, *, headers: dict[str, str] | None = None, timeout: float = 15.0) -> str:
    # Prefer curl for public pages: some local Python SSL stacks fail against
    # vendor docs hosts even when curl works (and vice versa).
    if not headers:
        try:
            return http_get_curl(url, timeout=timeout)
        except Exception as curl_exc:  # noqa: BLE001
            try:
                return http_get_urllib(url, timeout=timeout)
            except Exception as urllib_exc:  # noqa: BLE001
                raise RuntimeError(f"curl: {curl_exc}; urllib: {urllib_exc}") from urllib_exc
    return http_get_urllib(url, headers=headers, timeout=timeout)


def http_json(url: str, *, headers: dict[str, str] | None = None) -> Any:
    return json.loads(http_get(url, headers=headers))


def fetch_public_pages(
    vendor: str,
    urls: list[str],
    prefixes: tuple[str, ...],
    *,
    keep: Callable[[str], bool] | None = None,
) -> tuple[list[UpstreamModel], list[str]]:
    """Discover model ids from public docs/changelogs — no API key required."""
    errors: list[str] = []
    ids: set[str] = set()
    source_url = urls[0] if urls else ""
    for url in urls:
        try:
            text = http_get(url)
        except Exception as exc:  # noqa: BLE001
            errors.append(f"{vendor} public {url}: {exc}")
            continue
        got = _extract_ids(text, prefixes, vendor=vendor)
        if keep is not None:
            got = {mid for mid in got if keep(mid)}
        if got:
            ids.update(got)
            source_url = url
            break
    if not ids:
        return [], errors or [f"{vendor}: no ids from public sources"]
    return [UpstreamModel(vendor, mid, source_url) for mid in sorted(ids)], errors


def is_wildcard(model_id: str) -> bool:
    return bool(WILDCARD_RE.search(model_id))


def normalize_model_id(raw: str) -> str:
    return (raw or "").strip()


def denylisted(model_id: str) -> bool:
    low = model_id.lower()
    return any(token in low for token in DENYLIST_SUBSTRINGS)


def vendor_for_model(model_id: str) -> str | None:
    low = model_id.lower()
    for vendor, prefixes in VENDOR_PREFIXES.items():
        if any(low.startswith(p) for p in prefixes):
            return vendor
    return None


def _gemini_major_minor(model_id: str) -> tuple[int, int] | None:
    """Parse ``gemini-<major>[.<minor>]-…``; bare ``gemini-3`` returns (3, 0)."""
    match = re.match(r"^gemini-(\d+)(?:\.(\d+))?(?:-|$)", model_id)
    if not match:
        return None
    return int(match.group(1)), int(match.group(2) or 0)


def _version_ge(version: tuple[int, int], floor: tuple[int, int]) -> bool:
    return version >= floor


def _is_google_image_alias(model_id: str) -> bool:
    if model_id in GOOGLE_IMAGE_ALIASES_EXACT:
        return True
    return any(model_id == p or model_id.startswith(f"{p}-") or model_id.startswith(p)
               for p in GOOGLE_IMAGE_ALIAS_PREFIXES)


def _is_google_pro_id(model_id: str) -> bool:
    # gemini-3.1-pro / -preview / -high / -low; not *-pro-image (image path) or pro-agent.
    if "pro-agent" in model_id or "-pro-image" in model_id or model_id.endswith("-pro-image"):
        return False
    return bool(re.search(r"(?:^|-)pro(?:-|$)", model_id))


def google_in_candidate_scope(model_id: str) -> bool:
    """Google watch scope (operator-locked, 2026-10-03 converge):

    1. Text Flash: ``gemini-3.8-flash`` and newer Flash SKUs
    2. Pro: newer than the frozen ``gemini-3.1-pro*`` line (``>= 3.2`` Pro).
       ``gemini-3.1-pro`` / ``-preview`` / ``-high`` / ``-low`` stay out — not a
       public wire (structural-deadlist / product: do not expand).
    3. Image: ``gemini-3.1-flash-image`` and newer non-lite image SKUs, plus
       Nano Banana aliases / Nano Banana Pro. Lite image (``*-flash-lite-image``)
       is intentionally not expanded.
    """
    mid = model_id
    if re.fullmatch(r"gemini-\d+(\.\d+)?", mid):
        return False  # bare gemini-3 / gemini-3.5 prose fragments

    if mid.startswith("nano-"):
        return _is_google_image_alias(mid)

    if not mid.startswith("gemini-"):
        return False

    version = _gemini_major_minor(mid)
    if version is None:
        return False

    # Image family (Nano Banana / Flash Image / Pro Image).
    if "-image" in mid:
        if "lite-image" in mid or mid.endswith("-lite-image"):
            return False  # Nano Banana 2 Lite — do not expand unless product asks
        if mid.startswith("gemini-3-pro-image"):
            return True  # Nano Banana Pro (higher-tier sibling)
        return _version_ge(version, (3, 1))

    # High-capability Pro text — freeze entire 3.1-pro* public non-listing.
    if _is_google_pro_id(mid):
        return _version_ge(version, (3, 2))

    # Text Flash mainline: 3.8-flash and above (incl. flash-lite at that floor).
    # Pre-3.8 Flash (incl. gemini-3.1-flash-lite text) stays out.
    if "flash" in mid:
        return _version_ge(version, (3, 8))

    # Named next-gen text (e.g. gemini-4-argon) without flash/pro/image suffix.
    return version[0] >= 4


def in_candidate_scope(vendor: str, model_id: str) -> bool:
    """Current-generation gate: ignore legacy / specialty / doc fragments."""
    mid = normalize_model_id(model_id).lower()
    if not mid or denylisted(mid):
        return False

    if vendor == "openai":
        return mid.startswith("gpt-6") or mid.startswith("gpt-image-2")
    if vendor == "anthropic":
        return bool(re.match(r"claude-(opus|sonnet|haiku|fable|mythos)-5", mid))
    if vendor == "google":
        return google_in_candidate_scope(mid)
    if vendor == "deepseek":
        return bool(re.match(r"deepseek-(flash|v4)", mid))
    if vendor == "glm":
        # Mainline GLM-5.x only (flash ok; flashx/asr/ocr denied above).
        return bool(re.match(r"glm-5(\.|$|-)", mid)) and not re.search(r"glm-5\.\d+v", mid)
    if vendor == "kimi":
        return mid.startswith("kimi-k3")
    if vendor == "doubao":
        return any(
            token in mid
            for token in ("seed-2.1", "seed-2-1", "seedance-2", "seedream-5", "seed-evolving")
        )
    return False


def state_key(vendor: str, model_id: str) -> str:
    return f"{vendor}/{model_id}"


def load_state(path: Path) -> dict[str, Any]:
    if not path.is_file():
        return {"version": STATE_VERSION, "seen": {}}
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {"version": STATE_VERSION, "seen": {}}
    if not isinstance(data, dict):
        return {"version": STATE_VERSION, "seen": {}}
    seen = data.get("seen")
    if not isinstance(seen, dict):
        seen = {}
    return {"version": STATE_VERSION, "seen": seen}


def save_state(path: Path, state: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(state, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


@dataclass
class SupplyHit:
    surface: str
    mapped_to: str


@dataclass
class TokenKeyBaseline:
    explicit_keys: dict[str, list[SupplyHit]] = field(default_factory=dict)
    priced_owners: set[str] = field(default_factory=set)
    aliases: dict[str, str] = field(default_factory=dict)

    def surfaces_for(self, model_id: str) -> list[SupplyHit]:
        return list(self.explicit_keys.get(model_id) or [])

    def is_priced(self, model_id: str) -> bool:
        if model_id in self.priced_owners:
            return True
        owner = self.aliases.get(model_id)
        return bool(owner and owner in self.priced_owners)


@dataclass
class UpstreamModel:
    vendor: str
    model_id: str
    source_url: str
    watch_only: bool = False
    notes: str = ""


@dataclass
class Finding:
    vendor: str
    model_id: str
    status: str
    surfaces: list[str]
    priced: bool
    primary_coverage: bool
    source_url: str
    notes: str = ""
    issue_suppressed: bool = False
    in_scope: bool = True
    is_new: bool = True
    alert_all: bool = False

    @property
    def actionable(self) -> bool:
        novelty_ok = self.is_new or self.alert_all
        return (
            self.status in ACTIONABLE_STATUSES
            and self.in_scope
            and novelty_ok
            and not self.issue_suppressed
        )


def _add_key(baseline: TokenKeyBaseline, model_id: str, surface: str, mapped_to: str) -> None:
    mid = normalize_model_id(model_id)
    if not mid or is_wildcard(mid) or not MODEL_ID_RE.match(mid):
        return
    baseline.explicit_keys.setdefault(mid, []).append(SupplyHit(surface=surface, mapped_to=mapped_to or mid))


def _parse_allowlist_maps(go_text: str) -> dict[str, set[str]]:
    try:
        from servable_allowlist import parse_allowlist_maps  # type: ignore
    except ImportError:  # pragma: no cover - package import path
        from ops.pricing.servable_allowlist import parse_allowlist_maps  # type: ignore
    return parse_allowlist_maps(go_text)


def load_baseline(
    *,
    bundle_path: Path = BUNDLE_PATH,
    manifest_path: Path = MANIFEST_PATH,
    overlay_path: Path = OVERLAY_PATH,
    catalog_go: Path = CATALOG_GO,
) -> TokenKeyBaseline:
    baseline = TokenKeyBaseline()

    bundle = json.loads(bundle_path.read_text(encoding="utf-8"))
    amap = bundle.get("account_model_mapping") or {}
    for platform, mapping in (amap.get("platforms") or {}).items():
        if not isinstance(mapping, dict):
            continue
        for key, value in mapping.items():
            _add_key(baseline, str(key), f"platform:{platform}", str(value))
    for override in amap.get("account_overrides") or []:
        if not isinstance(override, dict):
            continue
        mapping = override.get("model_mapping") or {}
        if not isinstance(mapping, dict):
            continue
        platform = override.get("platform")
        channel_type = override.get("channel_type")
        base_url = override.get("base_url") or ""
        surface = f"override:{platform}:{channel_type}:{base_url}"
        for key, value in mapping.items():
            _add_key(baseline, str(key), surface, str(value))
    for channel_type, mapping in (amap.get("newapi_channel_types") or {}).items():
        if not isinstance(mapping, dict):
            continue
        for key, value in mapping.items():
            _add_key(baseline, str(key), f"newapi_channel:{channel_type}", str(value))

    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    for model_id, entry in (manifest.get("entries") or {}).items():
        _add_key(baseline, str(model_id), "manifest", str(model_id))
        if isinstance(entry, dict) and entry.get("price_owner"):
            baseline.aliases[str(model_id)] = str(entry["price_owner"])

    overlay = json.loads(overlay_path.read_text(encoding="utf-8"))
    for key, value in overlay.items():
        if key.startswith("_"):
            continue
        if isinstance(value, dict):
            baseline.priced_owners.add(str(key))
    aliases = overlay.get("_aliases") or {}
    if isinstance(aliases, dict):
        for src, dst in aliases.items():
            baseline.aliases[str(src)] = str(dst)

    go_text = catalog_go.read_text(encoding="utf-8")
    for platform, models in _parse_allowlist_maps(go_text).items():
        for model_id in models:
            _add_key(baseline, model_id, f"allowlist:{platform}", model_id)

    for var_name, surface in (
        ("supportedOpenAITokenseaRelayCatalogModels", "catalog:openai_tokensea_relay"),
        ("supportedAnthropicTokenseaRelayCatalogModels", "catalog:anthropic_tokensea_relay"),
    ):
        match = re.search(
            rf"{re.escape(var_name)}\s*=\s*map\[string\]struct\{{(.*?)}}",
            go_text,
            re.S,
        )
        if not match:
            continue
        for model_id in re.findall(r'"([^"]+)"\s*:\s*\{\}', match.group(1)):
            _add_key(baseline, model_id, surface, model_id)

    return baseline


def classify(
    upstream: UpstreamModel,
    baseline: TokenKeyBaseline,
) -> Finding:
    hits = baseline.surfaces_for(upstream.model_id)
    surfaces = sorted({h.surface for h in hits})
    priced = baseline.is_priced(upstream.model_id)
    primary = any(s in PRIMARY_SURFACES or s.startswith("override:newapi:14:") for s in surfaces)
    scoped = in_candidate_scope(upstream.vendor, upstream.model_id)

    if upstream.watch_only:
        return Finding(
            vendor=upstream.vendor,
            model_id=upstream.model_id,
            status="watch_only",
            surfaces=surfaces,
            priced=priced,
            primary_coverage=primary,
            source_url=upstream.source_url,
            notes=upstream.notes,
            issue_suppressed=True,
            in_scope=False,
        )

    if denylisted(upstream.model_id) or not scoped:
        return Finding(
            vendor=upstream.vendor,
            model_id=upstream.model_id,
            status="out_of_scope",
            surfaces=surfaces,
            priced=priced,
            primary_coverage=primary,
            source_url=upstream.source_url,
            notes=upstream.notes or "outside current-generation candidate scope",
            issue_suppressed=True,
            in_scope=False,
        )

    if not surfaces:
        return Finding(
            vendor=upstream.vendor,
            model_id=upstream.model_id,
            status="missing",
            surfaces=[],
            priced=priced,
            primary_coverage=False,
            source_url=upstream.source_url,
            notes=upstream.notes,
            in_scope=True,
        )

    if not priced:
        return Finding(
            vendor=upstream.vendor,
            model_id=upstream.model_id,
            status="unpriced",
            surfaces=surfaces,
            priced=False,
            primary_coverage=primary,
            source_url=upstream.source_url,
            notes=upstream.notes,
            in_scope=True,
        )

    if not primary:
        return Finding(
            vendor=upstream.vendor,
            model_id=upstream.model_id,
            status="narrow",
            surfaces=surfaces,
            priced=True,
            primary_coverage=False,
            source_url=upstream.source_url,
            notes=upstream.notes or "explicit supply exists only on non-primary surfaces",
            in_scope=True,
        )

    return Finding(
        vendor=upstream.vendor,
        model_id=upstream.model_id,
        status="served",
        surfaces=surfaces,
        priced=True,
        primary_coverage=True,
        source_url=upstream.source_url,
        notes=upstream.notes,
        issue_suppressed=True,
        in_scope=True,
    )


def _looks_like_api_model_id(model_id: str, prefixes: tuple[str, ...]) -> bool:
    """Reject HTML/CSS/doc-path noise; keep plausible vendor API ids only."""
    mid = normalize_model_id(model_id)
    if not mid or is_wildcard(mid) or denylisted(mid):
        return False
    low = mid.lower()
    if not any(low.startswith(p) for p in prefixes):
        return False
    # Paths, assets, and prose fragments are not API model ids.
    if "/" in mid or "\\" in mid:
        return False
    if any(low.endswith(ext) for ext in (".png", ".jpg", ".jpeg", ".svg", ".gif", ".webp", ".css", ".js")):
        return False
    if any(token in low for token in (
        "quickstart", "docs", "pricing", "overview", "platform", "awesome-",
        "social-card", "icon-", "card-", "header-", "hovercard", "-cta",
        "elevation-", "bulletpoints", "with-links", "models",
    )):
        return False
    # Require a digit somewhere after the vendor prefix (versioned API ids).
    if not re.search(r"\d", mid):
        return False
    # Disallow whitespace and uppercase display names with spaces; allow
    # hyphen/dot/_ API ids. Normalize later for GLM display casing.
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{1,127}", mid):
        return False
    # Drop slash-like compound leftovers and CSS-ish multi-token noise.
    if mid.count("-") > 8:
        return False
    return True


def _normalize_extracted_id(model_id: str, vendor: str) -> str:
    mid = normalize_model_id(model_id)
    if vendor in {"glm", "deepseek", "kimi", "doubao", "openai", "anthropic", "google"}:
        # TokenKey SSOT keys are lowercase for these families.
        return mid.lower()
    return mid


def _extract_ids(text: str, prefixes: tuple[str, ...], *, vendor: str) -> set[str]:
    found: set[str] = set()
    candidates: set[str] = set()

    # Prefer explicit code/markup and /models/<id> path tails.
    candidates.update(re.findall(r"[`\"']([A-Za-z0-9][A-Za-z0-9._-]{1,127})[`\"']", text))
    candidates.update(re.findall(r"<code[^>]*>\s*([A-Za-z0-9][A-Za-z0-9._-]{1,127})\s*</code>", text, re.I))
    candidates.update(re.findall(r"/models/([A-Za-z0-9][A-Za-z0-9._-]{1,127})", text))
    for prefix in prefixes:
        candidates.update(re.findall(rf"\b({re.escape(prefix)}[A-Za-z0-9._-]{{1,120}})\b", text, re.I))

    for raw in candidates:
        mid = _normalize_extracted_id(raw, vendor)
        if _looks_like_api_model_id(mid, prefixes):
            found.add(mid)
    return found


def _optional_api_models(
    vendor: str,
    url: str,
    *,
    headers: dict[str, str],
    id_field: str = "id",
) -> tuple[list[UpstreamModel], list[str]]:
    """Optional enrichment only — discovery must work without this."""
    try:
        data = http_json(url, headers=headers)
    except Exception as exc:  # noqa: BLE001
        return [], [f"{vendor} api optional: {exc}"]
    models: list[UpstreamModel] = []
    for row in data.get("data") or []:
        mid = _normalize_extracted_id(str(row.get(id_field) or ""), vendor)
        if vendor_for_model(mid) == vendor and not denylisted(mid):
            if vendor == "anthropic" and re.search(r"20\d{6}", mid):
                continue
            models.append(UpstreamModel(vendor, mid, url))
    return models, []


def fetch_anthropic() -> tuple[list[UpstreamModel], list[str]]:
    models, errors = fetch_public_pages(
        "anthropic",
        [
            "https://docs.anthropic.com/en/docs/about-claude/models/overview",
            "https://docs.anthropic.com/en/docs/about-claude/models",
            "https://www.anthropic.com/claude-sonnet-5-5",
            "https://www.anthropic.com/claude-opus-5-5",
        ],
        VENDOR_PREFIXES["anthropic"],
        keep=lambda mid: not re.search(r"20\d{6}", mid),
    )
    # Optional API enrichment — never required.
    key = os.environ.get("ANTHROPIC_API_KEY") or os.environ.get("MODEL_RELEASE_ANTHROPIC_API_KEY")
    if key:
        api_models, api_errs = _optional_api_models(
            "anthropic",
            "https://api.anthropic.com/v1/models",
            headers={"x-api-key": key, "anthropic-version": "2023-06-01"},
        )
        errors.extend(api_errs)
        by_id = {m.model_id: m for m in models}
        by_id.update({m.model_id: m for m in api_models})
        models = [by_id[k] for k in sorted(by_id)]
    return models, errors


def fetch_openai() -> tuple[list[UpstreamModel], list[str]]:
    models, errors = fetch_public_pages(
        "openai",
        [
            "https://platform.openai.com/docs/models",
            "https://openai.com/index/gpt-6-astra/",
            "https://openai.com/index/introducing-gpt-6-sol-and-luna/",
            "https://openai.com/index/introducing-gpt-6-1-sol",
            "https://openai.com/products/release-notes/",
        ],
        VENDOR_PREFIXES["openai"],
        keep=lambda mid: mid.startswith("gpt-6") or mid.startswith("gpt-image-2"),
    )
    key = os.environ.get("OPENAI_API_KEY") or os.environ.get("MODEL_RELEASE_OPENAI_API_KEY")
    if key:
        api_models, api_errs = _optional_api_models(
            "openai",
            "https://api.openai.com/v1/models",
            headers={"Authorization": f"Bearer {key}"},
        )
        errors.extend(api_errs)
        by_id = {m.model_id: m for m in models}
        by_id.update({m.model_id: m for m in api_models})
        models = [by_id[k] for k in sorted(by_id)]
    return models, errors


def fetch_google() -> tuple[list[UpstreamModel], list[str]]:
    models, errors = fetch_public_pages(
        "google",
        [
            "https://ai.google.dev/gemini-api/docs/models",
            "https://blog.google/innovation-and-ai/models-and-research/gemini-models/gemini-4-argon/",
        ],
        VENDOR_PREFIXES["google"],
    )
    # Announcement-only Argon when no API id is published yet.
    if not any("argon" in m.model_id for m in models):
        argon_url = (
            "https://blog.google/innovation-and-ai/models-and-research/gemini-models/gemini-4-argon/"
        )
        try:
            text = http_get(argon_url)
        except Exception as exc:  # noqa: BLE001
            errors.append(f"google argon blog: {exc}")
            text = ""
        if "argon" in text.lower():
            models.append(
                UpstreamModel(
                    "google",
                    "gemini-4-argon",
                    argon_url,
                    watch_only=True,
                    notes="announced; no public API model id confirmed",
                )
            )
    return models, errors


def fetch_deepseek() -> tuple[list[UpstreamModel], list[str]]:
    return fetch_public_pages(
        "deepseek",
        [
            "https://api-docs.deepseek.com/quick_start/pricing",
            "https://api-docs.deepseek.com/updates",
            "https://api-docs.deepseek.com/news/news260910",
        ],
        VENDOR_PREFIXES["deepseek"],
    )


def fetch_glm() -> tuple[list[UpstreamModel], list[str]]:
    return fetch_public_pages(
        "glm",
        [
            "https://docs.z.ai/guides/overview/pricing",
            "https://docs.z.ai/guides/vlm/glm-5.3-flash",
            "https://z.ai/blog/glm-5.3-flash",
        ],
        VENDOR_PREFIXES["glm"],
    )


def fetch_kimi() -> tuple[list[UpstreamModel], list[str]]:
    models, errors = fetch_public_pages(
        "kimi",
        [
            "https://platform.moonshot.cn/docs/pricing/chat",
            "https://platform.kimi.ai/docs/pricing/chat",
            "https://moonshotai.github.io/Moonshot-Document/guide/pricing.html",
        ],
        VENDOR_PREFIXES["kimi"],
    )
    key = os.environ.get("MOONSHOT_API_KEY") or os.environ.get("MODEL_RELEASE_MOONSHOT_API_KEY")
    if key:
        api_models, api_errs = _optional_api_models(
            "kimi",
            "https://api.moonshot.ai/v1/models",
            headers={"Authorization": f"Bearer {key}"},
        )
        errors.extend(api_errs)
        by_id = {m.model_id: m for m in models}
        by_id.update({m.model_id: m for m in api_models})
        models = [by_id[k] for k in sorted(by_id)]
    return models, errors


def fetch_doubao() -> tuple[list[UpstreamModel], list[str]]:
    models, errors = fetch_public_pages(
        "doubao",
        [
            "https://www.volcengine.com/docs/82379/1330310",
            "https://www.volcengine.com/docs/82379/1399008",
            "https://seed.bytedance.com/zh/blog/seed2-1-officially-released-advancing-ai-productivity",
        ],
        VENDOR_PREFIXES["doubao"],
    )
    # Public launch coverage often names Seed 2.1 Pro without a stable HTML id table.
    # Keep an explicit watch candidate when docs pages mention the family but omit the
    # dated API id — operators still triage supplier enablement after the issue.
    joined_notes = " ".join(errors).lower()
    if not any("seed-2.1" in m.model_id or "seed-2-1" in m.model_id for m in models):
        for url in (
            "https://www.volcengine.com/docs/82379/1330310",
            "https://seed.bytedance.com/zh/blog/seed2-1-officially-released-advancing-ai-productivity",
        ):
            try:
                text = http_get(url).lower()
            except Exception as exc:  # noqa: BLE001
                errors.append(f"doubao probe {url}: {exc}")
                continue
            if "seed-2.1" in text or "seed 2.1" in text or "doubao-seed-2.1" in text:
                models.append(
                    UpstreamModel(
                        "doubao",
                        "doubao-seed-2.1-pro",
                        url,
                        notes="public docs/blog mention Seed 2.1 Pro; confirm exact Ark model id in triage",
                    )
                )
                break
        if not models and "doubao" in joined_notes:
            pass
    key = os.environ.get("ARK_API_KEY") or os.environ.get("MODEL_RELEASE_ARK_API_KEY")
    if key:
        base = os.environ.get("ARK_BASE_URL") or "https://ark.cn-beijing.volces.com/api/v3"
        api_models, api_errs = _optional_api_models(
            "doubao",
            base.rstrip("/") + "/models",
            headers={"Authorization": f"Bearer {key}"},
        )
        errors.extend(api_errs)
        by_id = {m.model_id: m for m in models}
        by_id.update({m.model_id: m for m in api_models})
        models = [by_id[k] for k in sorted(by_id)]
    return models, errors


FETCHERS: dict[str, Callable[[], tuple[list[UpstreamModel], list[str]]]] = {
    "openai": fetch_openai,
    "anthropic": fetch_anthropic,
    "google": fetch_google,
    "deepseek": fetch_deepseek,
    "glm": fetch_glm,
    "kimi": fetch_kimi,
    "doubao": fetch_doubao,
}


def load_upstream_fixture(path: Path) -> list[UpstreamModel]:
    data = json.loads(path.read_text(encoding="utf-8"))
    models: list[UpstreamModel] = []
    for row in data.get("models") or data:
        models.append(
            UpstreamModel(
                vendor=str(row["vendor"]),
                model_id=normalize_model_id(str(row["model_id"])),
                source_url=str(row.get("source_url") or ""),
                watch_only=bool(row.get("watch_only")),
                notes=str(row.get("notes") or ""),
            )
        )
    return models


def load_public_catalog(path: Path = PUBLIC_CATALOG) -> list[UpstreamModel]:
    """Committed public-channel seeds — works without API keys or geo-unblocked docs."""
    if not path.is_file():
        return []
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return []
    models: list[UpstreamModel] = []
    for row in data.get("models") or []:
        if not isinstance(row, dict):
            continue
        models.append(
            UpstreamModel(
                vendor=str(row["vendor"]),
                model_id=normalize_model_id(str(row["model_id"])),
                source_url=str(row.get("source_url") or str(path)),
                watch_only=bool(row.get("watch_only")),
                notes=str(row.get("notes") or "public catalog seed"),
            )
        )
    return models


def collect_upstream(
    *,
    vendors: list[str] | None = None,
    fixture: Path | None = None,
    public_catalog: Path | None = PUBLIC_CATALOG,
) -> tuple[list[UpstreamModel], list[str]]:
    if fixture is not None:
        return load_upstream_fixture(fixture), []
    selected = vendors or list(FETCHERS)
    models: list[UpstreamModel] = []
    errors: list[str] = []

    # 1) Committed public catalog seeds (always available, no keys).
    if public_catalog is not None:
        for model in load_public_catalog(public_catalog):
            if selected is not None and model.vendor not in selected:
                continue
            models.append(model)

    # 2) Live public docs/changelogs when the network can reach them.
    for vendor in selected:
        fetcher = FETCHERS.get(vendor)
        if fetcher is None:
            errors.append(f"{vendor}: unknown vendor")
            continue
        got, errs = fetcher()
        models.extend(got)
        errors.extend(errs)

    # de-dupe by vendor+id (live fetch wins over seed when both exist)
    uniq: dict[tuple[str, str], UpstreamModel] = {}
    for model in models:
        uniq[(model.vendor, model.model_id)] = model
    return list(uniq.values()), errors


def finding_to_dict(finding: Finding) -> dict[str, Any]:
    row = asdict(finding)
    row["actionable"] = finding.actionable
    return row


def apply_seen_state(
    findings: list[Finding],
    state: dict[str, Any],
    *,
    bootstrap: bool,
    alert_all: bool,
) -> dict[str, Any]:
    """Mark novelty vs prior state; bootstrap seeds state without opening issues."""
    seen: dict[str, Any] = dict(state.get("seen") or {})
    now = now_utc()
    for finding in findings:
        key = state_key(finding.vendor, finding.model_id)
        previously = key in seen
        finding.is_new = not previously
        finding.alert_all = alert_all
        if bootstrap:
            finding.issue_suppressed = True
            if finding.notes:
                finding.notes = finding.notes + "; bootstrap seed (no issues)"
            else:
                finding.notes = "bootstrap seed (no issues)"
        elif not alert_all and not finding.is_new and finding.status in ACTIONABLE_STATUSES:
            finding.issue_suppressed = True
            suffix = "suppressed: previously seen"
            finding.notes = f"{finding.notes}; {suffix}" if finding.notes else suffix

        entry = seen.get(key) if isinstance(seen.get(key), dict) else {}
        seen[key] = {
            "first_seen": entry.get("first_seen") or now,
            "last_seen": now,
            "status": finding.status,
        }
    return {"version": STATE_VERSION, "seen": seen, "updated_at": now}


def build_report(
    *,
    baseline: TokenKeyBaseline,
    upstream: list[UpstreamModel],
    fetch_errors: list[str],
    state: dict[str, Any] | None = None,
    bootstrap: bool = False,
    alert_all: bool = False,
    git_sha: str = "",
    run_url: str = "",
) -> tuple[dict[str, Any], dict[str, Any]]:
    findings = [classify(item, baseline) for item in upstream]
    new_state = apply_seen_state(
        findings,
        state or {"version": STATE_VERSION, "seen": {}},
        bootstrap=bootstrap,
        alert_all=alert_all,
    )
    findings.sort(key=lambda f: (not f.actionable, f.status != "missing", f.status, f.vendor, f.model_id))
    actionable = [f for f in findings if f.actionable]
    in_scope_gap = [
        f for f in findings
        if f.in_scope and f.status in ACTIONABLE_STATUSES
    ]
    summary = {
        "upstream_count": len(upstream),
        "finding_count": len(findings),
        "in_scope_gap_count": len(in_scope_gap),
        "actionable_count": len(actionable),
        "new_actionable_count": sum(1 for f in actionable if f.is_new),
        "missing": sum(1 for f in findings if f.status == "missing"),
        "unpriced": sum(1 for f in findings if f.status == "unpriced"),
        "narrow": sum(1 for f in findings if f.status == "narrow"),
        "served": sum(1 for f in findings if f.status == "served"),
        "watch_only": sum(1 for f in findings if f.status == "watch_only"),
        "out_of_scope": sum(1 for f in findings if f.status == "out_of_scope"),
        "has_actionable": bool(actionable),
        "bootstrap": bootstrap,
        "alert_all": alert_all,
        "fetch_error_count": len(fetch_errors),
    }
    report = {
        "generated_at": now_utc(),
        "git_sha": git_sha,
        "run_url": run_url,
        "summary": summary,
        "fetch_errors": fetch_errors,
        "findings": [finding_to_dict(f) for f in findings],
    }
    return report, new_state


def render_markdown(report: dict[str, Any]) -> str:
    s = report["summary"]
    lines = [
        "# Model release watch",
        "",
        f"- Generated: `{report.get('generated_at')}`",
        f"- Upstream models: **{s['upstream_count']}**",
        f"- In-scope gaps: **{s.get('in_scope_gap_count', 0)}**",
        f"- Actionable (new + in-scope): **{s['actionable_count']}** "
        f"(missing={s['missing']}, unpriced={s['unpriced']}, narrow={s['narrow']})",
        f"- Served: {s['served']}; watch_only: {s['watch_only']}; out_of_scope: {s['out_of_scope']}",
        f"- Bootstrap: `{s.get('bootstrap')}`; alert_all: `{s.get('alert_all')}`",
        "",
        "## Actionable findings",
        "",
    ]
    actionable = [f for f in report["findings"] if f.get("actionable")]
    if not actionable:
        lines.append("_None._")
    else:
        for f in actionable:
            lines.append(
                f"- `{f['status']}` `{f['vendor']}` `{f['model_id']}` "
                f"new={f.get('is_new')} surfaces={f['surfaces'] or '[]'} "
                f"priced={f['priced']} — {f.get('source_url')}"
            )
    suppressed = [
        f for f in report["findings"]
        if f.get("in_scope") and f.get("status") in ACTIONABLE_STATUSES and not f.get("actionable")
    ]
    if suppressed:
        lines.extend(["", "## In-scope but suppressed (already seen / bootstrap)", ""])
        for f in suppressed[:40]:
            lines.append(
                f"- `{f['status']}` `{f['vendor']}` `{f['model_id']}` — {f.get('notes') or ''}"
            )
        if len(suppressed) > 40:
            lines.append(f"- … {len(suppressed) - 40} more")
    if report.get("fetch_errors"):
        lines.extend(["", "## Fetch errors", ""])
        lines.extend(f"- {err}" for err in report["fetch_errors"])
    lines.append("")
    return "\n".join(lines)


def run_selftest() -> None:
    baseline = TokenKeyBaseline(
        explicit_keys={
            "claude-opus-5-5": [SupplyHit("platform:kiro", "claude-opus-5-5")],
            "claude-fable-5-1": [
                SupplyHit("override:newapi:14:https://agentn.global.api5.cursor.sh", "claude-fable-5-1"),
                SupplyHit("platform:bedrock", "anthropic.claude-fable-5-1"),
            ],
            "claude-sonnet-5-5": [SupplyHit("platform:bedrock", "global.anthropic.claude-sonnet-5-5")],
            "claude-haiku-5-5": [SupplyHit("platform:bedrock", "claude-haiku-5-5")],
            "gpt-6-astra": [SupplyHit("platform:openai", "gpt-6-astra")],
        },
        priced_owners={"claude-opus-5-5", "claude-fable-5-1", "gpt-6-astra", "claude-haiku-5-5"},
    )
    cases = [
        (UpstreamModel("anthropic", "claude-opus-5-5", "u"), "served"),
        (UpstreamModel("anthropic", "claude-fable-5-1", "u"), "served"),
        (UpstreamModel("anthropic", "claude-sonnet-5-5", "u"), "unpriced"),
        (UpstreamModel("anthropic", "claude-mythos-5-1", "u"), "missing"),
        (UpstreamModel("anthropic", "claude-haiku-5-5", "u"), "narrow"),
        (UpstreamModel("openai", "gpt-6-astra", "u"), "served"),
        (UpstreamModel("google", "gemini-4-argon", "u", watch_only=True), "watch_only"),
        (UpstreamModel("google", "gemini-3.8-live", "u"), "out_of_scope"),
        (UpstreamModel("google", "gemini-2.5-flash", "u"), "out_of_scope"),
        (UpstreamModel("anthropic", "claude-opus-4-8", "u"), "out_of_scope"),
    ]

    failures: list[str] = []
    for upstream, expected in cases:
        got = classify(upstream, baseline).status
        if got != expected:
            failures.append(f"{upstream.model_id}: expected {expected}, got {got}")

    google_scope_cases = [
        ("gemini-3.8-flash", True),
        ("gemini-3.8-flash-lite", True),
        ("gemini-3.7-flash", False),
        ("gemini-3.5-flash", False),
        ("gemini-3.1-flash-lite", False),
        ("gemini-3.1-pro-preview", False),
        ("gemini-3.1-pro", False),
        ("gemini-3.1-pro-high", False),
        ("gemini-3.1-pro-low", False),
        ("gemini-3.2-pro", True),
        ("gemini-3-pro-preview", False),
        ("gemini-3.1-flash-image", True),
        ("gemini-3.1-flash-lite-image", False),
        ("gemini-3-pro-image", True),
        ("nano-banana-2", True),
        ("nano-2", True),
        ("nano-banana-pro", True),
        ("gemini-embedding-2-preview", False),
        ("veo-3.1-generate-preview", False),
        ("gemini-2.5-flash-image", False),
        ("gemini-3", False),
        ("gemini-4-argon", True),
    ]
    for mid, want in google_scope_cases:
        got = in_candidate_scope("google", mid)
        if got != want:
            failures.append(f"google scope {mid}: expected {want}, got {got}")

    # Novelty / bootstrap
    findings = [
        classify(UpstreamModel("anthropic", "claude-mythos-5-1", "u"), baseline),
        classify(UpstreamModel("anthropic", "claude-sonnet-5-5", "u"), baseline),
    ]
    state = apply_seen_state(findings, {"version": 1, "seen": {}}, bootstrap=True, alert_all=False)
    if any(f.actionable for f in findings):
        failures.append("bootstrap must suppress actionable issues")
    findings2 = [
        classify(UpstreamModel("anthropic", "claude-mythos-5-1", "u"), baseline),
        classify(UpstreamModel("anthropic", "claude-haiku-5-5", "u"), baseline),  # new narrow
    ]
    apply_seen_state(findings2, state, bootstrap=False, alert_all=False)
    if findings2[0].actionable:
        failures.append("previously seen mythos must not be actionable")
    if not findings2[1].actionable:
        failures.append("newly seen in-scope narrow must be actionable")

    # Wildcard keys must not enter baseline from loader helper
    tmp = TokenKeyBaseline()
    _add_key(tmp, "claude-*", "platform:openai_cloudwise_relay", "claude-*")
    if tmp.explicit_keys:
        failures.append("wildcard key incorrectly accepted")

    if failures:
        raise AssertionError("; ".join(failures))
    print("model-release-watch selftest ok")


def main(argv: list[str] | None = None) -> int:
    # Allow `python3 ops/pricing/model_release_watch.py` imports of sibling modules.
    sys.path.insert(0, str(Path(__file__).resolve().parent))

    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--selftest", action="store_true")
    ap.add_argument("--fixture", type=Path, help="Upstream models JSON fixture (skip live fetch)")
    ap.add_argument("--vendors", default="", help="Comma-separated vendor ids")
    ap.add_argument("--report-json", type=Path, default=DEFAULT_REPORT_JSON)
    ap.add_argument("--report-md", type=Path, default=DEFAULT_REPORT_MD)
    ap.add_argument("--state", type=Path, default=DEFAULT_STATE)
    ap.add_argument(
        "--bootstrap-state",
        action="store_true",
        help="Seed/update seen-state from this scan but suppress all issue actionables",
    )
    ap.add_argument(
        "--alert-all",
        action="store_true",
        help="Ignore novelty filter; alert every in-scope gap (still respects generation scope)",
    )
    ap.add_argument("--git-sha", default="")
    ap.add_argument("--run-url", default="")
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args(argv)

    if args.selftest:
        run_selftest()
        return 0

    vendors = [v.strip() for v in args.vendors.split(",") if v.strip()] or None
    baseline = load_baseline()
    upstream, fetch_errors = collect_upstream(vendors=vendors, fixture=args.fixture)
    prior = load_state(args.state)
    bootstrap = args.bootstrap_state or not (prior.get("seen") or {})
    report, new_state = build_report(
        baseline=baseline,
        upstream=upstream,
        fetch_errors=fetch_errors,
        state=prior,
        bootstrap=bootstrap,
        alert_all=args.alert_all,
        git_sha=args.git_sha,
        run_url=args.run_url,
    )
    args.report_json.parent.mkdir(parents=True, exist_ok=True)
    args.report_json.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    args.report_md.write_text(render_markdown(report), encoding="utf-8")
    save_state(args.state, new_state)
    if not args.quiet:
        if bootstrap and not args.bootstrap_state:
            print("note: empty state → bootstrap seed (no actionable issues this run)")
        print(render_markdown(report))
        print(f"wrote {args.report_json}")
        print(f"wrote {args.state}")
    return 1 if report["summary"]["has_actionable"] else 0


if __name__ == "__main__":
    raise SystemExit(main())
