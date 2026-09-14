#!/usr/bin/env python3
"""Validate the single structured ledger for long-lived advisory findings."""
import json
from datetime import date
from pathlib import Path

LEDGER = Path(__file__).resolve().parents[2] / "ops/observability/advisory-ledger.json"

def check():
    try: data = json.loads(LEDGER.read_text())
    except Exception as exc: return [f"cannot read advisory ledger: {exc}"]
    allowed = {"active", "accepted", "resolved"}; errors = []; ids = set()
    if set(data.get("statuses", [])) != allowed: errors.append("invalid advisory status vocabulary")
    for item in data.get("items", []):
        ident = item.get("id")
        if not ident or ident in ids: errors.append(f"duplicate or missing advisory id: {ident}")
        ids.add(ident)
        if item.get("status") not in allowed: errors.append(f"invalid status: {ident}")
        if not item.get("owner") or not item.get("remediation"): errors.append(f"missing owner/remediation: {ident}")
        try: date.fromisoformat(item.get("expires_at", ""))
        except ValueError: errors.append(f"invalid expires_at: {ident}")
    return errors

if __name__ == "__main__":
    failures = check()
    if failures:
        print("\n".join(f"FAIL: {x}" for x in failures)); raise SystemExit(1)
    print("advisory ledger: ok")
