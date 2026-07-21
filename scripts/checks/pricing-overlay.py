#!/usr/bin/env python3
"""Validate the TK-owned pricing overlay.

Source of truth: backend/internal/service/tk_pricing_overlay.json — a small curated
overlay merged (fill-only) into every PricingService load so models the production
runtime source lacks resolve to a real price. The source (Wei-Shaw mirror, a trimmed
litellm) drops provider-prefixed + token-less media keys (imagen-*/veo-*), and litellm
itself lags new provider models (deepseek-v4-*). Without this overlay imagen silently
bills the $0.134 default, veo bills $0, and uncatalogued text models bill $0 via
pricing_missing_record_zero_cost.

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

# mode -> the price field(s) that MUST be > 0 for that mode
MODE_FIELDS = {
    "image_generation": ("output_cost_per_image",),
    "video_generation": ("output_cost_per_second",),
    "chat": ("input_cost_per_token", "output_cost_per_token"),
}

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


def validate_official_list_base_tax(data: dict) -> list[str]:
    errors: list[str] = []
    config = data.get("_config")
    if not isinstance(config, dict):
        return ["_config must be an object"]
    unknown_config = sorted(set(config) - {"official_list_base_tax", "deepseek_peak_valley"})
    if unknown_config:
        errors.append(f"_config has unknown fields: {unknown_config}")
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
        mode = pricing.get("mode")
        fields = MODE_FIELDS.get(mode)
        if fields is None:
            errors.append(f"{model}: unrecognized mode {mode!r} (want one of {sorted(MODE_FIELDS)})")
            continue
        for field in fields:
            price = pricing.get(field)
            if not isinstance(price, (int, float)) or price <= 0:
                errors.append(f"{model}: mode={mode} requires {field} > 0, got {price!r}")
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
