#!/usr/bin/env python3
"""Mechanical direct-push vs bump-via-pr routing for VERSION bumps on main."""
from __future__ import annotations

import json
import os
import sys
from typing import Any


def decide_route(protection: dict[str, Any], meta: dict[str, Any], current_user: str) -> str:
    """Return 'direct-push' or 'bump-via-pr' for the given gh actor."""
    current_user = (current_user or "").strip()
    if meta.get("owner_type") == "Organization":
        reviews = protection.get("required_pull_request_reviews") or {}
        allow = reviews.get("bypass_pull_request_allowances") or {}
        logins: list[str] = []
        for item in allow.get("users") or []:
            if isinstance(item, dict) and item.get("login"):
                logins.append(str(item["login"]))
            elif isinstance(item, str):
                logins.append(item)
        if current_user in logins:
            return "direct-push"
        return "bump-via-pr"

    enforce_admins = bool((protection.get("enforce_admins") or {}).get("enabled"))
    if not enforce_admins and meta.get("admin"):
        return "direct-push"
    return "bump-via-pr"


def main() -> int:
    try:
        protection = json.loads(os.environ["PROTECTION_JSON"])
        meta = json.loads(os.environ["META_JSON"])
        current_user = os.environ["CURRENT_USER"]
    except (KeyError, json.JSONDecodeError) as exc:
        print(f"[release-main-push-route] ERROR: {exc}", file=sys.stderr)
        return 2
    print(decide_route(protection, meta, current_user))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
