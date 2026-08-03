#!/usr/bin/env python3
"""Validate the TK-owned pricing overlay.

Source of truth: backend/internal/service/tk_pricing_overlay.json — the complete
runtime pricing registry. Every active model resolves to one owner row; provider
snapshots are offline import evidence only, and channel_model_pricing is the only
scoped override above it. No remote or bundled mirror participates in runtime resolution.

This check hardens that against silent regression (CLAUDE.md §5 "upgrade principle":
a soft rule that bit us once becomes a mechanical gate). It asserts:
  1. The overlay parses and is non-empty.
  2. Anchor models are present with a non-zero price in the right field:
       imagen-4.0-generate-001        -> output_cost_per_image > 0
       veo-3.1-generate-001           -> output_cost_per_second > 0
       deepseek-v4-flash              -> input_cost_per_token > 0
       doubao-seedream-4-0-250828     -> output_cost_per_image > 0
       doubao-seedance-1-0-pro-250528 -> output_cost_per_second > 0
       grok-4.3                       -> input_cost_per_token > 0
       grok-build-0.1                 -> input_cost_per_token > 0
  3. EVERY entry has a recognized mode and a > 0 price in the matching field(s)
     (no silently-shipped $0 entry, which would deduct nothing):
       image_generation -> output_cost_per_image
       video_generation -> output_cost_per_second
       chat             -> input_cost_per_token AND output_cost_per_token
  4. `_config.official_list_base_tax` is a valid executable policy: one bounded
     multiplier, unique normalized providers, and non-duplicated fallback matchers.

Usage: python3 scripts/checks/pricing-overlay.py [--quiet]
Exit 0 ok, 1 violation, 2 missing dep / file / unparseable.
"""

from __future__ import annotations

import argparse
import json
import math
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
OVERLAY = REPO_ROOT / "backend" / "internal" / "service" / "tk_pricing_overlay.json"

# mode -> the price field(s) that MUST be > 0 for that mode. The registry also
# carries native OpenAI/Gemini embedding/audio rows imported from the former
# catalog; they are validated here so they cannot become an unowned runtime gap.
MODE_FIELDS = {
    "image_generation": ("output_cost_per_image", "output_cost_per_image_token"),
    "video_generation": ("output_cost_per_second",),
    "chat": ("input_cost_per_token", "output_cost_per_token"),
    "completion": ("input_cost_per_token", "output_cost_per_token"),
    "responses": ("input_cost_per_token", "output_cost_per_token"),
    "realtime": ("input_cost_per_token", "output_cost_per_token"),
    "audio_transcription": ("input_cost_per_token", "output_cost_per_token"),
    "audio_speech": ("input_cost_per_token", "output_cost_per_token"),
    "embedding": ("input_cost_per_token",),
}

GO_FLOAT_FIELDS = (
    "input_cost_per_token",
    "input_cost_per_token_priority",
    "output_cost_per_token",
    "output_cost_per_token_priority",
    "thinking_output_cost_per_token",
    "cache_creation_input_token_cost",
    "cache_creation_input_token_cost_priority",
    "cache_creation_input_token_cost_above_1hr",
    "cache_read_input_token_cost",
    "cache_read_input_token_cost_priority",
    "long_context_input_cost_multiplier",
    "long_context_output_cost_multiplier",
    "input_cost_per_token_above_272k_tokens",
    "output_cost_per_token_above_272k_tokens",
    "cache_read_input_token_cost_above_272k_tokens",
    "output_cost_per_image",
    "output_cost_per_image_token",
    "input_cost_per_image_token",
    "image_price_1k",
    "image_price_2k",
    "image_price_4k",
    "output_cost_per_second",
)
GO_INT_FIELDS = (
    "long_context_input_token_threshold",
    "max_input_tokens",
    "max_output_tokens",
)
GO_BOOL_FIELDS = (
    "supports_service_tier",
    "supports_prompt_caching",
    "supports_vision",
    "supports_tool_choice",
    "supports_function_calling",
    "supports_reasoning",
    "supports_response_schema",
    "supports_pdf_input",
    "supports_web_search",
    "explicit_free",
)
GO_STRING_FIELDS = ("litellm_provider", "mode")
INTERVAL_FLOAT_FIELDS = (
    "input_cost_per_token",
    "output_cost_per_token",
    "cache_read_input_token_cost",
    "cache_creation_input_token_cost",
)

