#!/usr/bin/env python3
"""Sync endpoint-compat-baseline.md runtime anchor to backend/cmd/server/VERSION."""
from __future__ import annotations

import argparse
import re
import sys
from datetime import date
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
BASELINE = REPO_ROOT / "docs/ops/endpoint-compat-baseline.md"
VERSION_FILE = REPO_ROOT / "backend/cmd/server/VERSION"

RUNTIME_ANCHOR_RE = re.compile(
    r"(\| Runtime code anchor \| `)v[\d.]+(` release \(`backend/cmd/server/VERSION`\); last live deploy `)v[\d.]+(`\.)"
)
BASELINE_DATE_RE = re.compile(r"(\| Baseline date \| )[\d-]+( \|)")


def normalize_tag(tag: str) -> str:
    tag = tag.strip()
    if not tag:
        raise ValueError("empty tag")
    return tag if tag.startswith("v") else f"v{tag}"


def normalize_version(version: str) -> str:
    version = version.strip()
    if not re.fullmatch(r"\d+\.\d+\.\d+", version):
        raise ValueError(f"invalid version: {version!r}")
    return version


def read_version_file() -> str:
    if not VERSION_FILE.is_file():
        raise SystemExit(f"missing VERSION file: {VERSION_FILE}")
    return normalize_version(VERSION_FILE.read_text(encoding="utf-8"))


def sync_baseline_text(text: str, version: str, previous_deploy_tag: str) -> str:
    new_tag = normalize_tag(version if version.startswith("v") else f"v{version}")
    prev_tag = normalize_tag(previous_deploy_tag)
    if not RUNTIME_ANCHOR_RE.search(text):
        raise SystemExit("endpoint-compat baseline: Runtime code anchor row not found")
    text = RUNTIME_ANCHOR_RE.sub(rf"\1{new_tag}\2{prev_tag}\3", text, count=1)
    if not BASELINE_DATE_RE.search(text):
        raise SystemExit("endpoint-compat baseline: Baseline date row not found")
    text = BASELINE_DATE_RE.sub(rf"\g<1>{date.today().isoformat()}\2", text, count=1)
    return text


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", help="target VERSION (default: backend/cmd/server/VERSION)")
    parser.add_argument(
        "--previous-deploy-tag",
        help="last live deploy tag before this release (e.g. v1.8.90)",
    )
    parser.add_argument("--dry-run", action="store_true", help="print planned anchor only")
    parser.add_argument("--selftest", action="store_true", help="offline selftest")
    args = parser.parse_args()

    if args.selftest:
        sample = (
            "| Baseline date | 2026-01-01 |\n"
            "| Runtime code anchor | `v1.0.0` release (`backend/cmd/server/VERSION`); "
            "last live deploy `v0.9.9`. suffix |\n"
        )
        out = sync_baseline_text(sample, "1.1.0", "v1.0.0")
        assert "`v1.1.0` release" in out and "last live deploy `v1.0.0`" in out
        assert "| Baseline date | " in out
        print("sync_endpoint_compat_baseline_anchor selftest: ok")
        return 0

    if not args.previous_deploy_tag:
        parser.error("--previous-deploy-tag is required unless --selftest")

    version = normalize_version(args.version) if args.version else read_version_file()
    if not BASELINE.is_file():
        print(f"sync endpoint-compat baseline: FAIL missing {BASELINE}", file=sys.stderr)
        return 1

    original = BASELINE.read_text(encoding="utf-8")
    updated = sync_baseline_text(original, version, args.previous_deploy_tag)
    if updated == original:
        print(f"sync endpoint-compat baseline: ok (already anchored at v{version})")
        return 0

    if args.dry_run:
        print(f"sync endpoint-compat baseline: would anchor v{version} (previous {normalize_tag(args.previous_deploy_tag)})")
        return 0

    BASELINE.write_text(updated, encoding="utf-8")
    print(
        "sync endpoint-compat baseline: updated "
        f"runtime anchor v{version} (previous {normalize_tag(args.previous_deploy_tag)})"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
