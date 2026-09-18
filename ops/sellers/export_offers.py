#!/usr/bin/env python3
"""Export existing seller prices without inference, account writes or credentials."""
from __future__ import annotations

import argparse
import base64
import gzip
import hashlib
import importlib.util
import json
from decimal import Decimal
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PLATFORMS = ("nanogpt", "poe", "huggingface", "eurouter")


def decimal_string(value):
    result = format(Decimal(str(value)), "f")
    return result.rstrip("0").rstrip(".") if "." in result else result


def normalize_price(price):
    result = dict(price)
    if "cost" in price:
        result["cost"] = decimal_string(price["cost"])
    if "cost_usd" in price:
        result["cost_usd"] = decimal_string(price["cost_usd"])
    cost = price.get("cost", price.get("cost_usd"))
    if cost is not None and price.get("unit") == "token":
        result["usd_per_million_tokens"] = decimal_string(Decimal(str(cost)) * 1000000)
    return result


def effective_rate(snapshot):
    rates = set()
    for group in snapshot["groups"]:
        if group["status"] != "active":
            raise ValueError("inactive authorized group requires catalog reconciliation")
        rate = group["user_rate_multiplier"]
        rates.add(Decimal(str(group["rate_multiplier"] if rate is None else rate)))
        if group.get("peak_rate_enabled"):
            raise ValueError("group peak policy requires per-model routing attribution")
    if len(rates) != 1:
        raise ValueError("different group rates require per-model routing attribution")
    rate = rates.pop()
    if not rate.is_finite() or rate < 0:
        raise ValueError("invalid effective rate")
    return rate


def public_prices(pricing, rate):
    if pricing.get("currency") != "USD":
        raise ValueError("expected public USD pricing")
    fields = {
        "input_per_1k_tokens": ("input", "prompt"),
        "output_per_1k_tokens": ("output", "completion"),
        "cache_read_per_1k": ("input", "cached_prompt"),
        "cache_write_per_1k": ("input", "cache_write"),
        "thinking_output_per_1k_tokens": ("output", "thinking_completion"),
    }
    result = []

    def add(block, conditions):
        for name, (direction, kind) in fields.items():
            if name not in block:
                continue
            amount = Decimal(str(block[name])) * rate / 1000
            if not amount.is_finite() or amount < 0:
                raise ValueError("invalid public price")
            result.append({
                "direction": direction, "modality": "text", "type": kind,
                "unit": "token", "cost_usd": decimal_string(amount),
                "usd_per_million_tokens": decimal_string(amount * 1000000),
                **conditions,
            })

    add(pricing, {})
    for tier in pricing.get("tiers", []):
        add(tier, {"context_tier": {k: tier[k] for k in ("min_tokens", "max_tokens") if k in tier}})
    peak = pricing.get("peak_valley")
    if peak:
        add(peak, {"peak_window": {k: peak[k] for k in ("timezone", "windows", "peak_multiplier") if k in peak}})
    media_fields = ("output_cost_per_image", "output_cost_per_second", "output_cost_per_character",
                    "input_cost_per_image_token", "output_cost_per_image_token",
                    "image_price_1k", "image_price_2k", "image_price_4k", "video_price_tiers")
    if any(pricing.get(k) for k in media_fields):
        raise ValueError("media pricing requires separate billing-unit validation")
    return result


def build_offers(snapshot, mapping_bundle=None):
    rows = []
    excluded = []
    rate = effective_rate(snapshot)
    public = {r["model_id"]: r for r in snapshot["public_pricing"]["data"]}
    alias_targets = {}
    if mapping_bundle is not None:
        for override in mapping_bundle["account_model_mapping"]["account_overrides"]:
            for source, target in override["model_mapping"].items():
                if source != target and source in public and target in public:
                    alias_targets.setdefault(source, set()).add(target)
    for model in sorted(snapshot["catalog"]["data"], key=lambda r: r["id"]):
        source_id = model["id"].removeprefix(snapshot["model_id_prefix"])
        if source_id not in public:
            excluded.append({"model_id": model["id"], "reason": "not_in_public_pricing; OR fallback price is not a public quote"})
            continue
        if source_id in alias_targets:
            excluded.append({
                "model_id": model["id"],
                "reason": "supply_bundle_alias; confirm actual served model and settlement before quoting",
                "possible_served_models": sorted(alias_targets[source_id]),
            })
            continue
        prices = public_prices(public[source_id]["pricing"], rate)
        or_prices = []
        for direction in ("input", "output"):
            for modality in model.get(direction + "_modalities", []):
                for price in modality.get("pricing", []):
                    or_prices.append({"direction": direction, "modality": modality["type"], **normalize_price(price)})
        if not prices:
            raise ValueError("unpriced seller model: " + model["id"])
        rows.append({
            "model_id": model["id"], "source_model_id": source_id,
            "seller_prices": prices,
            "openrouter_catalog_prices": or_prices,
            "public_pricing_reference": public.get(source_id, {}).get("pricing"),
            "catalog_document": model,
        })
    return {
        "captured_at": snapshot["captured_at"],
        "billing_user_id": snapshot["user"]["id"],
        "commercial_policy": "Public pricing intersected with the current OR seller catalog, multiplied by the billing user's effective group rate; separate API key per platform.",
        "effective_rate_multiplier": decimal_string(rate),
        "price_source": "https://api.tokenkey.dev/api/v1/public/pricing",
        "catalog_source": "https://api.tokenkey.dev/openrouter/v1/models",
        "platform_key_names": list(PLATFORMS),
        "mapping_bundle_reviewed": mapping_bundle is not None,
        "excluded_from_public_quote": excluded,
        "models": rows,
    }