ANCHORS = {
    "imagen-4.0-generate-001": "output_cost_per_image",
    "veo-3.1-generate-001": "output_cost_per_second",
    "deepseek-v4-flash": "input_cost_per_token",
    "doubao-seedream-4-0-250828": "output_cost_per_image",
    "doubao-seedance-1-0-pro-250528": "output_cost_per_second",
    "grok-4.3": "input_cost_per_token",
    "grok-build-0.1": "input_cost_per_token",
}

# Models that MUST carry a thinking-mode output price. For Qwen3 open-source dense
# models enable_thinking defaults to true, so dropping thinking_output_cost_per_token
# would make the DEFAULT request bill the cheaper non-thinking rate — a silent
# under-bill. These anchors fail the check if the field goes missing.
THINKING_ANCHORS = ("qwen3-8b", "qwen3-14b", "qwen3-32b")
VIDEO_RESOLUTIONS = {"480p", "720p", "1080p", "4k"}
VIDEO_SOURCE_CONTRACT = "video_price_tiers is the billing ssot"
STALE_VIDEO_SOURCE_PHRASES = (
    "tk bills a single",
    "tk's flat",
    "single per-second price",
    "priced at the model's max tier",
    "max-output convention",
    "retained (not lowered)",
    "same max-tier convention",
)


def _finite_number(value: object) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)


def validate_runtime_owner_shape(model: str, pricing: dict) -> list[str]:
    """Keep the standalone gate aligned with the Go registry decoder."""
    errors: list[str] = []
    if model != model.strip().lower() or "/" in model:
        errors.append(f"{model}: owner key must be normalized lowercase and bare")
    for field in GO_FLOAT_FIELDS:
        value = pricing.get(field)
        if field in pricing and value is not None and not _finite_number(value):
            errors.append(f"{model}: {field} must be a finite number when present")
        elif field in pricing and value is not None and value < 0:
            errors.append(f"{model}: {field} must be >= 0 when present")
    for field in GO_INT_FIELDS:
        value = pricing.get(field)
        if field in pricing and value is not None and (not isinstance(value, int) or isinstance(value, bool)):
            errors.append(f"{model}: {field} must be an integer when present")
        elif field in pricing and value is not None and value <= 0:
            errors.append(f"{model}: {field} must be > 0 when present")
    for field in GO_BOOL_FIELDS:
        value = pricing.get(field)
        if field in pricing and value is not None and not isinstance(value, bool):
            errors.append(f"{model}: {field} must be boolean when present")
    for field in GO_STRING_FIELDS:
        value = pricing.get(field)
        if field in pricing and value is not None and not isinstance(value, str):
            errors.append(f"{model}: {field} must be a string when present")

    intervals = pricing.get("intervals")
    if intervals is not None:
        if not isinstance(intervals, list):
            errors.append(f"{model}: intervals must be an array when present")
        else:
            parsed_bounds: list[tuple[int, int | None, str]] = []
            for idx, interval in enumerate(intervals):
                label = f"{model}.intervals[{idx}]"
                if not isinstance(interval, dict):
                    errors.append(f"{label} must be an object")
                    continue
                for field in ("min_tokens", "max_tokens"):
                    value = interval.get(field)
                    if field in interval and value is not None and (
                            not isinstance(value, int) or isinstance(value, bool)):
                        errors.append(f"{label}.{field} must be an integer or null")
                for field in INTERVAL_FLOAT_FIELDS:
                    value = interval.get(field)
                    if field in interval and value is not None and not _finite_number(value):
                        errors.append(f"{label}.{field} must be a finite number")
                    elif field in interval and value is not None and value < 0:
                        errors.append(f"{label}.{field} must be >= 0")
                min_tokens = interval.get("min_tokens", 0)
                max_tokens = interval.get("max_tokens")
                if isinstance(min_tokens, int) and not isinstance(min_tokens, bool):
                    if min_tokens < 0:
                        errors.append(f"{label}.min_tokens must be >= 0")
                    if isinstance(max_tokens, int) and not isinstance(max_tokens, bool):
                        if max_tokens <= min_tokens:
                            errors.append(f"{label}.max_tokens must be > min_tokens")
                    if max_tokens is None or (isinstance(max_tokens, int) and not isinstance(max_tokens, bool)):
                        parsed_bounds.append((min_tokens, max_tokens, label))
            parsed_bounds.sort(key=lambda item: item[0])
            for idx, (min_tokens, max_tokens, label) in enumerate(parsed_bounds):
                if max_tokens is None and idx < len(parsed_bounds) - 1:
                    errors.append(f"{label}.max_tokens=null is only valid on the last interval")
                if idx > 0:
                    previous_max = parsed_bounds[idx - 1][1]
                    if previous_max is None or previous_max > min_tokens:
                        errors.append(f"{label} overlaps the preceding interval")

    video_tiers = pricing.get("video_price_tiers")
    if video_tiers is not None and not isinstance(video_tiers, list):
        errors.append(f"{model}: video_price_tiers must be an array when present")
    default_resolution = pricing.get("default_video_resolution")
    if default_resolution is not None and not isinstance(default_resolution, str):
        errors.append(f"{model}: default_video_resolution must be a string when present")
    return errors


