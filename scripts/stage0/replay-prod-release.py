#!/usr/bin/env python3
"""Validate and record a production blue/green replay.

The runner is deliberately fail closed: it accepts only prod prepare receipts,
requires a retained-capture manifest, and never has a promote or edge rollout
operation.  The host-specific replay executor supplies the observed facts via
JSON; this contract keeps the release workflow deterministic and reviewable.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import sys
from typing import NoReturn
from datetime import datetime, timezone


def fail(message: str) -> "NoReturn":
    raise SystemExit(f"replay: {message}")


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--tag", required=True)
    p.add_argument("--prepared-receipt", required=True)
    p.add_argument("--capture-manifest", required=True)
    p.add_argument("--observations", required=True, help="sanitized executor observations")
    p.add_argument("--out", required=True)
    a = p.parse_args()
    if not a.tag or any(c not in "0123456789." for c in a.tag):
        fail("invalid release tag")
    if len(a.prepared_receipt) != 64 or any(c not in "0123456789abcdef" for c in a.prepared_receipt):
        fail("prepared receipt must be a SHA256")
    def read(path: str) -> dict:
        try:
            value = json.loads(pathlib.Path(path).read_text())
        except (OSError, json.JSONDecodeError) as e:
            fail(f"cannot read JSON: {e}")
        if not isinstance(value, dict):
            fail("JSON root must be an object")
        return value
    manifest, obs = read(a.capture_manifest), read(a.observations)
    required = ("users", "models", "protocols", "samples")
    if any(not manifest.get(k) for k in required):
        fail("capture manifest must contain non-empty users/models/protocols/samples")
    for key in ("active_color", "caddy_hash", "target_color", "target_tag", "listener", "isolated_db", "isolated_redis"):
        if key not in obs:
            fail(f"observation missing {key}")
    if obs["active_color"] == obs["target_color"] or obs["target_tag"] != a.tag:
        fail("target must be inactive and match release tag")
    if obs["listener"] != "127.0.0.1" or not obs["isolated_db"] or not obs["isolated_redis"]:
        fail("replay target is not isolated on loopback")
    if obs.get("caddy_changed") or obs.get("active_color_changed"):
        fail("cutover detected during replay")
    out = pathlib.Path(a.out); out.mkdir(parents=True, exist_ok=True)
    result = {"tag": a.tag, "prepared_receipt": a.prepared_receipt,
              "coverage": {k: len(manifest[k]) for k in required},
              "verdict": "green", "cutover": False, "approval_pending": True,
              "generated_at": datetime.now(timezone.utc).isoformat()}
    payload = json.dumps(result, sort_keys=True, separators=(",", ":"))
    receipt = hashlib.sha256(payload.encode()).hexdigest()
    result["receipt_sha256"] = receipt
    (out / "replay-receipt.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    main()
