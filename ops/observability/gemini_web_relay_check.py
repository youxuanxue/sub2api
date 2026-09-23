#!/usr/bin/env python3
"""Validate declared Gemini Web relays without guessing capabilities from URLs/models."""
from __future__ import annotations

import json
import sys


def evaluate(snapshot: dict) -> dict:
    accounts = snapshot.get("accounts")
    if not isinstance(accounts, list):
        raise ValueError("accounts must be a list")
    violations = []
    for account in accounts:
        if (
            not isinstance(account, dict)
            or type(account.get("id")) is not int
            or not isinstance(account.get("platform"), str)
            or not isinstance(account.get("type"), str)
            or type(account.get("declared_web")) is not bool
            or "marker" not in account
        ):
            raise ValueError("invalid account projection")
        marker = account["marker"]
        reasons = []
        if account["declared_web"] and marker is not True:
            reasons.append("missing_web_relay_capability")
        if marker is not None and type(marker) is not bool:
            reasons.append("invalid_web_relay_capability_type")
        if (account["declared_web"] or marker is True) and (
            account["platform"] != "gemini" or account["type"] != "apikey"
        ):
            reasons.append("invalid_web_relay_account_type")
        if reasons:
            violations.append({"account_id": account["id"], "reasons": reasons})
    return {
        "verdict": "review" if violations else "aligned",
        "accounts_checked": len(accounts),
        "violations": sorted(violations, key=lambda item: item["account_id"]),
    }


def main() -> int:
    try:
        snapshot = json.load(sys.stdin)
        if not isinstance(snapshot, dict):
            raise ValueError("snapshot must be an object")
        report = evaluate(snapshot)
    except (ValueError, TypeError):
        print(json.dumps({"verdict": "setup_error", "error": "invalid_snapshot"}))
        return 2
    print(json.dumps(report, sort_keys=True))
    return 0 if report["verdict"] == "aligned" else 1


if __name__ == "__main__":
    raise SystemExit(main())