def validate_priced_dimension_completeness(model: str, pricing: dict) -> list[str]:
    """Assert a row carries every price dimension its billing path will read.

    BillingService.applyModelSpecificPricingPolicy used to COMPLETE missing
    numbers in Go (deriving a gpt-5.6 cache-write price as input x 1.25, and
    back-filling the 272K long-context threshold/multipliers). Now that pricing
    policy is data-owned, that numeric completion is deliberately gone — which
    means an owner row that omits a dimension no longer gets a silent Go rescue;
    billing simply reads zero. This gate replaces the removed safety net
    mechanically (CLAUDE.md upgrade principle) so the omission fails CI instead
    of under-billing production.

    Two dimensions are load-bearing today:
      1. A row exposing ANY priority rate must provide a positive priority price
         for every positive base dimension billing can consume. Missing fields
         silently fall back to the base rate.
      2. A row declaring either normalized long-context policy or imported
         above-272K rates must declare the complete representation needed to
         derive both input and output multipliers.
    """
    errors: list[str] = []
    if pricing.get("explicit_free") is True:
        return errors

    def positive(field: str) -> bool:
        value = pricing.get(field)
        return _finite_number(value) and value > 0

    # (1) priority tier completeness. Presence matters: the old gate checked
    # only fields that happened to exist, so deleting a priority cache-write
    # field made the row pass while runtime silently fell back to its base rate.
    priority_pairs = (
        ("input_cost_per_token", "input_cost_per_token_priority"),
        ("output_cost_per_token", "output_cost_per_token_priority"),
        ("cache_creation_input_token_cost", "cache_creation_input_token_cost_priority"),
        ("cache_read_input_token_cost", "cache_read_input_token_cost_priority"),
    )
    declares_priority = any(priority in pricing for _, priority in priority_pairs)
    if declares_priority:
        for base, priority in priority_pairs:
            if positive(base) and not positive(priority):
                errors.append(
                    f"{model}: priority tier requires {priority} > 0 because {base} > 0 "
                    f"(Go no longer derives it; priority traffic would fall back to base price)"
                )

    # (2a) normalized long-context triple completeness.
    long_context_fields = (
        "long_context_input_token_threshold",
        "long_context_input_cost_multiplier",
        "long_context_output_cost_multiplier",
    )
    declares_long_context = any(f in pricing for f in long_context_fields)
    if declares_long_context:
        missing = [f for f in long_context_fields if not positive(f)]
        if missing:
            errors.append(
                f"{model}: partial long-context policy — missing {missing}. Declare the full "
                f"triple (threshold + both multipliers); Go no longer back-fills it, so "
                f"long-context requests would bill at the base rate"
            )

    # (2b) imported above-272K rates are an alternative complete expression.
    # The decoder derives the threshold and input/output multipliers from these
    # fields. Seeing just one field is not self-describing: the absent multiplier
    # remains zero and that side of the request bills at the base rate.
    above_272k_pairs = (
        ("input_cost_per_token", "input_cost_per_token_above_272k_tokens"),
        ("output_cost_per_token", "output_cost_per_token_above_272k_tokens"),
        ("cache_read_input_token_cost", "cache_read_input_token_cost_above_272k_tokens"),
    )
    declares_above_272k = any(above in pricing for _, above in above_272k_pairs)
    if declares_above_272k:
        for base, above in above_272k_pairs:
            if positive(base) and not positive(above):
                errors.append(
                    f"{model}: above-272K policy requires {above} > 0 because {base} > 0; "
                    f"the decoder cannot derive a complete long-context policy without it"
                )
            elif positive(above) and not positive(base):
                errors.append(
                    f"{model}: {above} > 0 requires positive base field {base} to derive "
                    f"the long-context multiplier"
                )
    return errors


