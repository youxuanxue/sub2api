#!/usr/bin/env python3
"""Audit complete-registry catalog expectations against live ``/pricing``.

The repository registry is the only editable global price owner. This audit is
the read-only deployment close-out: every native model selected by the empirical
allowlist and resolvable through a direct registry owner or ``_aliases`` must be
present in the live public catalog. Paid rows must expose a non-zero price;
``explicit_free`` rows must expose the row itself.

``check`` without ``--live`` validates the local projection inputs. ``--live``
also fetches the public catalog and reports expected rows missing from that
runtime projection. Static ownership is enforced by
``scripts/checks/catalog-serving-drift.py``.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import urllib.request
from pathlib import Path


REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO / "ops" / "pricing"))
from servable_allowlist import ALLOWLIST_PLATFORMS, parse_allowlist_maps
GO_ALLOWLIST = REPO / "backend/internal/service/pricing_catalog_supported_models_tk.go"
REGISTRY = REPO / "backend/internal/service/tk_pricing_overlay.json"
DEFAULT_BASE = os.environ.get("TOKENKEY_BASE_URL", "https://api.tokenkey.dev")
PLATFORMS = ALLOWLIST_PLATFORMS


def parse_allowlist(go_text: str) -> dict[str, set[str]]:
    return parse_allowlist_maps(go_text)


def registry_entry_catalog_valid(entry: object) -> bool:
    if not isinstance(entry, dict):
        return False
    if entry.get("explicit_free") is True or entry.get("intervals"):
        return True
    return any(
        (entry.get(field) or 0) > 0
        for field in (
            "input_cost_per_token",
            "output_cost_per_token",
            "output_cost_per_image",
            "output_cost_per_second",
        )
    )


def registry_catalog_ids(registry: dict) -> tuple[set[str], set[str]]:
    owners = {
        model
        for model, entry in registry.items()
        if not model.startswith("_") and registry_entry_catalog_valid(entry)
    }
    free_owners = {
        model
        for model, entry in registry.items()
        if not model.startswith("_")
        and isinstance(entry, dict)
        and entry.get("explicit_free") is True
    }
    aliases = registry.get("_aliases") or {}
    if not isinstance(aliases, dict):
        raise ValueError("registry _aliases must be an object")
    catalog_aliases = {
        alias
        for alias, owner in aliases.items()
        if isinstance(alias, str) and isinstance(owner, str) and owner in owners
    }
    free_aliases = {
        alias
        for alias, owner in aliases.items()
        if isinstance(alias, str) and isinstance(owner, str) and owner in free_owners
    }
    return owners | catalog_aliases, free_owners | free_aliases


def expected_projection(
    allowlist: dict[str, set[str]], catalog_ids: set[str]
) -> tuple[dict[str, set[str]], dict[str, set[str]]]:
    expected: dict[str, set[str]] = {}
    unbacked: dict[str, set[str]] = {}
    for platform, models in allowlist.items():
        expected[platform] = models & catalog_ids
        missing = models - catalog_ids
        if missing:
            unbacked[platform] = missing
    return expected, unbacked


def live_row_priced(row: object) -> bool:
    if not isinstance(row, dict):
        return False
    pricing = row.get("pricing") or {}
    if not isinstance(pricing, dict):
        return False
    tiers = pricing.get("tiers") or []
    candidates = list(tiers) if isinstance(tiers, list) else []
    candidates.append(pricing)
    return any(
        isinstance(candidate, dict)
        and any(
            (candidate.get(field) or 0) > 0
            for field in (
                "input_per_1k_tokens",
                "output_per_1k_tokens",
                "output_cost_per_image",
                "output_cost_per_second",
            )
        )
        for candidate in candidates
    )


def live_covered_ids(payload: dict, free_ids: set[str]) -> set[str]:
    return {
        str(row.get("model_id") or row.get("id"))
        for row in payload.get("data", [])
        if isinstance(row, dict)
        and (row.get("model_id") or row.get("id"))
        and (
            live_row_priced(row)
            or str(row.get("model_id") or row.get("id")) in free_ids
        )
    }


def fetch_live(base_url: str) -> dict:
    url = base_url.rstrip("/") + "/api/v1/public/pricing"
    with urllib.request.urlopen(url, timeout=25) as response:  # noqa: S310 operator URL
        return json.loads(response.read().decode())


def projection_gaps(expected: dict[str, set[str]], live: set[str]) -> dict[str, list[str]]:
    return {
        platform: sorted(models - live)
        for platform, models in expected.items()
        if models - live
    }


def cmd_check(args: argparse.Namespace) -> int:
    try:
        allowlist = parse_allowlist(GO_ALLOWLIST.read_text(encoding="utf-8"))
        registry = json.loads(REGISTRY.read_text(encoding="utf-8"))
        catalog_ids, free_ids = registry_catalog_ids(registry)
        expected, unbacked = expected_projection(allowlist, catalog_ids)
    except Exception as exc:  # noqa: BLE001
        print(f"ERROR: cannot build local catalog expectation: {exc}", file=sys.stderr)
        return 2

    if args.platform:
        expected = {args.platform: expected.get(args.platform, set())}
        unbacked = (
            {args.platform: unbacked.get(args.platform, set())}
            if args.platform in unbacked
            else {}
        )
    if unbacked:
        for platform, models in sorted(unbacked.items()):
            for model in sorted(models):
                print(
                    f"GAP {platform}/{model}: allowlisted without catalog-valid registry backing",
                    file=sys.stderr,
                )
        print("fix: run scripts/checks/catalog-serving-drift.py for the canonical static finding", file=sys.stderr)
        return 1

    if not args.live:
        total = sum(len(models) for models in expected.values())
        print(f"display projection inputs: PASS ({total} registry-backed native rows)")
        return 0

    try:
        live = live_covered_ids(fetch_live(args.base_url), free_ids)
    except Exception as exc:  # noqa: BLE001
        print(f"ERROR: live /pricing fetch failed: {exc}", file=sys.stderr)
        return 2
    gaps = projection_gaps(expected, live)
    for platform, models in sorted(gaps.items()):
        for model in models:
            print(
                f"GAP {platform}/{model}: expected from registry+allowlist, absent/invalid live",
                file=sys.stderr,
            )
    total = sum(len(models) for models in expected.values())
    missing = sum(len(models) for models in gaps.values())
    print(f"display projection audit: {missing} gap(s) across {total} expected native rows")
    return 1 if gaps else 0


def cmd_selftest(_args: argparse.Namespace) -> int:
    go = (
        "// servable-allowlist:begin openai\n"
        '\t"gpt-owner": {},\n\t"gpt-alias": {},\n\t"free-owner": {},\n'
        '\t"free-alias": {},\n\t"gpt-missing": {},\n'
        "// servable-allowlist:end openai\n"
    )
    allowlist = parse_allowlist(go)
    registry = {
        "gpt-owner": {"input_cost_per_token": 1e-6},
        "free-owner": {"explicit_free": True},
        "_aliases": {
            "gpt-alias": "gpt-owner",
            "free-alias": "free-owner",
        },
    }
    catalog_ids, free_ids = registry_catalog_ids(registry)
    expected, unbacked = expected_projection(allowlist, catalog_ids)
    assert expected["openai"] == {
        "gpt-owner", "gpt-alias", "free-owner", "free-alias"
    }, expected
    assert free_ids == {"free-owner", "free-alias"}, free_ids
    assert unbacked == {"openai": {"gpt-missing"}}, unbacked
    payload = {
        "data": [
            {"model_id": "gpt-owner", "pricing": {"input_per_1k_tokens": 0.001}},
            {"model_id": "image", "pricing": {"output_cost_per_image": 0.04}},
            {"model_id": "free-owner", "pricing": {"input_per_1k_tokens": 0}},
            {"model_id": "free-alias", "pricing": {"output_per_1k_tokens": 0}},
            {"model_id": "zero", "pricing": {"input_per_1k_tokens": 0}},
        ]
    }
    live = live_covered_ids(payload, free_ids)
    assert live == {"gpt-owner", "image", "free-owner", "free-alias"}, live
    assert projection_gaps(expected, live) == {"openai": ["gpt-alias"]}
    print("audit-display-coverage selftest: PASS")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    check = sub.add_parser("check")
    check.add_argument("--live", action="store_true")
    check.add_argument("--base-url", default=DEFAULT_BASE)
    check.add_argument("--platform", choices=PLATFORMS)
    check.set_defaults(func=cmd_check)
    selftest = sub.add_parser("selftest")
    selftest.set_defaults(func=cmd_selftest)
    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
