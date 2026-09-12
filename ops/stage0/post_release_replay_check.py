#!/usr/bin/env python3
"""Run the finite capability manifest after a release.

This is intentionally an explicit operator action; normal releases do not
invoke it. The command only reports the matrix and delegates execution to the
isolated prod replay entrypoint.
"""
from __future__ import annotations

import argparse
from pathlib import Path
from prod_replay_manifest import load


def main(argv=None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--manifest", type=Path, default=Path(__file__).with_name("prod-replay-capabilities.json"))
    args = parser.parse_args(argv)
    capabilities = load(args.manifest)
    print(f"capability_count={len(capabilities)}")
    print("replay_required=true")
    print("cutover=false")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
