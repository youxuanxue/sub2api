#!/usr/bin/env python3
"""Validate the raw-only target contract before a gateway release mutates hosts.

Maintenance is published independently. Gateway release classification never
infers its baseline or authorizes a raw storage migration.
"""

from __future__ import annotations

import argparse
from pathlib import Path

import yaml


def release_mode(rollout: Path) -> str:
    data = yaml.safe_load(rollout.read_text())
    prod = data.get("prod") if isinstance(data, dict) else None
    component = prod.get("component_release") if isinstance(prod, dict) else None
    contract = (
        component.get("runtime_contract") if isinstance(component, dict) else None
    )
    if contract != "raw_archive_v1":
        raise ValueError(
            "unsupported runtime contract; raw archive compatibility must be verified separately"
        )
    return "raw_archive"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--operation", choices=("classify",), default="classify")
    parser.add_argument("--rollout", type=Path, required=True)
    args = parser.parse_args()
    try:
        mode = release_mode(args.rollout)
    except (OSError, ValueError, yaml.YAMLError) as exc:
        parser.error(str(exc))
    print("mode=" + mode)


if __name__ == "__main__":
    main()
