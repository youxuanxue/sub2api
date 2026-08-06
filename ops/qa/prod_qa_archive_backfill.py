#!/usr/bin/env python3
"""Loop prod QA archive backfill (one oldest hour per invocation)."""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--max-hours", type=int, default=1)
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    if args.max_hours < 1:
        raise SystemExit("--max-hours must be >= 1")

    receipts: list[dict] = []
    script = ROOT / "ops" / "qa" / "prod_qa_maintenance.py"
    for _ in range(args.max_hours):
        proc = subprocess.run(
            [sys.executable, str(script), "--backfill-once", "--json"],
            capture_output=True,
            text=True,
            check=False,
        )
        if proc.returncode != 0:
            err = (proc.stderr or proc.stdout or "").strip()
            if "no qa archive backfill window remaining" in err.lower():
                break
            print(err, file=sys.stderr)
            return proc.returncode
        receipts.append(json.loads(proc.stdout))
    payload = {"attempted": args.max_hours, "completed": len(receipts), "receipts": receipts}
    print(json.dumps(payload, ensure_ascii=True, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
