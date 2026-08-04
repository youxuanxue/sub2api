#!/usr/bin/env python3
"""Compare provider/LiteLLM evidence with the TokenKey pricing registry.

This program is deliberately disconnected from runtime publication. It emits a
deterministic report and may materialize price-field updates for owners that
already exist in the registry. Unknown models, ambiguous provider aliases, and
known requested-to-routed aliases remain report-only pending human review.
"""
from __future__ import annotations

import argparse
import copy
import hashlib
import json
import math
import sys
import urllib.request
from decimal import Decimal, InvalidOperation
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
REGISTRY_PATH = REPO_ROOT / "backend" / "internal" / "service" / "tk_pricing_overlay.json"
DEFAULT_SOURCE = (
    "https://raw.githubusercontent.com/BerriAI/litellm/main/"
    "model_prices_and_context_window.json"
)

# Only dimensions consumed by TokenKey settlement may be proposed automatically,
# and only for an owner that already exists. Metadata drift is still reported.
CANDIDATE_FIELDS = frozenset({
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
    "intervals",
    "video_price_tiers",
    "default_video_resolution",
})

REPORT_ONLY_MODELS = {
    "gpt-5.5-pro": "requested model routes and bills through registry owner gpt-5.5",
}

_METADATA_FIELDS = frozenset({
    "litellm_provider",
    "mode",
    "max_input_tokens",
    "max_output_tokens",
    "max_tokens",
    "supported_endpoints",
    "supported_modalities",
    "supported_output_modalities",
    "deprecation_date",
    "failure_billing",
    "explicit_free",
})


def _is_relevant_field(field: str) -> bool:
    return (
        field in CANDIDATE_FIELDS
        or field in _METADATA_FIELDS
        or field.startswith("supports_")
        or "cost" in field
        or "price" in field
        or field.endswith("_multiplier")
    )


def _json_equal(left: Any, right: Any) -> bool:
    if isinstance(left, bool) or isinstance(right, bool):
        return left is right
    if isinstance(left, (int, float)) and isinstance(right, (int, float)):
        if not math.isfinite(float(left)) or not math.isfinite(float(right)):
            return False
        try:
            return Decimal(str(left)) == Decimal(str(right))
        except InvalidOperation:
            return False
    return left == right


def _bare_source_model(source_key: str) -> str:
    return source_key.rsplit("/", 1)[-1].strip().lower()


def _select_evidence(registry: dict, source: dict) -> tuple[dict[str, tuple[str, dict]], list[dict]]:
    candidates: dict[str, list[tuple[bool, str, dict]]] = {}
    report_only: list[dict] = []
    for source_key in sorted(source):
        row = source[source_key]
        if not isinstance(source_key, str) or not isinstance(row, dict):
            continue
        bare = _bare_source_model(source_key)
        if bare in REPORT_ONLY_MODELS and bare not in registry:
            report_only.append({
                "source_key": source_key,
                "normalized_model": bare,
                "reason": REPORT_ONLY_MODELS[bare],
            })
            continue
        if bare not in registry or bare.startswith("_"):
            report_only.append({
                "source_key": source_key,
                "normalized_model": bare,
                "reason": "no existing registry owner; requested/routed/billing relationship requires review",
            })
            continue
        owner = registry[bare]
        owner_provider = owner.get("litellm_provider") if isinstance(owner, dict) else None
        evidence_provider = row.get("litellm_provider")
        provider_matches = (
            isinstance(owner, dict)
            and isinstance(owner_provider, str)
            and bool(owner_provider)
            and evidence_provider == owner_provider
        )
        candidates.setdefault(bare, []).append((provider_matches, source_key, row))

    selected: dict[str, tuple[str, dict]] = {}
    for owner, rows in candidates.items():
        matching = [item for item in rows if item[0]]
        if not matching:
            registry_provider = registry[owner].get("litellm_provider")
            for _, source_key, row in rows:
                report_only.append({
                    "source_key": source_key,
                    "normalized_model": owner,
                    "reason": (
                        "provider mismatch; "
                        f"registry={registry_provider!r}, "
                        f"evidence={row.get('litellm_provider')!r}"
                    ),
                })
            continue
        matching.sort(key=lambda item: (0 if item[1] == owner else 1, item[1]))
        _, source_key, row = matching[0]
        selected[owner] = (source_key, row)
        for provider_matches, alternate_key, alternate_row in rows:
            if alternate_key == source_key:
                continue
            reason = f"alternate evidence; selected {source_key}"
            if not provider_matches:
                reason = (
                    "provider mismatch; "
                    f"registry={registry[owner].get('litellm_provider')!r}, "
                    f"evidence={alternate_row.get('litellm_provider')!r}"
                )
            report_only.append({
                "source_key": alternate_key,
                "normalized_model": owner,
                "reason": reason,
            })
    report_only.sort(key=lambda item: (item["normalized_model"], item["source_key"]))
    return selected, report_only


