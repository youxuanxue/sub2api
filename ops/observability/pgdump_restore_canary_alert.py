#!/usr/bin/env python3
"""Build a deduplicated firing/recovery decision for one restore-canary target."""

from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys
from typing import Any

if __package__:
    from .pgdump_restore_canary_verdict import receipt_problem
else:
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


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", required=True)
    parser.add_argument("--outcome", required=True)
    parser.add_argument("--receipt", type=pathlib.Path, required=True)
    parser.add_argument("--prev-key-file", type=pathlib.Path, required=True)
    parser.add_argument("--run-url", required=True)
    args = parser.parse_args(argv)

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
