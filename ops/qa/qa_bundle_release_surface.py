#!/usr/bin/env python3
"""Classify whether a release changes the QA Bundle Worker or publisher surface.

App image and Bundle Worker are separate release lifecycles. Gateway-only
deploys must be able to reuse a verified live Worker and skip the 24-hour
raw-S3 canary. This module is the path-list SSOT for that decision.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

IMAGE_REPOSITORY = "ghcr.io/youxuanxue/sub2api"
TAG_PATTERN = re.compile(
    r"^(?:0|[1-9][0-9]*)\."
    r"(?:0|[1-9][0-9]*)\."
    r"(?:0|[1-9][0-9]*)"
    r"(?:-(?:beta|rc)\.(?:0|[1-9][0-9]*))?$"
)

# Rolling Fargate is required only when the Worker execution path or its
# CloudFormation contract changed.
WORKER_SURFACE_PATHS = (
    "backend/internal/observability/qa/bundle/",
    "backend/internal/observability/qa/archive/",
    "backend/cmd/server/qa_bundle_worker.go",
    "deploy/aws/cloudformation/stage0-qa-raw-archive.yaml",
    "ops/qa/deploy_qa_raw_archive_cfn.sh",
)

# Canary must still run when the app-side publisher/canary runner changed,
# even if the live Worker binary can be reused.
PUBLISHER_SURFACE_PATHS = (
    "backend/internal/observability/qa/service_bundle.go",
    "backend/internal/observability/qa/service_bundle_canary.go",
    "backend/cmd/server/qa_bundle_canary.go",
    "ops/stage0/run-qa-bundle-canary-via-ssm.sh",
    "deploy/aws/stage0/tokenkey-qa-maintenance.sh",
)


def release_tag_from_image(image: str) -> str | None:
    prefix = f"{IMAGE_REPOSITORY}:"
    if not image.startswith(prefix):
        return None
    tag = image.removeprefix(prefix)
    return tag if TAG_PATTERN.fullmatch(tag) else None


def git_paths_changed(
    repo: Path,
    from_ref: str,
    to_ref: str,
    paths: tuple[str, ...],
) -> bool:
    if from_ref == to_ref:
        return False
    command = [
        "git",
        "-C",
        str(repo),
        "diff",
        "--name-only",
        "--no-ext-diff",
        from_ref,
        to_ref,
        "--",
        *paths,
    ]
    completed = subprocess.run(command, capture_output=True, text=True, check=False)
    if completed.returncode != 0:
        detail = completed.stderr.strip() or completed.stdout.strip() or "git diff failed"
        raise ValueError(detail)
    return any(line.strip() for line in completed.stdout.splitlines())


def classify(
    *,
    from_ref: str,
    to_ref: str,
    repo: Path,
) -> dict[str, str | bool]:
    worker_changed = git_paths_changed(repo, from_ref, to_ref, WORKER_SURFACE_PATHS)
    publisher_changed = git_paths_changed(repo, from_ref, to_ref, PUBLISHER_SURFACE_PATHS)
    return {
        "from_ref": from_ref,
        "to_ref": to_ref,
        "worker_surface_changed": worker_changed,
        "publisher_surface_changed": publisher_changed,
    }


def classify_from_image(
    *,
    image: str,
    to_tag: str,
    repo: Path,
) -> dict[str, str | bool]:
    if TAG_PATTERN.fullmatch(to_tag) is None:
        raise ValueError("target tag must match X.Y.Z (optionally -rc.N or -beta.N)")
    from_tag = release_tag_from_image(image)
    if from_tag is None:
        raise ValueError("live worker image is not a reusable immutable release tag")
    return classify(from_ref=f"v{from_tag}", to_ref=f"v{to_tag}", repo=repo)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--from-image", default="")
    parser.add_argument("--to-tag", default="")
    parser.add_argument("--git-dir", type=Path, default=Path("."))
    parser.add_argument("--json", action="store_true")
    parser.add_argument("--print-tag", action="store_true")
    args = parser.parse_args()
    try:
        if args.print_tag:
            tag = release_tag_from_image(args.from_image)
            if tag is None:
                raise ValueError("live worker image is not a reusable immutable release tag")
            print(tag)
            return 0
        if not args.from_image or not args.to_tag:
            raise ValueError("--from-image and --to-tag are required")
        result = classify_from_image(
            image=args.from_image,
            to_tag=args.to_tag,
            repo=args.git_dir,
        )
    except ValueError as exc:
        print(f"classify QA Bundle release surface: {exc}", file=sys.stderr)
        return 1
    if args.json:
        print(json.dumps(result, separators=(",", ":"), sort_keys=True))
        return 0
    print(
        "worker_surface_changed={worker_surface_changed} "
        "publisher_surface_changed={publisher_surface_changed} "
        "from_ref={from_ref} to_ref={to_ref}".format(**result)
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
