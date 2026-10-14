#!/usr/bin/env python3
"""Check adapted Stage0 Caddy JSON, where imports/defaults have been resolved."""
from __future__ import annotations

import json
import sys


def check_policy(config: object) -> None:
    proxies = []

    def visit(value: object) -> None:
        if isinstance(value, dict):
            if value.get("handler") == "reverse_proxy":
                proxies.append(value)
            for child in value.values():
                visit(child)
        elif isinstance(value, list):
            for child in value:
                visit(child)

    visit(config)
    if not proxies:
        raise ValueError("Stage0 config has no reverse proxy")
    for proxy in proxies:
        if len(proxy.get("upstreams", [])) != 1:
            raise ValueError("Stage0 health policy requires one active backend")
        health = proxy.get("health_checks", {})
        if "passive" in health:
            raise ValueError("passive health can spread client/upload failures to unrelated requests")
        active = health.get("active", {})
        if (active.get("uri"), active.get("interval"), active.get("timeout")) != (
            "/health", 5_000_000_000, 3_000_000_000
        ):
            raise ValueError("drain-aware /health checks must remain enabled at 5s/3s")
        balancing = proxy.get("load_balancing", {})
        if (balancing.get("try_duration"), balancing.get("try_interval")) != (
            30_000_000_000, 1_000_000_000
        ):
            raise ValueError("bounded 30s/1s downtime retries must remain enabled")


if __name__ == "__main__":
    try:
        with open(sys.argv[1], encoding="utf-8") as handle:
            check_policy(json.load(handle))
    except (ValueError, OSError) as error:
        sys.exit(f"FAIL: Stage0 Caddy health policy: {error}")
