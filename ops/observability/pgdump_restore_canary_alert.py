#!/usr/bin/env python3
"""Build and deliver a deduplicated firing/recovery decision for one restore-canary target."""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import re
import sys
from typing import Any

if __package__:
    from .edge_health_delivery import DeliveryError, _atomic_write, post_feishu
    from .pgdump_restore_canary_verdict import receipt_problem
else:
    from edge_health_delivery import DeliveryError, _atomic_write, post_feishu
    from pgdump_restore_canary_verdict import receipt_problem


def _receipt_valid(target: str, receipt: object) -> bool:
    return receipt_problem(receipt, target) is None


def build_decision(
    target: str,
    outcome: str,
    receipt: object,
    prev_key: str,
    run_url: str,
) -> dict[str, Any]:
    if outcome != "success":
        reason = f"job-{re.sub(r'[^a-z0-9-]+', '-', outcome.lower()).strip('-') or 'failed'}"
    elif receipt is None:
        reason = "missing-receipt"
    elif not _receipt_valid(target, receipt):
        reason = "invalid-receipt"
    else:
        reason = ""

    prefix = f"pgdump:{target}:"
    if reason:
        key = f"{prefix}firing:{reason}"
        return {
            "should_alert": key != prev_key,
            "key": key,
            "message": (
                f"TokenKey pg_dump 恢复演练失败\n"
                f"目标: {target}\n原因: {reason}\n运行: {run_url}"
            ),
        }

    key = f"{prefix}healthy"
    recovering = prev_key.startswith(f"{prefix}firing:")
    return {
        "should_alert": recovering,
        "key": key,
        "message": (
            f"TokenKey pg_dump 恢复演练已恢复\n"
            f"目标: {target}\n真实 S3 恢复对象已还原，临时资源清理已验证\n"
            f"运行: {run_url}"
        ) if recovering else "",
    }


def apply_key_decision(
    decision: dict[str, Any],
    *,
    key_file: pathlib.Path,
    dry_run: bool = False,
    webhook_url: str = "",
    signing_secret: str = "",
) -> str:
    """Post one Feishu alert when needed, then persist only the canary key."""
    should_alert = decision.get("should_alert")
    message = decision.get("message")
    key = decision.get("key")
    if not isinstance(should_alert, bool) or not isinstance(message, str) or not isinstance(key, str) or not key:
        raise DeliveryError("decision must contain boolean should_alert, string message, and non-empty key")

    if dry_run:
        if should_alert:
            print(message)
        return "dry-run"

    if should_alert:
        if not message:
            raise DeliveryError("alert decision has an empty message")
        post_feishu(message, webhook_url=webhook_url, signing_secret=signing_secret)

    _atomic_write(key_file, key + "\n")
    return "delivered" if should_alert else "unchanged"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--deliver", action="store_true")
    parser.add_argument("--decision", type=pathlib.Path)
    parser.add_argument("--key-file", type=pathlib.Path)
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--target")
    parser.add_argument("--outcome")
    parser.add_argument("--receipt", type=pathlib.Path)
    parser.add_argument("--prev-key-file", type=pathlib.Path)
    parser.add_argument("--run-url")
    args = parser.parse_args(argv)

    if args.deliver:
        if args.decision is None or args.key_file is None:
            parser.error("--deliver requires --decision and --key-file")
        try:
            decision = json.loads(args.decision.read_text(encoding="utf-8"))
            result = apply_key_decision(
                decision,
                key_file=args.key_file,
                dry_run=args.dry_run,
                webhook_url=os.environ.get("FEISHU_WEBHOOK_URL", ""),
                signing_secret=os.environ.get("FEISHU_SIGNING_SECRET", ""),
            )
        except (OSError, json.JSONDecodeError, DeliveryError) as exc:
            print(f"restore-canary delivery failed: {exc}", file=sys.stderr)
            return 1
        print(f"restore-canary delivery: {result}")
        return 0

    missing = [
        name
        for name, value in (
            ("--target", args.target),
            ("--outcome", args.outcome),
            ("--receipt", args.receipt),
            ("--prev-key-file", args.prev_key_file),
            ("--run-url", args.run_url),
        )
        if value is None
    ]
    if missing:
        parser.error("decide mode requires " + ", ".join(missing))

    receipt: object = None
    if args.receipt.is_file():
        try:
            receipt = json.loads(args.receipt.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            receipt = {}
    try:
        prev_key = args.prev_key_file.read_text(encoding="utf-8").strip()
    except FileNotFoundError:
        prev_key = ""
    except OSError as exc:
        print(f"cannot read previous restore-canary state: {exc}", file=sys.stderr)
        return 1

    decision = build_decision(args.target, args.outcome, receipt, prev_key, args.run_url)
    print(json.dumps(decision, ensure_ascii=True, separators=(",", ":"), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
