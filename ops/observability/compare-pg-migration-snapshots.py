#!/usr/bin/env python3
"""Compare source/standby snapshots without performing a database action."""
from __future__ import annotations

import argparse
import datetime as dt
import json
import math
import re
import sys
from pathlib import Path


def lsn(value: str | None) -> int | None:
    if value is None:
        return None
    if not isinstance(value, str) or not re.fullmatch(r"[0-9A-Fa-f]{1,8}/[0-9A-Fa-f]{1,8}", value):
        raise ValueError("invalid LSN")
    high, low = value.split("/")
    return (int(high, 16) << 32) + int(low, 16)


def load(path: Path) -> dict:
    value = json.loads(path.read_text())
    if not isinstance(value, dict):
        raise ValueError(f"snapshot must be an object: {path}")
    return value


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("source", type=Path)
    parser.add_argument("target", type=Path)
    parser.add_argument("--max-age-seconds", type=float, default=120)
    args = parser.parse_args()
    if not math.isfinite(args.max_age_seconds) or args.max_age_seconds <= 0:
        raise ValueError("max age must be finite and positive")
    source, target = load(args.source), load(args.target)
    now = dt.datetime.now(dt.timezone.utc)
    times = {}
    required = ("kind", "at", "system_identifier", "checkpoint_timeline")
    for label, value in (("source", source), ("target", target)):
        missing = [key for key in required if key not in value]
        if missing:
            raise ValueError(f"{label} missing {','.join(missing)}")
        if value["kind"] != "pg_migration_snapshot":
            raise ValueError(f"{label} is not a migration snapshot")
        if not isinstance(value["system_identifier"], str) or not re.fullmatch(r"[1-9][0-9]*", value["system_identifier"]):
            raise ValueError(f"{label} invalid system identifier")
        timeline = value["checkpoint_timeline"]
        if type(timeline) is not int or not 0 < timeline <= 0xFFFFFFFF:
            raise ValueError(f"{label} invalid timeline")
        if not isinstance(value["at"], str):
            raise ValueError(f"{label} invalid timestamp")
        stamp = dt.datetime.fromisoformat(value["at"])
        if stamp.tzinfo is None or stamp.utcoffset() is None:
            raise ValueError(f"{label} timestamp requires timezone")
        elapsed = (now - stamp).total_seconds()
        if elapsed < -5 or elapsed > args.max_age_seconds:
            raise ValueError(f"{label} stale or future timestamp")
        times[label] = stamp
    if source["system_identifier"] != target["system_identifier"]:
        raise ValueError("system identifier mismatch")
    if source["checkpoint_timeline"] != target["checkpoint_timeline"]:
        raise ValueError("timeline mismatch")
    source_time, target_time = times["source"], times["target"]
    age = abs((target_time - source_time).total_seconds())
    if age > args.max_age_seconds:
        raise ValueError(f"snapshot age gap {age:.1f}s exceeds {args.max_age_seconds:.1f}s")
    current = lsn(source.get("current_lsn"))
    replay = lsn(target.get("replay_lsn"))
    if current is None or replay is None:
        raise ValueError("source current_lsn and target replay_lsn are required")
    if source.get("in_recovery") is not False:
        raise ValueError("source is not a primary")
    if target.get("in_recovery") is not True:
        raise ValueError("target is not in recovery")
    # Signed: a later target sample may have passed the earlier source sample.
    # Never turn this observation into a claim of final catch-up or readiness.
    gap_bytes = current - replay
    print(json.dumps({
        "kind": "pg_migration_snapshot_comparison",
        "source_at": source["at"], "target_at": target["at"],
        "system_identifier": source["system_identifier"],
        "timeline": source["checkpoint_timeline"],
        "age_gap_seconds": age, "source_current_lsn": source["current_lsn"],
        "target_replay_lsn": target["replay_lsn"], "observed_lsn_gap_bytes": gap_bytes,
        "target_last_replay_transaction_at": target.get("last_replay_transaction_at"),
        "source_archive_failed_count": source.get("archiver", {}).get("failed_count"),
        "verdict": "comparable_observations",
        "cutover_authorized": False,
    }, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError) as exc:
        print(f"snapshot comparison rejected: {exc}", file=sys.stderr)
        raise SystemExit(2)
