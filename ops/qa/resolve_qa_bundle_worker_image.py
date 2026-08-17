#!/usr/bin/env python3
"""Resolve the QA Bundle Worker image independently from the app rollback tag."""
from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass


IMAGE_REPOSITORY = "ghcr.io/youxuanxue/sub2api"
PHASE3_MINIMUM_TAG = "1.8.156"
TAG_PATTERN = re.compile(
    r"^(?P<major>0|[1-9][0-9]*)\."
    r"(?P<minor>0|[1-9][0-9]*)\."
    r"(?P<patch>0|[1-9][0-9]*)"
    r"(?:-(?P<stage>beta|rc)\.(?P<number>0|[1-9][0-9]*))?$"
)


@dataclass(frozen=True, order=True)
class Version:
    major: int
    minor: int
    patch: int
    stage_rank: int
    stage_number: int


def parse_tag(tag: str) -> Version | None:
    match = TAG_PATTERN.fullmatch(tag)
    if match is None:
        return None
    stage = match.group("stage")
    stage_rank = {"beta": 0, "rc": 1, None: 2}[stage]
    return Version(
        int(match.group("major")),
        int(match.group("minor")),
        int(match.group("patch")),
        stage_rank,
        int(match.group("number") or 0),
    )


def existing_release(image: str) -> tuple[Version, str] | None:
    prefix = f"{IMAGE_REPOSITORY}:"
    if not image.startswith(prefix):
        return None
    tag = image.removeprefix(prefix)
    version = parse_tag(tag)
    if version is None:
        return None
    return version, image


def resolve(target_tag: str, existing_image: str) -> dict[str, str]:
    target = parse_tag(target_tag)
    if target is None:
        raise ValueError(
            "target tag must match X.Y.Z (optionally -rc.N or -beta.N)"
        )

    minimum = parse_tag(PHASE3_MINIMUM_TAG)
    assert minimum is not None
    current = existing_release(existing_image)

    if target >= minimum:
        target_image = f"{IMAGE_REPOSITORY}:{target_tag}"
        if current is not None and current[0] >= minimum and current[0] > target:
            target_image = current[1]
        return {"mode": "phase3", "resolved_worker_image": target_image}

    if current is None or current[0] < minimum:
        raise ValueError(
            "legacy rollback requires an existing compatible QA Bundle Worker "
            f"image from {IMAGE_REPOSITORY} at or above {PHASE3_MINIMUM_TAG}"
        )
    return {"mode": "legacy_rollback", "resolved_worker_image": current[1]}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--target-tag", required=True)
    parser.add_argument("--existing-image", default="")
    args = parser.parse_args()
    try:
        result = resolve(args.target_tag, args.existing_image)
    except ValueError as exc:
        print(f"resolve QA Bundle Worker image: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(result, separators=(",", ":"), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