def validate_official_list_base_tax(data: dict) -> list[str]:
    errors: list[str] = []
    config = data.get("_config")
    if not isinstance(config, dict):
        return ["_config must be an object"]
    unknown_config = sorted(set(config) - {"official_list_base_tax", "deepseek_peak_valley", "web_search_price_per_call"})
    if unknown_config:
        errors.append(f"_config has unknown fields: {unknown_config}")
    # web_search_price_per_call is REQUIRED and must be > 0. It replaced a
    # hardcoded Go default, so an omitted/zero key would silently bill every
    # Codex alpha/search call at $0 instead of failing loudly.
    web_search_price = config.get("web_search_price_per_call")
    if web_search_price is None:
        errors.append(
            "_config.web_search_price_per_call is required (it owns the "
            "per-call web-search price; omitting it bills searches at $0)"
        )
    elif (not isinstance(web_search_price, (int, float)) or isinstance(web_search_price, bool)
            or not math.isfinite(web_search_price) or web_search_price <= 0):
        errors.append(
            f"_config.web_search_price_per_call must be finite and > 0, got {web_search_price!r}"
        )
    policy = config.get("official_list_base_tax")
    if not isinstance(policy, dict):
        return errors + ["_config.official_list_base_tax must be an object"]
    unknown_policy = sorted(set(policy) - {"multiplier", "rules"})
    if unknown_policy:
        errors.append(f"official_list_base_tax has unknown fields: {unknown_policy}")
    multiplier = policy.get("multiplier")
    if (not isinstance(multiplier, (int, float)) or isinstance(multiplier, bool)
            or not math.isfinite(multiplier) or multiplier < 1 or multiplier > 2):
        errors.append(f"official_list_base_tax.multiplier must be within [1,2], got {multiplier!r}")
    rules = policy.get("rules")
    if not isinstance(rules, list) or not rules:
        return errors + ["official_list_base_tax.rules must be a non-empty array"]

    providers: set[str] = set()
    matchers: dict[tuple[str, str], str] = {}
    allowed_rule_fields = {"provider", "model_prefixes", "model_contains"}
    for idx, rule in enumerate(rules):
        label = f"official_list_base_tax.rules[{idx}]"
        if not isinstance(rule, dict):
            errors.append(f"{label} must be an object")
            continue
        unknown = sorted(set(rule) - allowed_rule_fields)
        if unknown:
            errors.append(f"{label} has unknown fields: {unknown}")
        provider = rule.get("provider")
        if not isinstance(provider, str) or not provider or provider != provider.strip().lower():
            errors.append(f"{label}.provider must be normalized lowercase")
            continue
        if provider in providers:
            errors.append(f"official_list_base_tax provider {provider!r} is duplicated")
        providers.add(provider)
        prefixes = rule.get("model_prefixes", [])
        contains = rule.get("model_contains", [])
        if not prefixes and not contains:
            errors.append(f"official_list_base_tax provider {provider!r} requires a fallback matcher")
        for kind, values in (("prefix", prefixes), ("contains", contains)):
            if not isinstance(values, list):
                errors.append(f"{label}.model_{kind} values must be an array")
                continue
            seen: set[str] = set()
            for value in values:
                if not isinstance(value, str) or not value or value != value.strip().lower():
                    errors.append(f"{label} has invalid {kind} matcher {value!r}")
                    continue
                if value in seen:
                    errors.append(f"{label} duplicates {kind} matcher {value!r}")
                seen.add(value)
                key = (kind, value)
                if key in matchers:
                    errors.append(
                        f"official_list_base_tax {kind} matcher {value!r} belongs to both "
                        f"{matchers[key]!r} and {provider!r}"
                    )
                matchers[key] = provider
    return errors


