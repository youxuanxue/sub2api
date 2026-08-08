#!/usr/bin/env python3
"""Keep generic data-layer archive constants aligned with pipeline_status.yaml."""
from __future__ import annotations

import argparse
import importlib.util
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PIPELINE = ROOT / "ops" / "archive" / "pipeline_status.yaml"
REHEARSAL = ROOT / "ops" / "archive" / "data_layer_archive_rehearsal.py"
LOADER = ROOT / "ops" / "archive" / "pipeline_status_loader.py"
HEALTH = ROOT / "ops" / "observability" / "data_layer_archive_health.py"

_HARDCODED_EVIDENCE = re.compile(
    r"US-039-prod-cleanup-hold-|US-040-|ops-error-logs|ops-system-logs"
)


def _load_yaml(path: Path) -> dict:
    try:
        import yaml
    except ImportError as exc:
        raise RuntimeError("PyYAML required") from exc
    payload = yaml.safe_load(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"{path} must be a mapping")
    return payload


def _load_module(path: Path, module_name: str):
    spec = importlib.util.spec_from_file_location(module_name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot import {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    spec.loader.exec_module(module)
    return module


def _load_rehearsal_constants(rehearsal_path: Path) -> tuple[dict[str, int], str]:
    module = _load_module(rehearsal_path, "data_layer_archive_rehearsal")
    retention = getattr(module, "DEFAULT_RETENTION_DAYS", None)
    legacy_upper = getattr(module, "PROD_LEGACY_UPPER_EXCLUSIVE", None)
    if not isinstance(retention, dict):
        raise ValueError("DEFAULT_RETENTION_DAYS missing from rehearsal module")
    if not isinstance(legacy_upper, str):
        raise ValueError("PROD_LEGACY_UPPER_EXCLUSIVE missing from rehearsal module")
    return retention, legacy_upper


def _evidence_failures(root: Path, pipeline: dict) -> list[str]:
    failures: list[str] = []
    loader_path = root / LOADER.relative_to(ROOT)
    health_path = root / HEALTH.relative_to(ROOT)
    if not loader_path.is_file():
        return [f"missing pipeline loader: {LOADER.relative_to(ROOT)}"]
    if not health_path.is_file():
        return [f"missing archive health script: {HEALTH.relative_to(ROOT)}"]
    health_body = health_path.read_text(encoding="utf-8")
    if "pipeline_status_loader" not in health_body:
        failures.append("data_layer_archive_health.py must import pipeline_status_loader")
    for match in _HARDCODED_EVIDENCE.finditer(health_body):
        failures.append(
            "data_layer_archive_health.py hardcodes evidence path fragment: "
            f"{match.group(0)!r}"
        )
    evidence = pipeline.get("evidence_attachments")
    if not isinstance(evidence, dict):
        failures.append("pipeline_status.yaml evidence_attachments must be a mapping")
        return failures
    try:
        loader = _load_module(loader_path, "pipeline_status_loader")
        layout = loader.load_evidence_layout(root / PIPELINE.relative_to(ROOT))
    except (OSError, ValueError, RuntimeError) as exc:
        return failures + [f"pipeline_status_loader failed: {exc}"]
    checks = (
        ("cleanup_hold_glob", layout.cleanup_hold_glob),
        ("cleanup_release_receipt_glob", layout.cleanup_release_receipt_glob),
        ("export_ledger_template", layout.export_ledger_template),
        ("promote_ledger_template", layout.promote_ledger_template),
        ("tail_export_ledger_template", layout.tail_export_ledger_template),
        ("tail_promote_ledger_template", layout.tail_promote_ledger_template),
        ("closeout_receipt_template", layout.closeout_receipt_template),
    )
    for key, actual in checks:
        expected = evidence.get(key)
        if expected != actual:
            failures.append(f"evidence drift: pipeline {key}={expected!r} loader {actual!r}")
    pipeline_slugs = evidence.get("table_slugs")
    if pipeline_slugs != layout.table_slugs:
        failures.append("evidence drift: pipeline table_slugs differ from loader")
    return failures


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
    failures.extend(_evidence_failures(root, pipeline))
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
