#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime as dt
import json
import sys


SCHEMA_VERSION = 1
STALE_AFTER = dt.timedelta(minutes=15)


class ProbeContractError(ValueError):
    pass


def _timestamp(value: object) -> dt.datetime:
    if not isinstance(value, str) or not value.strip():
        raise ProbeContractError("timestamp must be a non-empty string")
    try:
        parsed = dt.datetime.fromisoformat(value.strip().replace("Z", "+00:00"))
    except ValueError as exc:
        raise ProbeContractError(f"invalid timestamp {value!r}") from exc
    if parsed.tzinfo is None:
        raise ProbeContractError("timestamp must include timezone")
    return parsed.astimezone(dt.timezone.utc)


def _tagged_rows(raw: str) -> dict[str, list[dict]]:
    rows = {"TERMINAL_META": [], "TERMINAL_WINDOW": [], "TERMINAL_FACT": []}
    for number, line in enumerate(raw.splitlines(), 1):
        tag, separator, payload = line.strip().partition(" ")
        if tag not in rows:
            continue
        if not separator:
            raise ProbeContractError(f"line {number}: missing JSON payload")
        try:
            value = json.loads(payload)
        except json.JSONDecodeError as exc:
            raise ProbeContractError(f"line {number}: invalid JSON") from exc
        if not isinstance(value, dict):
            raise ProbeContractError(f"line {number}: payload must be an object")
        rows[tag].append(value)
    return rows


def parse_probe_output(raw: str, label: str, *, now: dt.datetime | None = None) -> dict:
    rows = _tagged_rows(raw)
    if len(rows["TERMINAL_META"]) != 1:
        raise ProbeContractError("exactly one TERMINAL_META row is required")
    if not rows["TERMINAL_WINDOW"]:
        raise ProbeContractError("at least one TERMINAL_WINDOW row is required")

    meta = rows["TERMINAL_META"][0]
    if meta.get("schema_version") != SCHEMA_VERSION:
        raise ProbeContractError("unsupported terminal schema version")
    watermark = _timestamp(meta.get("watermark"))
    evaluated_at = (now or dt.datetime.now(dt.timezone.utc)).astimezone(dt.timezone.utc)

    buckets: dict[str, dict] = {}
    for window in rows["TERMINAL_WINDOW"]:
        bucket_at = _timestamp(window.get("bucket_start"))
        bucket_key = bucket_at.isoformat().replace("+00:00", "Z")
        if bucket_key in buckets:
            raise ProbeContractError(f"duplicate terminal window {bucket_key}")
        heartbeat_minutes = window.get("heartbeat_minutes")
        producer_epochs = window.get("producer_epochs")
        all_complete = window.get("all_complete")
        if not isinstance(heartbeat_minutes, int) or not isinstance(producer_epochs, int) or not isinstance(all_complete, bool):
            raise ProbeContractError(f"invalid terminal window fields for {bucket_key}")
        buckets[bucket_key] = {
            "bucket_start": bucket_key,
            "complete": heartbeat_minutes == 5 and producer_epochs == 1 and all_complete,
            "heartbeat_minutes": heartbeat_minutes,
            "producer_epochs": producer_epochs,
            "facts": [],
        }

    for fact in rows["TERMINAL_FACT"]:
        bucket_key = _timestamp(fact.get("bucket_start")).isoformat().replace("+00:00", "Z")
        if bucket_key not in buckets:
            raise ProbeContractError(f"fact references unknown terminal window {bucket_key}")
        requested_model = fact.get("requested_model")
        group_id = fact.get("group_id")
        counts = [fact.get("success"), fact.get("final_empty_pool_429"), fact.get("other_error")]
        if not isinstance(requested_model, str) or not requested_model.strip() or not isinstance(group_id, int):
            raise ProbeContractError(f"invalid terminal fact identity for {bucket_key}")
        if any(not isinstance(value, int) or value < 0 for value in counts):
            raise ProbeContractError(f"invalid terminal fact counts for {bucket_key}")
        buckets[bucket_key]["facts"].append(
            {
                "group_id": group_id,
                "requested_model": requested_model.strip(),
                "success": counts[0],
                "final_empty_pool_429": counts[1],
                "other_error": counts[2],
            }
        )

    ordered = [buckets[key] for key in sorted(buckets)]
    for bucket in ordered:
        bucket["facts"].sort(key=lambda row: (row["group_id"], row["requested_model"]))
    return {
        "edge": label,
        "reachable": True,
        "schema_version": SCHEMA_VERSION,
        "watermark": watermark.isoformat().replace("+00:00", "Z"),
        "telemetry_status": "stale" if evaluated_at - watermark > STALE_AFTER else "fresh",
        "buckets": ordered,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--label", required=True)
    args = parser.parse_args()
    try:
        result = parse_probe_output(sys.stdin.read(), args.label)
    except ProbeContractError as exc:
        print(f"edge-terminal-probe: {exc}", file=sys.stderr)
        return 1
    json.dump(result, sys.stdout, separators=(",", ":"), sort_keys=True)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