def fetch_snapshot(*, ensure_keys=False):
    spec = importlib.util.spec_from_file_location("ssm_execution", ROOT / "ops/stage0/ssm_execution.py")
    ssm = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(ssm)
    remote = (Path(__file__).parent / "seller_snapshot_remote.py").read_text()
    command = "python3 - --ensure-platform-keys" if ensure_keys else "python3 -"
    shell = command + " <<'TK_SELLER_SNAPSHOT'\n" + remote + "\nTK_SELLER_SNAPSHOT\n"
    envelope = json.loads(ssm.run_shell_b64(
        ssm.resolve_prod_instance(), base64.b64encode(shell.encode()).decode(),
        "seller onboarding: ensure platform keys" if ensure_keys else "seller onboarding: read-only pricing and policy snapshot",
    ))
    raw = gzip.decompress(base64.b64decode(envelope["gzip_base64"], validate=True))
    if hashlib.sha256(raw).hexdigest() != envelope["sha256"]:
        raise ValueError("snapshot integrity mismatch")
    return json.loads(raw)


def render_markdown(offers):
    lines = ["# TokenKey Seller Quote", "", "Snapshot: " + offers["captured_at"], "",
             "Public pricing x " + offers["effective_rate_multiplier"] + ". Uses the existing OpenRouter billing user's authorized seller catalog. Platform admission and model eligibility remain subject to review.", "",
             "USD per million tokens. Display rounded to 9 decimal places; exact decimal amounts and context/time conditions are preserved in seller-offers.json. Rows absent from public pricing are excluded and recorded in the JSON.", "",
             "| Model | Direction / Modality | Price Type | Unit | USD | Conditions |",
             "| --- | --- | --- | --- | --- | --- |"]
    for row in offers["models"]:
        for p in row["seller_prices"]:
            cost = p.get("usd_per_million_tokens", p.get("cost", p.get("cost_usd", "unknown")))
            unit = "million tokens" if "usd_per_million_tokens" in p else p.get("unit", "unknown")
            conditions = {k: v for k, v in p.items() if k not in {"direction", "modality", "type", "unit", "cost", "cost_usd", "usd_per_million_tokens"}}
            detail = json.dumps(conditions, ensure_ascii=False, separators=(",", ":")) if conditions else "base"
            display_cost = format(Decimal(str(cost)), ".9f").rstrip("0").rstrip(".")
            values = [row["model_id"], p["direction"] + "/" + p["modality"], p.get("type", ""), unit, display_cost, detail]
            lines.append("| " + " | ".join(v.replace("|", "\\|").replace("\n", " ") for v in values) + " |")
    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--snapshot", type=Path, help="rebuild from a saved sanitized snapshot instead of prod")
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    snapshot = json.loads(args.snapshot.read_text()) if args.snapshot else fetch_snapshot()
    bundle = json.loads((ROOT / "ops/pricing/model-surface-bundle.json").read_text())
    offers = build_offers(snapshot, bundle)
    args.output_dir.mkdir(parents=True, exist_ok=True)
    for name, payload in (("seller-snapshot.json", snapshot), ("seller-offers.json", offers)):
        (args.output_dir / name).write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n")
    (args.output_dir / "seller-quote.md").write_text(render_markdown(offers))
    print(json.dumps({"billing_user_id": offers["billing_user_id"], "model_count": len(offers["models"]), "output_dir": str(args.output_dir)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
