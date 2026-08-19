#!/usr/bin/env python3
"""Resolve QA Bundle rollout from an explicit release-tree capability contract."""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

import yaml


IMAGE_REPOSITORY = "ghcr.io/youxuanxue/sub2api"
SUPPORTED_RUNTIME_CONTRACT = "phase3_v1"
TAG_PATTERN = re.compile(
    r"^(?:0|[1-9][0-9]*)\."
    r"(?:0|[1-9][0-9]*)\."
    r"(?:0|[1-9][0-9]*)"
    r"(?:-(?:beta|rc)\.(?:0|[1-9][0-9]*))?$"
)
DIGEST_PATTERN = re.compile(r"^sha256:[a-f0-9]{64}$")


def release_contract(path: Path) -> str | None:
    try:
        payload = yaml.safe_load(path.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError) as exc:
        raise ValueError(f"target rollout manifest is invalid: {exc}") from exc
    if not isinstance(payload, dict) or not isinstance(payload.get("prod"), dict):
        raise ValueError("target rollout manifest prod must be a mapping")
    user_export = payload["prod"].get("user_export")
    if user_export is None:
        return None
    if not isinstance(user_export, dict):
        raise ValueError("target rollout manifest prod.user_export must be a mapping")
    contract = user_export.get("bundle_runtime_contract")
    if contract is None:
        return None
    if contract != SUPPORTED_RUNTIME_CONTRACT:
        raise ValueError(f"unsupported target Bundle runtime contract: {contract!r}")
    return contract


def verified_worker_image(image: str) -> str | None:
    tag_prefix = f"{IMAGE_REPOSITORY}:"
    digest_prefix = f"{IMAGE_REPOSITORY}@"
    if image.startswith(tag_prefix):
        tag = image.removeprefix(tag_prefix)
        return image if TAG_PATTERN.fullmatch(tag) else None
    if image.startswith(digest_prefix):
        digest = image.removeprefix(digest_prefix)
        return image if DIGEST_PATTERN.fullmatch(digest) else None
    return None


def _optional_bool(value: object) -> bool | None:
    if value is None:
        return None
    if isinstance(value, bool):
        return value
    raise ValueError("surface change flags must be booleans")


def parse_surface_json(raw: str) -> tuple[bool | None, bool | None]:
    text = raw.strip()
    if not text:
        return None, None
    try:
        payload = json.loads(text)
    except json.JSONDecodeError as exc:
        raise ValueError(f"surface JSON is invalid: {exc}") from exc
    if not isinstance(payload, dict):
        raise ValueError("surface JSON must be an object")
    return (
        _optional_bool(payload.get("worker_surface_changed")),
        _optional_bool(payload.get("publisher_surface_changed")),
    )


def resolve(
    target_tag: str,
    target_rollout: Path,
    verified_existing_image: str,
    worker_surface_changed: bool | None = None,
    publisher_surface_changed: bool | None = None,
) -> dict[str, str | bool]:
    if TAG_PATTERN.fullmatch(target_tag) is None:
        raise ValueError("target tag must match X.Y.Z (optionally -rc.N or -beta.N)")

    contract = release_contract(target_rollout)
    if contract == SUPPORTED_RUNTIME_CONTRACT:
        existing = verified_worker_image(verified_existing_image)
        if existing is not None and worker_surface_changed is False:
            return {
                "mode": "phase3",
                "resolved_worker_image": existing,
                "worker_source": "verified_live_worker",
                "run_canary": publisher_surface_changed is not False,
                "host_runtime_mode": "target_release",
            }
        return {
            "mode": "phase3",
            "resolved_worker_image": f"{IMAGE_REPOSITORY}:{target_tag}",
            "worker_source": "target_release",
            "run_canary": True,
            "host_runtime_mode": "target_release",
        }

    existing = verified_worker_image(verified_existing_image)
    if existing is None:
        raise ValueError(
            "legacy rollback requires a fully verified immutable QA Bundle Worker "
            f"image from {IMAGE_REPOSITORY}"
        )
    return {
        "mode": "legacy_rollback",
        "resolved_worker_image": existing,
        "worker_source": "verified_live_worker",
        "run_canary": False,
        "host_runtime_mode": "current_safe_degraded",
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--target-tag", required=True)
    parser.add_argument("--target-rollout", type=Path, required=True)
    parser.add_argument("--verified-existing-image", default="")
    parser.add_argument("--surface-json", default="")
    args = parser.parse_args()
    try:
        worker_changed, publisher_changed = parse_surface_json(args.surface_json)
        result = resolve(
            args.target_tag,
            args.target_rollout,
            args.verified_existing_image,
            worker_surface_changed=worker_changed,
            publisher_surface_changed=publisher_changed,
        )
    except ValueError as exc:
        print(f"resolve QA Bundle Worker image: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(result, separators=(",", ":"), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
