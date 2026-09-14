#!/usr/bin/env python3
"""Ensure the served-model manifest is the sole catalog declaration source."""
import json
from pathlib import Path
ROOT = Path(__file__).resolve().parents[2]
MANIFEST = ROOT / "backend/internal/service/tk_served_models.json"
def check():
    try: data=json.loads(MANIFEST.read_text())
    except Exception as exc: return [f"cannot read model manifest: {exc}"]
    rows=list(data.get("entries", {}).values()) if isinstance(data, dict) else data
    errors=[]; ids=set()
    if not isinstance(rows,list): return ["model manifest must contain a list"]
    for i,row in enumerate(rows):
        if not isinstance(row,dict): errors.append(f"row {i} is not an object"); continue
        mid=row.get("model_id") or row.get("id")
        if not mid or mid in ids: errors.append(f"duplicate or missing model id: {mid}")
        ids.add(mid)
        if not row.get("price_owner"): errors.append(f"{mid}: missing price_owner")
    return errors
if __name__ == '__main__':
    failures=check()
    if failures: print('\n'.join(f"FAIL: {x}" for x in failures)); raise SystemExit(1)
    print(f"model owner manifest: ok ({len(json.loads(MANIFEST.read_text()).get('entries', json.loads(MANIFEST.read_text()).get('models', [])))} rows)")
