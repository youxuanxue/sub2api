#!/usr/bin/env python3
"""Generate/check the offline catalog projection through its Go owner."""
import argparse
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[2]
OUTPUT = ROOT / 'ops/stage0/generated/gateway-catalog.json'


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    proc = subprocess.run(['go', 'run', './cmd/gateway-capability-catalog'], cwd=ROOT/'backend',
                          capture_output=True, text=True, check=True, timeout=300)
    value = json.loads(proc.stdout)
    text = json.dumps(value, indent=2, sort_keys=True) + '\n'
    if args.check:
        if not OUTPUT.is_file() or OUTPUT.read_text() != text:
            raise SystemExit('capability catalog drift: run python3 scripts/stage0/update-capability-catalog.py')
    else:
        OUTPUT.parent.mkdir(parents=True, exist_ok=True)
        OUTPUT.write_text(text)
    print('capability catalog: consistent with compiled owner')


if __name__ == '__main__':
    main()
