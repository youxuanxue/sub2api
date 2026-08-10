#!/usr/bin/env python3
"""Verify endpoint-compat-baseline.md tracks the deployed server VERSION."""
from __future__ import annotations

import argparse
import importlib.util
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
BASELINE = REPO_ROOT / "docs/ops/endpoint-compat-baseline.md"
VERSION_FILE = REPO_ROOT / "backend/cmd/server/VERSION"
_SYNC_ANCHOR = Path(__file__).resolve().parent / "sync_endpoint_compat_baseline_anchor.py"


def _load_parse_runtime_anchor():
    spec = importlib.util.spec_from_file_location(
        "sync_endpoint_compat_baseline_anchor", _SYNC_ANCHOR
    )
    if spec is None or spec.loader is None:
        raise RuntimeError(f"unable to load {_SYNC_ANCHOR}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.parse_runtime_anchor


parse_runtime_anchor = _load_parse_runtime_anchor()


def read_version() -> str:
    if not VERSION_FILE.is_file():
        raise SystemExit(f"missing VERSION file: {VERSION_FILE}")
    version = VERSION_FILE.read_text(encoding="utf-8").strip()
    if not re.fullmatch(r"\d+\.\d+\.\d+", version):
        raise SystemExit(f"invalid VERSION file contents: {version!r}")
    return version


def validate_baseline_freshness(baseline_text: str, version: str) -> str | None:
    """Return an error message when the baseline is stale or not syncable."""
    expected = f"v{version}"
    try:
        release_tag, _last_deploy = parse_runtime_anchor(baseline_text)
    except ValueError as exc:
        return str(exc)
    if release_tag != expected:
        return (
            "docs/ops/endpoint-compat-baseline.md runtime anchor release tag "
            f"{release_tag} != backend/cmd/server/VERSION {expected}"
        )
    return None


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--selftest", action="store_true", help="offline selftest")
    args = parser.parse_args()

    if args.selftest:
        valid = (
            "| Runtime code anchor | `v1.8.142` release (`backend/cmd/server/VERSION`); "
            "last live deploy `v1.8.141`. note |\n"
        )
        broken = (
            "| Runtime code anchor | `v1.8.142` release (`backend/cmd/server/VERSION`); "
            "last live deploy pending `v1.8.142` (prod still on `v1.8.141`). |\n"
        )
        assert validate_baseline_freshness(valid, "1.8.142") is None
        assert validate_baseline_freshness(valid, "1.8.141") is not None
        assert validate_baseline_freshness(broken, "1.8.142") is not None
        print("check_endpoint_compat_baseline_freshness selftest: ok")
        return 0

    version = read_version()
    if not BASELINE.is_file():
        print(f"endpoint-compat baseline freshness: FAIL missing {BASELINE}", file=sys.stderr)
        return 1

    err = validate_baseline_freshness(BASELINE.read_text(encoding="utf-8"), version)
    if err:
        print(f"endpoint-compat baseline freshness: FAIL {err}", file=sys.stderr)
        return 1

    print(f"endpoint-compat baseline freshness: ok (runtime anchor v{version})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
