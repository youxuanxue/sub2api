#!/usr/bin/env python3
"""Regenerate/check the universal account-supply plan without accessing production."""
import argparse
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'ops/stage0'))
from gateway_capability_matrix import DEFAULT_INVENTORY, build, load  # noqa: E402


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--inventory', type=Path, default=DEFAULT_INVENTORY)
    parser.add_argument('--out', type=Path, help='write release artifact outside the tracked source inventory')
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    value = build(json.loads(args.inventory.read_text()), load())
    text = json.dumps(value, indent=2, sort_keys=True) + '\n'
    if not args.check:
        if args.out is None:
            parser.error('--out is required unless --check is used')
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(text)
    print(f"account-supply plan: {len(value['entries'])} universal obligations; no upstream requests")


if __name__ == '__main__':
    main()
