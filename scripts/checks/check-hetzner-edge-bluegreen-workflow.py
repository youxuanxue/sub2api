#!/usr/bin/env python3
"""Contract: Hetzner edge workflow wires upgrade/rollback to shared blue/green."""
from __future__ import annotations

import pathlib
import sys

import yaml

ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / ".github/workflows/deploy-edge-hetzner-stage0.yml"
DISPATCH = ROOT / "scripts/stage0/dispatch-edge-deploy.sh"


def fail(message: str) -> int:
    print(f"FAIL: hetzner edge bluegreen workflow: {message}", file=sys.stderr)
    return 1


def main() -> int:
    data = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
    # PyYAML turns bare key `on:` into boolean True.
    on = data.get("on") or data.get(True) or {}
    inputs = (on.get("workflow_dispatch") or {}).get("inputs") or {}
    ops = (inputs.get("operation") or {}).get("options") or []
    for required in ("upgrade", "rollback", "smoke", "provision", "validate"):
        if required not in ops:
            return fail(f"operation options missing {required!r}: {ops}")

    jobs = data.get("jobs") or {}
    upgrade = jobs.get("dispatch-upgrade") or {}
    if not upgrade:
        return fail("missing dispatch-upgrade job")

    dump = yaml.safe_dump(upgrade)
    runs = "\n".join(str(step.get("run") or "") for step in (upgrade.get("steps") or []))
    if "deploy_via_ssm_bluegreen.sh" not in runs:
        return fail("dispatch-upgrade does not call deploy_via_ssm_bluegreen.sh")
    if "bluegreen-migration-safety.py" not in runs:
        return fail("dispatch-upgrade missing bluegreen-migration-safety.py --release-tag")
    if "verify_ghcr_manifest.sh" not in runs:
        return fail("dispatch-upgrade missing verify_ghcr_manifest.sh")
    if "backup-env-secrets-via-ssm.sh" not in runs:
        return fail("dispatch-upgrade missing backup-env-secrets-via-ssm.sh")
    if "STAGE0_DEPLOY_PROFILE" not in dump or "edge" not in dump:
        return fail("dispatch-upgrade missing STAGE0_DEPLOY_PROFILE=edge")
    if "edge_ssm_execution.py" not in runs:
        return fail("dispatch-upgrade must resolve Hybrid identity via edge_ssm_execution.py")
    if "--platform hetzner" not in runs and "--platform auto" not in runs:
        return fail("edge resolve must pin hetzner or auto")

    dispatch_text = DISPATCH.read_text(encoding="utf-8")
    if "hetzner ${OPERATION} not wired yet" in dispatch_text:
        return fail("dispatch-edge-deploy.sh still blocks hetzner upgrade/rollback/smoke")

    print("check-hetzner-edge-bluegreen-workflow: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
