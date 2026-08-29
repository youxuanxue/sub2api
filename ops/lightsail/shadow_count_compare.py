#!/usr/bin/env python3
"""Compare precious-table count blobs after a shadow restore."""

from __future__ import annotations

import argparse
import sys

IDENTITY_TABLES = ("users", "accounts", "api_keys", "groups", "settings")


def parse_counts(raw: str) -> dict[str, int]:
    out: dict[str, int] = {}
    for part in raw.split(","):
        if not part or "=" not in part:
            raise ValueError(f"bad count blob: {raw!r}")
        key, value = part.split("=", 1)
        out[key] = int(value)
    return out


def billing_dedup_slack(base: int) -> int:
    # Busy edges write this table during dump+restore. Keep a floor of 20, then
    # allow 0.01% of the larger snapshot so a 751k-row ledger can drift ~24 rows.
    return max(20, base // 10000)


def compare_identity(dump: dict[str, int], shadow: dict[str, int]) -> None:
    for key in IDENTITY_TABLES:
        if dump.get(key) != shadow.get(key):
            raise SystemExit(
                f"identity table {key} dump={dump.get(key)} shadow={shadow.get(key)}"
            )


def compare_billing(dump: dict[str, int], shadow: dict[str, int]) -> tuple[int, int]:
    delta = abs(dump.get("usage_billing_dedup", -1) - shadow.get("usage_billing_dedup", -2))
    base = max(dump.get("usage_billing_dedup", 0), shadow.get("usage_billing_dedup", 0))
    slack = billing_dedup_slack(base)
    if delta > slack:
        raise SystemExit(f"usage_billing_dedup drift {delta} exceeds slack {slack}")
    return delta, slack


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("dump")
    parser.add_argument("shadow")
    parser.add_argument("--billing", action="store_true")
    args = parser.parse_args(argv)
    try:
        dump = parse_counts(args.dump)
        shadow = parse_counts(args.shadow)
    except ValueError as exc:
        raise SystemExit(str(exc)) from exc
    compare_identity(dump, shadow)
    if args.billing:
        delta, slack = compare_billing(dump, shadow)
        print(f"precious identity match; billing_dedup_delta={delta} slack={slack}")
    else:
        print("precious identity match")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
