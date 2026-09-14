#!/usr/bin/env python3
import json
from pathlib import Path
ROOT = Path(__file__).resolve().parents[2]
LEDGER = ROOT / "ops/observability/candidate-eligibility-acceptance-ledger.json"
STORY = ROOT / ".testing/user-stories/stories/US-050-candidate-eligibility-ssot.md"
def check():
    try: data = json.loads(LEDGER.read_text())
    except Exception as exc: return [f"cannot read acceptance ledger: {exc}"]
    allowed = {"contract-tested", "live-verified", "blocked"}; errors = []
    if data.get("contract") != "US-050": errors.append("ledger contract must be US-050")
    if set(data.get("statuses", [])) != allowed: errors.append("invalid status vocabulary")
    if not data.get("criteria"): errors.append("criteria must not be empty")
    for key, value in data.get("criteria", {}).items():
        if not key.startswith("AC-") or value not in allowed: errors.append(f"invalid criterion status: {key}={value}")
    if not STORY.exists(): errors.append("US-050 story is missing")
    if data.get("live_observation", {}).get("status") not in allowed: errors.append("invalid live_observation status")
    return errors
if __name__ == "__main__":
    failures = check()
    if failures:
        print("\n".join(f"FAIL: {x}" for x in failures)); raise SystemExit(1)
    print("candidate acceptance ledger: ok")
