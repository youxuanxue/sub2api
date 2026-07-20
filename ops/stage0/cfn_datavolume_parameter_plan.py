#!/usr/bin/env python3
"""Build the narrow CloudFormation parameter plan for DataVolume growth.

Pure/offline by design: this module validates the live stack description saved
by the shell orchestrator and writes a parameter file. It never calls AWS.
"""

from __future__ import annotations

import argparse
import json
import pathlib
import sys
from typing import Any

MIN_VOLUME_GIB = 20
MAX_VOLUME_GIB = 500


def build_parameter_plan(
    stack_doc: dict[str, Any], desired_size_gib: int, stable_ami_param: str
) -> tuple[list[dict[str, Any]], int]:
    if not isinstance(desired_size_gib, int):
        raise ValueError("desired size must be an integer")
    if not MIN_VOLUME_GIB <= desired_size_gib <= MAX_VOLUME_GIB:
        raise ValueError(
            f"desired size must be between {MIN_VOLUME_GIB} and {MAX_VOLUME_GIB} GiB"
        )
    if not stable_ami_param.startswith("/"):
        raise ValueError("stable AMI parameter must be an absolute SSM parameter name")

    stacks = stack_doc.get("Stacks") or []
    if len(stacks) != 1 or not isinstance(stacks[0], dict):
        raise ValueError("stack description must contain exactly one stack")
    source_params = stacks[0].get("Parameters") or []

    current_size: int | None = None
    seen: set[str] = set()
    for param in source_params:
        key = param.get("ParameterKey")
        if not isinstance(key, str) or not key:
            raise ValueError("stack contains a parameter without ParameterKey")
        if key in seen:
            raise ValueError(f"duplicate stack parameter {key}")
        seen.add(key)
        if key == "DataVolumeSizeGiB":
            try:
                current_size = int(param.get("ParameterValue"))
            except (TypeError, ValueError) as exc:
                raise ValueError("current DataVolumeSizeGiB is not an integer") from exc

    if current_size is None:
        raise ValueError("stack is missing DataVolumeSizeGiB")
    if desired_size_gib < current_size:
        raise ValueError(
            f"refusing DataVolume shrink: current={current_size} desired={desired_size_gib}"
        )

    desired = {
        "DataVolumeSizeGiB": str(desired_size_gib),
        "AmazonLinux2023Arm64Ami": stable_ami_param,
    }
    params: list[dict[str, Any]] = []
    for param in source_params:
        key = param["ParameterKey"]
        if key in desired:
            params.append({"ParameterKey": key, "ParameterValue": desired[key]})
        else:
            params.append({"ParameterKey": key, "UsePreviousValue": True})
    return params, current_size


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--stack-json", type=pathlib.Path, required=True)
    parser.add_argument("--out", type=pathlib.Path, required=True)
    parser.add_argument("--size", type=int, required=True)
    parser.add_argument("--stable-ami-param", required=True)
    args = parser.parse_args()

    try:
        stack_doc = json.loads(args.stack_json.read_text(encoding="utf-8"))
        params, current_size = build_parameter_plan(
            stack_doc, args.size, args.stable_ami_param
        )
        args.out.write_text(json.dumps(params, indent=2) + "\n", encoding="utf-8")
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"parameter plan rejected: {exc}", file=sys.stderr)
        return 2

    print(
        json.dumps(
            {
                "current_size_gib": current_size,
                "desired_size_gib": args.size,
                "mode": "offline_parameter_plan",
            },
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
