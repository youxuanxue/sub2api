#!/usr/bin/env python3
"""Validate NewAPI catalog declarations through the canonical manifest parser.

Price-owner resolution stays in catalog-serving-drift.py. Native catalog sets
and recommendation withdrawals retain their own owners.
"""
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'ops/pricing'))
from served_models_manifest import ManifestError, load_manifest


def main() -> int:
    try:
        manifest = load_manifest()
    except ManifestError as exc:
        for failure in exc.errors:
            print(f'FAIL: {failure}')
        return 1
    print(f'model owner manifest: ok ({len(manifest.entries)} NewAPI declarations)')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
