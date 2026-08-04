#!/usr/bin/env python3
"""Reproduce the one-time migration into the complete pricing registry.

The comparison is pinned to immutable commits and an exact external-source
digest. It is migration evidence, not a second owner that follows later price
changes.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import pathlib
import re
import subprocess
import sys
import urllib.request
from collections.abc import Mapping
from typing import Any


REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
PRE_MIGRATION_COMMIT = "3ba6f0d0ffdf25a9f93a74d8166ffcf135827497"
INITIAL_REGISTRY_COMMIT = "f729faf22ff207330c4349caa1423470efc23c37"
VALIDATED_REGISTRY_BLOB = "5b4f60317958c7d13732b65b099a11f572867ec3"
EXTERNAL_COMMIT = "b575880c17ce702770d9d2463470899c7f0dd3e0"
EXTERNAL_SHA256 = "f7244f5dc8d9423b93bae92ace97c63750d759b59d0dfe7b90e4a603153b07fa"
EXTERNAL_URL = (
    "https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/"
    f"{EXTERNAL_COMMIT}/model_prices_and_context_window.json"
)
REGISTRY_PATH = "backend/internal/service/tk_pricing_overlay.json"
LEGACY_FALLBACK_PATH = "backend/internal/service/billing_service.go"

SCALAR_DIMENSIONS = (
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
    "long_context_input_token_threshold",
    "long_context_input_cost_multiplier",
    "long_context_output_cost_multiplier",
    "output_cost_per_image",
    "output_cost_per_image_token",
    "input_cost_per_image_token",
    "image_price_1k",
    "image_price_2k",
    "image_price_4k",
    "output_cost_per_second",
)
STRUCTURAL_DIMENSIONS = (
    "intervals",
    "video_price_tiers",
    "default_video_resolution",
)
PRICE_AMOUNT_DIMENSIONS = frozenset(
    field
    for field in SCALAR_DIMENSIONS
    if field
    not in {
        "long_context_input_token_threshold",
        "long_context_input_cost_multiplier",
        "long_context_output_cost_multiplier",
    }
)
EXTERNAL_ENTRY_FIELDS = (
    "input_cost_per_token",
    "output_cost_per_token",
    "output_cost_per_image",
    "output_cost_per_image_token",
    "output_cost_per_second",
)

GO_FIELD_MAP = {
    "InputPricePerToken": "input_cost_per_token",
    "InputPricePerTokenPriority": "input_cost_per_token_priority",
    "OutputPricePerToken": "output_cost_per_token",
    "OutputPricePerTokenPriority": "output_cost_per_token_priority",
    "ThinkingOutputPricePerToken": "thinking_output_cost_per_token",
    "CacheCreationPricePerToken": "cache_creation_input_token_cost",
    "CacheCreationPricePerTokenPriority": "cache_creation_input_token_cost_priority",
    "CacheCreation1hPrice": "cache_creation_input_token_cost_above_1hr",
    "CacheReadPricePerToken": "cache_read_input_token_cost",
    "CacheReadPricePerTokenPriority": "cache_read_input_token_cost_priority",
    "LongContextInputThreshold": "long_context_input_token_threshold",
    "LongContextInputMultiplier": "long_context_input_cost_multiplier",
    "LongContextOutputMultiplier": "long_context_output_cost_multiplier",
    "ImageOutputPricePerToken": "output_cost_per_image_token",
    "ImageInputPricePerToken": "input_cost_per_image_token",
}

APPROVED_DELTAS = {
    "kimi-k2.6": {
        "input_cost_per_token": "approved exact Moonshot CNY/USD=6.7 correction",
        "output_cost_per_token": "approved exact Moonshot CNY/USD=6.7 correction",
        "cache_creation_input_token_cost": "approved exact Moonshot CNY/USD=6.7 correction",
        "cache_read_input_token_cost": "approved exact Moonshot CNY/USD=6.7 correction",
    },
}
OWNER_REDIRECTS = {
    "gpt-5.5-pro": "gpt-5.5",
}


class LegacyOwner:
    def __init__(self, *, source: str, dimensions: dict[str, Any]) -> None:
        self.source = source
        self.dimensions = dimensions


def _json_object(data: bytes, label: str) -> dict[str, Any]:
    value = json.loads(data)
    if not isinstance(value, dict):
        raise ValueError(f"{label} must be a JSON object")
    return value


def _normalize_nested(value: Any) -> Any:
    if isinstance(value, dict):
        return {key: _normalize_nested(value[key]) for key in sorted(value)}
    if isinstance(value, list):
        return [_normalize_nested(item) for item in value]
    return value


def normalize_dimensions(row: Mapping[str, Any]) -> dict[str, Any]:
    dimensions: dict[str, Any] = {}
    for field in SCALAR_DIMENSIONS:
        value = row.get(field)
        if isinstance(value, (int, float)) and not isinstance(value, bool):
            dimensions[field] = value
    for field in STRUCTURAL_DIMENSIONS:
        value = row.get(field)
        if value not in (None, "", []):
            dimensions[field] = _normalize_nested(value)
    return dimensions


def _has_positive_price(dimensions: Mapping[str, Any]) -> bool:
    for field in PRICE_AMOUNT_DIMENSIONS:
        value = dimensions.get(field)
        if isinstance(value, (int, float)) and not isinstance(value, bool) and value > 0:
            return True
    for field in ("intervals", "video_price_tiers"):
        rows = dimensions.get(field)
        if not isinstance(rows, list):
            continue
        for row in rows:
            if not isinstance(row, dict):
                continue
            for key, value in row.items():
                if "cost" in key and isinstance(value, (int, float)) and value > 0:
                    return True
    return False


def _is_external_pricing_entry(row: Any) -> bool:
    return isinstance(row, dict) and any(
        field in row and row[field] is not None for field in EXTERNAL_ENTRY_FIELDS
    )


def build_legacy_direct_owners(
    external: Mapping[str, Any], overlay: Mapping[str, Any]
) -> dict[str, LegacyOwner]:
    """Reproduce parsePricingData plus applyTKPricingOverlay fill-only behavior."""
    owners: dict[str, LegacyOwner] = {}
    for model, row in external.items():
        if model == "sample_spec" or not _is_external_pricing_entry(row):
            continue
        owners[model] = LegacyOwner(
            source="external", dimensions=normalize_dimensions(row)
        )

    for model, row in overlay.items():
        if model.startswith("_") or not isinstance(row, dict):
            continue
        existing = owners.get(model)
        if existing is not None and _has_positive_price(existing.dimensions):
            continue
        owners[model] = LegacyOwner(
            source="overlay_fill", dimensions=normalize_dimensions(row)
        )

    return {
        model: owner
        for model, owner in owners.items()
        if _has_positive_price(owner.dimensions)
    }


def _parse_go_number(expression: str) -> float | int | None:
    expression = expression.strip()
    cny_match = re.fullmatch(
        r"tkCNYPerMTokToUSDPerToken\(([-+0-9.eE]+)\)", expression
    )
    if cny_match:
        return float(cny_match.group(1)) / 6.7 / 1_000_000
    if not re.fullmatch(r"[-+]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][-+]?\d+)?", expression):
        return None
    value = float(expression)
    if value.is_integer() and "e" not in expression.lower() and "." not in expression:
        return int(value)
    return value


def extract_go_fallbacks(source: str) -> dict[str, LegacyOwner]:
    """Extract immutable numeric fallback owners from initFallbackPricing."""
    owners: dict[str, LegacyOwner] = {}
    block_pattern = re.compile(
        r's\.fallbackPrices\["([^"]+)"\]\s*=\s*&ModelPricing\{(.*?)\n\s*\}',
        re.DOTALL,
    )
    for model, body in block_pattern.findall(source):
        dimensions: dict[str, Any] = {}
        for line in body.splitlines():
            field_match = re.match(r"\s*([A-Za-z0-9_]+):\s*([^,]+),", line)
            if not field_match:
                continue
            target = GO_FIELD_MAP.get(field_match.group(1))
            if target is None:
                continue
            value = _parse_go_number(field_match.group(2).split("//", 1)[0])
            if value is not None:
                dimensions[target] = value
        owners[model] = LegacyOwner(source="go_fallback", dimensions=dimensions)

    aliases = re.findall(
        r's\.fallbackPrices\["([^"]+)"\]\s*=\s*s\.fallbackPrices\["([^"]+)"\]',
        source,
    )
    unresolved = list(aliases)
    while unresolved:
        next_unresolved: list[tuple[str, str]] = []
        progressed = False
        for model, target in unresolved:
            owner = owners.get(target)
            if owner is None:
                next_unresolved.append((model, target))
                continue
            owners[model] = LegacyOwner(
                source="go_fallback", dimensions=dict(owner.dimensions)
            )
            progressed = True
        if not progressed:
            break
        unresolved = next_unresolved
    return owners


def _extract_go_constants(source: str) -> dict[str, float | int]:
    constants: dict[str, float | int] = {}
    for name, expression in re.findall(
        r"^\s*([A-Za-z][A-Za-z0-9_]*)\s*=\s*([^\s/]+)", source, re.MULTILINE
    ):
        value = _parse_go_number(expression)
        if value is not None:
            constants[name] = value
    return constants


def extract_go_supplemental_dimensions(source: str) -> dict[str, dict[str, Any]]:
    """Materialize prices owned by legacy billing helpers outside model structs."""
    constants = _extract_go_constants(source)

    def grok_dimensions(one_k: str, two_k: str) -> dict[str, Any]:
        return {
            "image_price_1k": constants[one_k],
            "image_price_2k": constants[two_k],
            "image_price_4k": constants[two_k],
        }

    return {
        "grok-imagine-image": grok_dimensions(
            "defaultGrokImagineImagePrice1K", "defaultGrokImagineImagePrice2K"
        ),
        "grok-imagine-image-quality": grok_dimensions(
            "defaultGrokImagineImageQualityPrice1K",
            "defaultGrokImagineImageQualityPrice2K",
        ),
    }


def build_legacy_global_policy(
    overlay: Mapping[str, Any], source: str
) -> dict[str, Any]:
    config = overlay.get("_config")
    policy = _normalize_nested(config) if isinstance(config, dict) else {}
    constants = _extract_go_constants(source)
    policy.setdefault(
        "web_search_price_per_call", constants["defaultWebSearchPricePerCall"]
    )
    return policy


def materialize_legacy_runtime_dimensions(
    model: str, dimensions: Mapping[str, Any]
) -> dict[str, Any]:
    """Apply legacy billing policies that ran after the selected price row."""
    materialized = dict(dimensions)
    lower = model.lower()
    is_gpt56 = "gpt-5.6" in lower
    uses_legacy_long_context = "gpt-5.5" in lower or (
        "gpt-5.4" in lower and "gpt-5.4-mini" not in lower and "gpt-5.4-nano" not in lower
    )
    if is_gpt56:
        if materialized.get("cache_creation_input_token_cost", 0) <= 0:
            materialized["cache_creation_input_token_cost"] = (
                materialized.get("input_cost_per_token", 0) * 1.25
            )
        if (
            materialized.get("input_cost_per_token_priority", 0) > 0
            and materialized.get("cache_creation_input_token_cost_priority", 0) <= 0
        ):
            materialized["cache_creation_input_token_cost_priority"] = (
                materialized["input_cost_per_token_priority"] * 1.25
            )
    if is_gpt56 or uses_legacy_long_context:
        materialized.setdefault("long_context_input_token_threshold", 272000)
        materialized.setdefault("long_context_input_cost_multiplier", 2.0)
        materialized.setdefault("long_context_output_cost_multiplier", 1.5)
    return materialized


def _values_equal(left: Any, right: Any) -> bool:
    if isinstance(left, (int, float)) and not isinstance(left, bool):
        if not isinstance(right, (int, float)) or isinstance(right, bool):
            return False
        return math.isclose(float(left), float(right), rel_tol=1e-12, abs_tol=1e-18)
    return left == right


def _compare_dimensions(
    model: str,
    legacy: Mapping[str, Any],
    registry: Mapping[str, Any],
    approved: Mapping[str, str],
) -> tuple[list[dict[str, Any]], list[dict[str, Any]], int]:
    approved_deltas: list[dict[str, Any]] = []
    unapproved_deltas: list[dict[str, Any]] = []
    fields = sorted(set(legacy) | set(registry))
    for field in fields:
        old = legacy.get(field, 0)
        new = registry.get(field, 0)
        if _values_equal(old, new):
            continue
        delta = {"model": model, "dimension": field, "legacy": old, "registry": new}
        reason = approved.get(field)
        if reason is not None:
            delta["approval"] = reason
            approved_deltas.append(delta)
        else:
            unapproved_deltas.append(delta)
    return approved_deltas, unapproved_deltas, len(fields)


def compare_migration(
    *,
    external: Mapping[str, Any],
    overlay: Mapping[str, Any],
    legacy_fallbacks: Mapping[str, LegacyOwner],
    registry: Mapping[str, Any],
    approved_deltas: Mapping[str, Mapping[str, str]],
    owner_redirects: Mapping[str, str],
    legacy_supplemental: Mapping[str, Mapping[str, Any]] | None = None,
    legacy_global_policy: Mapping[str, Any] | None = None,
) -> dict[str, Any]:
    legacy_owners = build_legacy_direct_owners(external, overlay)
    for model, owner in legacy_fallbacks.items():
        legacy_owners.setdefault(model, owner)
    for model, dimensions in (legacy_supplemental or {}).items():
        owner = legacy_owners.get(model)
        if owner is not None:
            owner.dimensions.update(dimensions)
    for model, owner in legacy_owners.items():
        owner.dimensions = materialize_legacy_runtime_dimensions(
            model, owner.dimensions
        )

    registry_owners = {
        model: normalize_dimensions(row)
        for model, row in registry.items()
        if not model.startswith("_") and isinstance(row, dict)
    }
    redirect_rows = [
        {"legacy_owner": model, "registry_owner": owner_redirects[model]}
        for model in sorted(owner_redirects)
        if model in legacy_owners and model not in registry_owners
    ]
    redirected = {row["legacy_owner"] for row in redirect_rows}
    missing_registry_owners = sorted(set(legacy_owners) - set(registry_owners) - redirected)
    unexpected_registry_owners = sorted(set(registry_owners) - set(legacy_owners))

    approved_rows: list[dict[str, Any]] = []
    unapproved_rows: list[dict[str, Any]] = []
    compared_dimensions = 0
    common_models = sorted(set(legacy_owners) & set(registry_owners))
    source_counts: dict[str, int] = {}
    for model in common_models:
        legacy_owner = legacy_owners[model]
        source_counts[legacy_owner.source] = source_counts.get(legacy_owner.source, 0) + 1
        registry_dimensions = materialize_legacy_runtime_dimensions(
            model, registry_owners[model]
        )
        approved, unapproved, dimension_count = _compare_dimensions(
            model,
            legacy_owner.dimensions,
            registry_dimensions,
            approved_deltas.get(model, {}),
        )
        approved_rows.extend(approved)
        unapproved_rows.extend(unapproved)
        compared_dimensions += dimension_count

    global_policy_deltas: list[dict[str, Any]] = []
    if legacy_global_policy is not None:
        registry_policy = registry.get("_config", {})
        normalized_legacy_policy = _normalize_nested(dict(legacy_global_policy))
        normalized_registry_policy = _normalize_nested(registry_policy)
        if normalized_legacy_policy != normalized_registry_policy:
            global_policy_deltas.append(
                {
                    "dimension": "_config",
                    "legacy": normalized_legacy_policy,
                    "registry": normalized_registry_policy,
                }
            )

    report: dict[str, Any] = {
        "summary": {
            "legacy_owner_count": len(legacy_owners),
            "registry_owner_count": len(registry_owners),
            "compared_owner_count": len(common_models),
            "compared_dimension_count": compared_dimensions,
            "approved_delta_count": len(approved_rows),
            "unapproved_delta_count": len(unapproved_rows),
            "missing_registry_owner_count": len(missing_registry_owners),
            "unexpected_registry_owner_count": len(unexpected_registry_owners),
            "global_policy_delta_count": len(global_policy_deltas),
        },
        "legacy_owner_source_counts": dict(sorted(source_counts.items())),
        "compared_owners": common_models,
        "compared_dimensions": list(SCALAR_DIMENSIONS + STRUCTURAL_DIMENSIONS),
        "owner_redirects": redirect_rows,
        "approved_deltas": approved_rows,
        "unapproved_deltas": unapproved_rows,
        "missing_registry_owners": missing_registry_owners,
        "unexpected_registry_owners": unexpected_registry_owners,
        "global_policy_deltas": global_policy_deltas,
    }
    report["status"] = "pass" if report_passes(report) else "fail"
    return report


def report_passes(report: Mapping[str, Any]) -> bool:
    summary = report["summary"]
    return all(
        summary[field] == 0
        for field in (
            "unapproved_delta_count",
            "missing_registry_owner_count",
            "unexpected_registry_owner_count",
            "global_policy_delta_count",
        )
    )


def _git_show(commit: str, path: str) -> bytes:
    result = subprocess.run(
        ["git", "show", f"{commit}:{path}"],
        cwd=REPO_ROOT,
        check=True,
        stdout=subprocess.PIPE,
    )
    return result.stdout


def _git_blob(blob: str) -> bytes:
    result = subprocess.run(
        ["git", "cat-file", "blob", blob],
        cwd=REPO_ROOT,
        check=True,
        stdout=subprocess.PIPE,
    )
    return result.stdout


def _sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def _load_external(path: pathlib.Path | None) -> bytes:
    if path is not None:
        return path.read_bytes()
    with urllib.request.urlopen(EXTERNAL_URL, timeout=30) as response:
        return response.read()


def build_immutable_report(external_bytes: bytes) -> dict[str, Any]:
    external_digest = _sha256(external_bytes)
    if external_digest != EXTERNAL_SHA256:
        raise ValueError(
            f"external source digest mismatch: got {external_digest}, want {EXTERNAL_SHA256}"
        )

    overlay_bytes = _git_show(PRE_MIGRATION_COMMIT, REGISTRY_PATH)
    fallback_bytes = _git_show(PRE_MIGRATION_COMMIT, LEGACY_FALLBACK_PATH)
    initial_registry_bytes = _git_show(INITIAL_REGISTRY_COMMIT, REGISTRY_PATH)
    registry_bytes = _git_blob(VALIDATED_REGISTRY_BLOB)
    report = compare_migration(
        external=_json_object(external_bytes, "external source"),
        overlay=_json_object(overlay_bytes, "legacy overlay"),
        legacy_fallbacks=extract_go_fallbacks(fallback_bytes.decode("utf-8")),
        registry=_json_object(registry_bytes, "initial registry"),
        approved_deltas=APPROVED_DELTAS,
        owner_redirects=OWNER_REDIRECTS,
        legacy_supplemental=extract_go_supplemental_dimensions(
            fallback_bytes.decode("utf-8")
        ),
        legacy_global_policy=build_legacy_global_policy(
            _json_object(overlay_bytes, "legacy overlay"),
            fallback_bytes.decode("utf-8"),
        ),
    )
    report["evidence_scope"] = "immutable_initial_migration_only"
    report["sources"] = {
        "pre_migration": {
            "commit": PRE_MIGRATION_COMMIT,
            "overlay_path": REGISTRY_PATH,
            "overlay_sha256": _sha256(overlay_bytes),
            "fallback_path": LEGACY_FALLBACK_PATH,
            "fallback_sha256": _sha256(fallback_bytes),
        },
        "external": {
            "commit": EXTERNAL_COMMIT,
            "url": EXTERNAL_URL,
            "sha256": external_digest,
        },
        "initial_registry": {
            "commit": INITIAL_REGISTRY_COMMIT,
            "path": REGISTRY_PATH,
            "sha256": _sha256(initial_registry_bytes),
        },
        "validated_registry": {
            "git_blob": VALIDATED_REGISTRY_BLOB,
            "path": REGISTRY_PATH,
            "sha256": _sha256(registry_bytes),
            "note": "initial registry with review-found unapproved deltas removed",
        },
    }
    return report


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--external-file",
        type=pathlib.Path,
        help="use cached immutable external bytes (the pinned SHA-256 is still required)",
    )
    parser.add_argument("--output", type=pathlib.Path, help="write the report to this path")
    args = parser.parse_args(argv)

    try:
        report = build_immutable_report(_load_external(args.external_file))
    except (OSError, ValueError, json.JSONDecodeError, subprocess.CalledProcessError) as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2

    rendered = json.dumps(report, ensure_ascii=True, indent=2, sort_keys=True) + "\n"
    if args.output is None:
        sys.stdout.write(rendered)
    else:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(rendered, encoding="utf-8")
        print(f"wrote {args.output}")
    return 0 if report_passes(report) else 1


if __name__ == "__main__":
    raise SystemExit(main())