def _valid_hhmm(value: object) -> bool:
    if not isinstance(value, str) or len(value) != 5 or value[2] != ":":
        return False
    try:
        hour = int(value[:2])
        minute = int(value[3:])
    except ValueError:
        return False
    return 0 <= hour <= 23 and 0 <= minute <= 59


def validate_deepseek_peak_valley(data: dict) -> list[str]:
    errors: list[str] = []
    config = data.get("_config")
    if not isinstance(config, dict):
        return errors
    policy = config.get("deepseek_peak_valley")
    if policy is None:
        return errors
    if not isinstance(policy, dict):
        return errors + ["_config.deepseek_peak_valley must be an object"]
    unknown = sorted(set(policy) - {"timezone", "peak_multiplier", "windows", "model_contains"})
    if unknown:
        errors.append(f"deepseek_peak_valley has unknown fields: {unknown}")
    multiplier = policy.get("peak_multiplier")
    if (not isinstance(multiplier, (int, float)) or isinstance(multiplier, bool)
            or not math.isfinite(multiplier) or multiplier < 1 or multiplier > 4):
        errors.append(f"deepseek_peak_valley.peak_multiplier must be within [1,4], got {multiplier!r}")
    windows = policy.get("windows")
    if not isinstance(windows, list) or not windows:
        return errors + ["deepseek_peak_valley.windows must be a non-empty array"]
    for idx, window in enumerate(windows):
        label = f"deepseek_peak_valley.windows[{idx}]"
        if not isinstance(window, dict):
            errors.append(f"{label} must be an object")
            continue
        start = window.get("start")
        end = window.get("end")
        if not _valid_hhmm(start):
            errors.append(f"{label}.start must be HH:MM, got {start!r}")
            continue
        if not _valid_hhmm(end):
            errors.append(f"{label}.end must be HH:MM, got {end!r}")
            continue
        sh, sm = int(start[:2]), int(start[3:])
        eh, em = int(end[:2]), int(end[3:])
        if sh * 60 + sm >= eh * 60 + em:
            errors.append(f"{label} requires end > start")
    contains = policy.get("model_contains")
    if not isinstance(contains, list) or not contains:
        errors.append("deepseek_peak_valley.model_contains must be a non-empty array")
    elif contains:
        for idx, value in enumerate(contains):
            if not isinstance(value, str) or not value or value != value.strip().lower():
                errors.append(f"deepseek_peak_valley.model_contains[{idx}] must be normalized lowercase")
    tz = policy.get("timezone", "")
    if tz != "" and not isinstance(tz, str):
        errors.append("deepseek_peak_valley.timezone must be a string when present")
    return errors


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--quiet", action="store_true", help="suppress success output")
    ap.add_argument("--path", type=pathlib.Path, default=OVERLAY,
                    help="overlay JSON to validate (default: repo embedded overlay)")
    args = ap.parse_args()
    quiet = args.quiet
    overlay = args.path
    if not overlay.is_absolute():
        overlay = REPO_ROOT / overlay

    if not overlay.is_file():
        print(f"  FAIL: pricing overlay not found: {overlay}", flush=True)
        return 2
    try:
        data = json.loads(overlay.read_text(encoding="utf-8"))
    except (ValueError, OSError) as exc:
        print(f"  FAIL: pricing overlay unparseable: {exc}", flush=True)
        return 2

    # Entries are bare model -> pricing dict; keys starting with "_" (e.g. _meta) are
    # provenance, not pricing.
    entries = {k: v for k, v in data.items() if not k.startswith("_")}
    errors: list[str] = validate_official_list_base_tax(data)
    errors.extend(validate_deepseek_peak_valley(data))

    if not entries:
        errors.append("overlay has zero pricing entries")

    for model, pricing in entries.items():
        if not isinstance(pricing, dict):
            errors.append(f"{model}: entry is not an object")
            continue
        errors.extend(validate_runtime_owner_shape(model, pricing))
        errors.extend(validate_priced_dimension_completeness(model, pricing))
        mode = pricing.get("mode")
        fields = MODE_FIELDS.get(mode)
        if fields is None:
            errors.append(f"{model}: unrecognized mode {mode!r} (want one of {sorted(MODE_FIELDS)})")
            continue
        if pricing.get("explicit_free") is True:
            if any((pricing.get(field) or 0) != 0 for field in fields):
                errors.append(f"{model}: explicit_free rows must carry zero registry prices")
            continue
        if mode == "image_generation":
            if not any(_finite_number(pricing.get(field)) and pricing.get(field) > 0 for field in fields):
                errors.append(f"{model}: mode={mode} requires one of {fields} > 0")
        else:
            for field in fields:
                price = pricing.get(field)
                if not isinstance(price, (int, float)) or price <= 0:
                    errors.append(f"{model}: mode={mode} requires {field} > 0, got {price!r}")
        if mode == "video_generation":
            tiers = pricing.get("video_price_tiers")
            if isinstance(tiers, list) and tiers:
                seen_resolutions: set[str] = set()
                defaults: list[str] = []
                for idx, tier in enumerate(tiers):
                    label = f"{model}.video_price_tiers[{idx}]"
                    if not isinstance(tier, dict):
                        errors.append(f"{label} must be an object")
                        continue
                    res = tier.get("resolution")
                    if not isinstance(res, str) or res not in VIDEO_RESOLUTIONS:
                        errors.append(f"{label}.resolution must be one of {sorted(VIDEO_RESOLUTIONS)}")
                    elif res in seen_resolutions:
                        errors.append(f"{model}: duplicate video resolution {res!r}")
                    else:
                        seen_resolutions.add(res)
                    if tier.get("default_for_model") is True and isinstance(res, str):
                        defaults.append(res)
                    if "default_for_model" in tier and not isinstance(tier["default_for_model"], bool):
                        errors.append(f"{label}.default_for_model must be boolean when present")
                    rate = tier.get("output_cost_per_second")
                    if not _finite_number(rate) or rate <= 0:
                        errors.append(f"{label}.output_cost_per_second must be > 0, got {rate!r}")
                    silent = tier.get("output_cost_per_second_silent")
                    if silent is not None and (not _finite_number(silent) or silent <= 0):
                        errors.append(f"{label}.output_cost_per_second_silent must be > 0 when present")
                    surcharge = tier.get("input_image_surcharge_per_second")
                    if surcharge is not None and (not _finite_number(surcharge) or surcharge < 0):
                        errors.append(f"{label}.input_image_surcharge_per_second must be >= 0 when present")
                if len(defaults) != 1:
                    errors.append(f"{model}: video_price_tiers must declare exactly one default_for_model")
                declared_default = pricing.get("default_video_resolution")
                if declared_default is not None and defaults and declared_default != defaults[0]:
                    errors.append(
                        f"{model}: default_video_resolution {declared_default!r} does not match "
                        f"default_for_model {defaults[0]!r}"
                    )
                flat = pricing.get("output_cost_per_second")
                tier_mins: list[float] = []
                for tier in tiers:
                    if isinstance(tier, dict):
                        r = tier.get("output_cost_per_second")
                        if _finite_number(r) and r > 0:
                            tier_mins.append(r)
                        s = tier.get("output_cost_per_second_silent")
                        if _finite_number(s) and s > 0:
                            tier_mins.append(s)
                if tier_mins and _finite_number(flat):
                    if abs(flat - min(tier_mins)) > 1e-12:
                        errors.append(
                            f"{model}: output_cost_per_second {flat} must equal min video tier "
                            f"{min(tier_mins)} (catalog compatibility floor)"
                        )
                source = pricing.get("source")
                source_lower = source.lower() if isinstance(source, str) else ""
                if VIDEO_SOURCE_CONTRACT not in source_lower:
                    errors.append(f"{model}: tiered-video source must state the billing SSOT contract")
                for phrase in STALE_VIDEO_SOURCE_PHRASES:
                    if phrase in source_lower:
                        errors.append(f"{model}: tiered-video source contains stale flat-billing phrase {phrase!r}")
        # TK thinking-mode output price (e.g. qwen3-8b/14b/32b): an optional field
        # that, when present, must be a real positive price — a $0 thinking rate
        # would silently under-bill thinking traffic, which for these models is the
        # DEFAULT mode (enable_thinking defaults to true). Mirrors Alibaba's two-rate
        # table; consumed by computeTokenBreakdown.
        if "thinking_output_cost_per_token" in pricing:
            tp = pricing.get("thinking_output_cost_per_token")
            if not isinstance(tp, (int, float)) or tp <= 0:
                errors.append(
                    f"{model}: thinking_output_cost_per_token must be > 0 when present, got {tp!r}"
                )
        if mode == "video_generation":
            # TokenKey refunds the user in full when a video task ends failed —
            # loss-free ONLY if the provider does not charge for failed tasks.
            # Whoever prices a video model verifies that on the official pricing
            # page and declares it here; a provider that charges on failure must
            # not be priced (= not served) until the refund design handles it.
            failure_billing = pricing.get("failure_billing")
            if failure_billing != "success_only":
                errors.append(
                    f"{model}: video entries must declare failure_billing='success_only' "
                    f"(got {failure_billing!r}); a provider that charges for failed tasks "
                    f"breaks the terminal-failure refund — change the refund design before "
                    f"pricing it"
                )

    for model, field in ANCHORS.items():
        pricing = entries.get(model)
        if not isinstance(pricing, dict):
            errors.append(f"anchor {model} missing from overlay")
            continue
        price = pricing.get(field)
        if not isinstance(price, (int, float)) or price <= 0:
            errors.append(f"anchor {model}: {field} must be > 0, got {price!r}")

    for model in THINKING_ANCHORS:
        pricing = entries.get(model)
        if not isinstance(pricing, dict):
            errors.append(f"thinking-anchor {model} missing from overlay")
            continue
        tp = pricing.get("thinking_output_cost_per_token")
        if not isinstance(tp, (int, float)) or tp <= 0:
            errors.append(
                f"thinking-anchor {model}: thinking_output_cost_per_token must be > 0 "
                f"(enable_thinking defaults to true → this is the default-mode price), got {tp!r}"
            )

    if errors:
        print(f"  FAIL: pricing overlay invalid ({len(errors)} issue(s)):", flush=True)
        for e in errors:
            print(f"    - {e}", flush=True)
        return 1

    if not quiet:
        print(f"  ok: {len(entries)} pricing overlay entries valid (anchors present, no $0)", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
