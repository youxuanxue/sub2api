#!/usr/bin/env python3
"""Validate the protected pricing-registry publication boundary."""

from __future__ import annotations

import argparse
import pathlib
import sys

try:
    import yaml
except ImportError as exc:  # pragma: no cover - preflight environment failure
    raise SystemExit(f"PyYAML is required: {exc}")


REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
REGISTRY = "backend/internal/service/tk_pricing_overlay.json"


def _load_workflow(path: pathlib.Path) -> dict:
    try:
        doc = yaml.safe_load(path.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError) as exc:
        raise ValueError(f"{path}: cannot parse workflow: {exc}") from exc
    if not isinstance(doc, dict):
        raise ValueError(f"{path}: workflow root must be an object")
    return doc


def _workflow_events(doc: dict) -> object:
    # PyYAML 1.1 parses the unquoted GitHub Actions key `on` as boolean true.
    return doc.get("on", doc.get(True))


def validate_publication_boundary(root: pathlib.Path) -> list[str]:
    errors: list[str] = []
    publisher_path = root / ".github/workflows/pricing-registry-publish.yml"
    deploy_path = root / ".github/workflows/deploy-stage0.yml"
    sensor_workflow_path = root / ".github/workflows/pricing-registry-sensor.yml"
    sensor_script_path = root / "ops/pricing/pricing-registry-sensor.py"

    try:
        publisher = _load_workflow(publisher_path)
    except ValueError as exc:
        return [str(exc)]

    events = _workflow_events(publisher)
    if not isinstance(events, dict) or set(events) != {"push"}:
        errors.append("publisher must accept only the push event")
    else:
        push = events.get("push")
        if not isinstance(push, dict):
            errors.append("publisher push event must be an object")
        else:
            if push.get("branches") != ["main"]:
                errors.append("publisher push branches must be exactly [main]")
            if push.get("paths") != [REGISTRY]:
                errors.append(f"publisher push paths must be exactly [{REGISTRY}]")

    concurrency = publisher.get("concurrency")
    if not isinstance(concurrency, dict) or concurrency.get("cancel-in-progress") is not False:
        errors.append("publisher must serialize runs with cancel-in-progress=false")

    jobs = publisher.get("jobs")
    publish_job = jobs.get("publish") if isinstance(jobs, dict) else None
    if not isinstance(publish_job, dict) or publish_job.get("environment") != "prod":
        errors.append("publisher job must use the protected prod environment")

    try:
        publisher_text = publisher_path.read_text(encoding="utf-8")
    except OSError as exc:
        errors.append(f"{publisher_path}: cannot read workflow: {exc}")
    else:
        if "manage-overlay-runtime.py sync-runtime" not in publisher_text:
            errors.append("publisher must invoke the protected sync-runtime command")
        if "git checkout --detach origin/main" not in publisher_text:
            errors.append("publisher must detach onto freshly fetched origin/main")

    forbidden_deploy = (
        "manage-overlay-runtime.py sync-runtime",
        "tk_pricing_overlay_runtime",
        "SettingKeyTKPricingOverlayRuntime",
    )
    try:
        deploy_text = deploy_path.read_text(encoding="utf-8")
    except OSError as exc:
        errors.append(f"{deploy_path}: cannot read workflow: {exc}")
    else:
        for marker in forbidden_deploy:
            if marker in deploy_text:
                errors.append(f"deploy workflow may not write pricing runtime: found {marker!r}")

    forbidden_sensor = (
        "manage-overlay-runtime",
        "sync-runtime",
        "tk_pricing_overlay_runtime",
        "settings_updated",
        "aws-actions/",
        "boto3",
        "_SSM",
    )
    for path in (sensor_workflow_path, sensor_script_path):
        try:
            text = path.read_text(encoding="utf-8")
        except OSError as exc:
            errors.append(f"{path}: cannot read sensor surface: {exc}")
            continue
        for marker in forbidden_sensor:
            if marker in text:
                errors.append(f"sensor may not publish pricing or access AWS: {path} contains {marker!r}")

    try:
        sensor_workflow = _load_workflow(sensor_workflow_path)
    except ValueError as exc:
        errors.append(str(exc))
    else:
        permissions = sensor_workflow.get("permissions")
        if not isinstance(permissions, dict) or set(permissions) != {"contents", "pull-requests"}:
            errors.append("sensor permissions must be limited to contents and pull-requests")
        elif permissions != {"contents": "write", "pull-requests": "write"}:
            errors.append("sensor permissions must only support a draft registry PR")

    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true")
    parser.add_argument("--root", type=pathlib.Path, default=REPO_ROOT)
    args = parser.parse_args()
    errors = validate_publication_boundary(args.root.resolve())
    if errors:
        print(f"  FAIL: pricing registry publication boundary invalid ({len(errors)} issue(s)):")
        for error in errors:
            print(f"    - {error}")
        return 1
    if not args.quiet:
        print("  ok: pricing registry publisher, deploy, and sensor boundaries valid")
    return 0


if __name__ == "__main__":
    sys.exit(main())