def build_report(registry: dict, source: dict, *, source_label: str) -> dict:
    selected, report_only = _select_evidence(registry, source)
    owner_drifts: list[dict] = []
    actionable_owners: set[str] = set()
    for owner in sorted(selected):
        source_key, evidence = selected[owner]
        registry_row = registry[owner]
        fields: list[dict] = []
        for field in sorted(evidence):
            if not _is_relevant_field(field):
                continue
            registry_value = registry_row.get(field)
            evidence_value = evidence[field]
            if _json_equal(registry_value, evidence_value):
                continue
            actionable = field in CANDIDATE_FIELDS
            if actionable:
                actionable_owners.add(owner)
            fields.append({
                "field": field,
                "registry": registry_value,
                "evidence": evidence_value,
                "actionable": actionable,
            })
        if fields:
            owner_drifts.append({
                "owner": owner,
                "source_key": source_key,
                "fields": fields,
            })

    registry_owners = sorted(
        key for key, value in registry.items()
        if not key.startswith("_") and isinstance(value, dict)
    )
    registry_without_evidence = sorted(set(registry_owners) - set(selected))
    report = {
        "schema_version": 1,
        "source": source_label,
        "registry_sha256": hashlib.sha256(
            (json.dumps(registry, indent=2, ensure_ascii=False) + "\n").encode("utf-8")
        ).hexdigest(),
        "summary": {
            "registry_owner_count": len(registry_owners),
            "selected_evidence_count": len(selected),
            "drift_owner_count": len(owner_drifts),
            "actionable_owner_count": len(actionable_owners),
            "report_only_count": len(report_only),
            "registry_without_evidence_count": len(registry_without_evidence),
            "has_drift": bool(owner_drifts or report_only),
        },
        "owner_drifts": owner_drifts,
        "report_only_evidence": report_only,
        "registry_without_evidence": registry_without_evidence,
    }
    return report


def build_candidate_registry(registry: dict, report: dict) -> tuple[dict, list[str]]:
    candidate = copy.deepcopy(registry)
    changed_owners: list[str] = []
    for drift in report["owner_drifts"]:
        owner = drift["owner"]
        changed = False
        for field in drift["fields"]:
            if not field["actionable"]:
                continue
            candidate[owner][field["field"]] = copy.deepcopy(field["evidence"])
            changed = True
        if changed:
            changed_owners.append(owner)
    return candidate, sorted(changed_owners)


def render_markdown(report: dict) -> str:
    summary = report["summary"]
    lines = [
        "# Pricing registry sensor report",
        "",
        f"Source: `{report['source']}`",
        "",
        "| Result | Count |",
        "| --- | ---: |",
        f"| Registry owners | {summary['registry_owner_count']} |",
        f"| Selected evidence rows | {summary['selected_evidence_count']} |",
        f"| Owners with drift | {summary['drift_owner_count']} |",
        f"| Owners with candidate price changes | {summary['actionable_owner_count']} |",
        f"| Report-only evidence rows | {summary['report_only_count']} |",
        "",
    ]
    if report["owner_drifts"]:
        lines.extend(["## Owner drift", ""])
        for drift in report["owner_drifts"]:
            fields = ", ".join(
                f"`{item['field']}`" + ("" if item["actionable"] else " (report-only)")
                for item in drift["fields"]
            )
            lines.append(
                f"- `{drift['owner']}` from `{drift['source_key']}`: {fields}"
            )
        lines.append("")
    if report["report_only_evidence"]:
        lines.extend(["## Report-only evidence", ""])
        for item in report["report_only_evidence"]:
            lines.append(
                f"- `{item['source_key']}` -> `{item['normalized_model']}`: {item['reason']}"
            )
        lines.append("")
    lines.extend([
        "Candidate changes are sensor evidence only. Human review and protected-main merge are required; this workflow never publishes runtime pricing.",
        "",
    ])
    return "\n".join(lines)


def _load_json(location: str) -> dict:
    if location.startswith(("https://", "http://")):
        request = urllib.request.Request(location, headers={"User-Agent": "tokenkey-pricing-sensor/1"})
        with urllib.request.urlopen(request, timeout=30) as response:
            data = response.read()
    else:
        path = Path(location)
        if not path.is_absolute():
            path = REPO_ROOT / path
        data = path.read_bytes()
    value = json.loads(data)
    if not isinstance(value, dict):
        raise ValueError(f"{location}: top-level JSON must be an object")
    return value


def _write_text(path_value: str, content: str) -> None:
    path = Path(path_value)
    if not path.is_absolute():
        path = REPO_ROOT / path
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--registry", default=str(REGISTRY_PATH))
    parser.add_argument("--source", default=DEFAULT_SOURCE)
    parser.add_argument("--report-json")
    parser.add_argument("--report-md")
    parser.add_argument("--candidate-registry")
    args = parser.parse_args(argv)

    registry = _load_json(args.registry)
    source = _load_json(args.source)
    report = build_report(registry, source, source_label=args.source)
    candidate, changed_owners = build_candidate_registry(registry, report)
    report["summary"]["candidate_owner_count"] = len(changed_owners)

    if args.report_json:
        _write_text(args.report_json, json.dumps(report, indent=2, ensure_ascii=False) + "\n")
    markdown = render_markdown(report)
    if args.report_md:
        _write_text(args.report_md, markdown)
    if args.candidate_registry:
        _write_text(
            args.candidate_registry,
            json.dumps(candidate, indent=2, ensure_ascii=False) + "\n",
        )

    summary = report["summary"]
    print(
        "pricing sensor: "
        f"drift_owners={summary['drift_owner_count']} "
        f"candidate_owners={len(changed_owners)} "
        f"report_only={summary['report_only_count']}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
