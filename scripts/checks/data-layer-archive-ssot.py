#!/usr/bin/env python3
"""Keep generic data-layer archive constants aligned with pipeline_status.yaml."""
from __future__ import annotations

import argparse
import importlib.util
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PIPELINE = ROOT / "ops" / "archive" / "pipeline_status.yaml"
REHEARSAL = ROOT / "ops" / "archive" / "data_layer_archive_rehearsal.py"


def _load_yaml(path: Path) -> dict:
    try:
        import yaml
    except ImportError as exc:
        raise RuntimeError("PyYAML required") from exc
    payload = yaml.safe_load(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"{path} must be a mapping")
    return payload


def _load_rehearsal_constants(rehearsal_path: Path) -> tuple[dict[str, int], str]:
    spec = importlib.util.spec_from_file_location("data_layer_archive_rehearsal", rehearsal_path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot import {rehearsal_path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    retention = getattr(module, "DEFAULT_RETENTION_DAYS", None)
    legacy_upper = getattr(module, "PROD_LEGACY_UPPER_EXCLUSIVE", None)
    if not isinstance(retention, dict):
        raise ValueError("DEFAULT_RETENTION_DAYS missing from rehearsal module")
    if not isinstance(legacy_upper, str):
        raise ValueError("PROD_LEGACY_UPPER_EXCLUSIVE missing from rehearsal module")
    return retention, legacy_upper


def scan(root: Path) -> list[str]:
    failures: list[str] = []
    pipeline_path = root / PIPELINE.relative_to(ROOT)
    rehearsal_path = root / REHEARSAL.relative_to(ROOT)
    if not pipeline_path.is_file():
        return [f"missing pipeline SSOT: {PIPELINE.relative_to(ROOT)}"]
    if not rehearsal_path.is_file():
        return [f"missing rehearsal module: {REHEARSAL.relative_to(ROOT)}"]
    try:
        pipeline = _load_yaml(pipeline_path)
        retention, legacy_upper = _load_rehearsal_constants(rehearsal_path)
    except (OSError, ValueError, RuntimeError) as exc:
        return [f"data-layer archive SSOT load failed: {exc}"]

    hot = pipeline.get("retention_hot_layer_days")
    if not isinstance(hot, dict):
        failures.append("pipeline_status.yaml retention_hot_layer_days must be a mapping")
        return failures

    for dataset in ("usage", "ops"):
        expected = hot.get(dataset)
        actual = retention.get(dataset)
        if expected != actual:
            failures.append(
                f"retention drift: pipeline {dataset}={expected!r} rehearsal {dataset}={actual!r}"
            )

    pipeline_legacy = pipeline.get("legacy_export_upper_exclusive")
    if pipeline_legacy != legacy_upper:
        failures.append(
            "legacy upper drift: pipeline "
            f"{pipeline_legacy!r} rehearsal {legacy_upper!r}"
        )
    return failures


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true")
    args = parser.parse_args()
    failures = scan(ROOT)
    if failures:
        for failure in failures:
            print(f"FAIL: {failure}")
        return 1
    if not args.quiet:
        print(f"data-layer archive SSOT: OK ({PIPELINE.relative_to(ROOT)})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
