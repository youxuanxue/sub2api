#!/usr/bin/env python3
"""Shared evidence contract for Fleet pg_dump restore canaries."""

from __future__ import annotations

PRECIOUS_TABLES = (
    "users",
    "accounts",
    "api_keys",
    "groups",
    "settings",
    "usage_billing_dedup",
)


def precious_counts_valid(value: object) -> bool:
    if not isinstance(value, dict) or set(value) != set(PRECIOUS_TABLES):
        return False
    return all(
        not isinstance(count, bool) and isinstance(count, int) and count >= 0
        for count in value.values()
    )
