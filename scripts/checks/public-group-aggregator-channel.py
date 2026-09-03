#!/usr/bin/env python3
"""Gate scheme C: public groups must never host aggregator upstream channels.

OpenRouter provider loop prevention relies on keeping new-api aggregator
channel types (OpenRouter/Coze/Submodel) off public (is_exclusive=false) groups.
Runtime enforcement lives in openrouter_provider_tk_policy.go; this check verifies
the policy wiring and documents the forbidden channel types mechanically.
Ops catalog export: ops/pricing/export-openrouter-provider-models.py (see
ops/pricing/openrouter-provider-onboarding.md).

Exit codes:
  0 — policy anchors present
  1 — drift detected
"""
from __future__ import annotations

import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
POLICY_GO = REPO_ROOT / "backend/internal/service/openrouter_provider_tk_policy.go"
ADMIN_ACCOUNT_GO = REPO_ROOT / "backend/internal/service/admin_account.go"
ROUTE_GO = REPO_ROOT / "backend/internal/server/routes/gateway.go"
CONFIG_EXAMPLE = REPO_ROOT / "ops/pricing/examples/openrouter-provider-config.example.json"

REQUIRED_POLICY_MARKERS = (
    "checkPublicGroupAggregatorChannelPolicy",
    "PublicGroupForbiddenAggregatorChannelTypes",
    "ChannelTypeOpenRouter",
    "ChannelTypeCoze",
    "ChannelTypeSubmodel",
    "scheme C",
)

REQUIRED_OR_PROVIDER_MARKERS = {
    REPO_ROOT / "backend/internal/service/openrouter_provider_tk_model.go": (
        "NormalizeOpenRouterProviderChatBody",
        "InternalModelID",
    ),
    REPO_ROOT / "backend/internal/service/openrouter_provider_tk_schema.go": (
        "OpenRouterProviderPriceOverride",
        "openRouterProviderSchemaVersion",
        "openRouterProviderDefaultQuantization",
    ),
    REPO_ROOT / "ops/pricing/openrouter-provider-onboarding.md": (
        "monitor_api_key_ids",
        "invoicing_contact_email",
        "schema 2.4",
    ),
}

REQUIRED_ROUTE_MARKERS = (
    'Group("/openrouter/v1")',
    "OpenRouterProviderModels",
)

REQUIRED_ADMIN_ACCOUNT_MARKERS = (
    "checkPublicGroupAggregatorChannelPolicy",
)


def _fail(msg: str) -> int:
    print(f"FAIL: {msg}", file=sys.stderr)
    return 1


def main() -> int:
    if not POLICY_GO.is_file():
        return _fail(f"missing policy owner: {POLICY_GO}")
    policy_text = POLICY_GO.read_text(encoding="utf-8")
    for marker in REQUIRED_POLICY_MARKERS:
        if marker not in policy_text:
            return _fail(f"openrouter provider policy missing marker {marker!r}")

    if not ROUTE_GO.is_file():
        return _fail(f"missing route owner: {ROUTE_GO}")
    route_text = ROUTE_GO.read_text(encoding="utf-8")
    for marker in REQUIRED_ROUTE_MARKERS:
        if marker not in route_text:
            return _fail(f"openrouter provider route missing marker {marker!r}")

    if not CONFIG_EXAMPLE.is_file():
        return _fail(f"missing config example: {CONFIG_EXAMPLE}")
    example_text = CONFIG_EXAMPLE.read_text(encoding="utf-8")
    for marker in ("monitor_api_key_ids", "privacy_policy_url", "status_page_url"):
        if marker not in example_text:
            return _fail(f"config example missing marker {marker!r}")

    for path, markers in REQUIRED_OR_PROVIDER_MARKERS.items():
        if not path.is_file():
            return _fail(f"missing openrouter provider owner: {path}")
        text = path.read_text(encoding="utf-8")
        for marker in markers:
            if marker not in text:
                return _fail(f"{path.name} missing marker {marker!r}")

    if not ADMIN_ACCOUNT_GO.is_file():
        return _fail(f"missing admin account owner: {ADMIN_ACCOUNT_GO}")
    admin_text = ADMIN_ACCOUNT_GO.read_text(encoding="utf-8")
    for marker in REQUIRED_ADMIN_ACCOUNT_MARKERS:
        if marker not in admin_text:
            return _fail(f"admin_account.go missing policy call site {marker!r}")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
