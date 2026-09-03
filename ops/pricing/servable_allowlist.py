"""Shared parser for marker-delimited native catalog allowlists."""

from __future__ import annotations

import re


ALLOWLIST_PLATFORMS = ("anthropic", "openai", "gemini", "antigravity", "grok")


def parse_allowlist_maps(go_text: str) -> dict[str, set[str]]:
    """Extract model ids from each ``servable-allowlist`` marker block."""
    out: dict[str, set[str]] = {}
    for platform in ALLOWLIST_PLATFORMS:
        match = re.search(
            rf"servable-allowlist:begin {re.escape(platform)}"
            rf"(.*?)servable-allowlist:end {re.escape(platform)}",
            go_text,
            re.S,
        )
        out[platform] = (
            set(re.findall(r'"([^"]+)"\s*:\s*\{\}', match.group(1)))
            if match
            else set()
        )
    return out

