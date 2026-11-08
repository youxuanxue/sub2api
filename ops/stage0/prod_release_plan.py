#!/usr/bin/env python3
"""Validate the target runtime contract before a gateway release mutates hosts.

Maintenance is published independently. Gateway release classification never
infers its baseline or authorizes a raw storage migration. QA S3 was retired on
2026-11-08: ``gateway_only_v1`` plans a plain gateway release with no QA
component step; ``raw_archive_v1`` stays accepted for rollback targets.
"""

from __future__ import annotations

import argparse
from pathlib import Path

import yaml


RELEASE_MODES = {
    "gateway_only_v1": "gateway",
    # Rollback targets released before the QA S3 retirement.
    "raw_archive_v1": "raw_archive",
}


def release_mode(rollout: Path) -> str:
    data = yaml.safe_load(rollout.read_text())
    prod = data.get("prod") if isinstance(data, dict) else None
    component = prod.get("component_release") if isinstance(prod, dict) else None
    contract = (
        component.get("runtime_contract") if isinstance(component, dict) else None
    )
    mode = RELEASE_MODES.get(contract) if isinstance(contract, str) else None
    if mode is None:
        raise ValueError(
            "unsupported runtime contract; raw archive compatibility must be verified separately"
        )
    return mode


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
