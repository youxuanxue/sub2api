#!/usr/bin/env python3
"""Select prod components from their independently observed release baselines."""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
from pathlib import Path

import sys
import yaml

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "qa"))
from qa_bundle_release_surface import PUBLISHER_SURFACE_PATHS, WORKER_SURFACE_PATHS
from resolve_qa_bundle_worker_image import release_contract, verified_worker_image

SCHEMA = "prod-component-release-v1"
REPOSITORY = "ghcr.io/youxuanxue/sub2api"
TAG = re.compile(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-(?:rc|beta)\.(?:0|[1-9][0-9]*))?")
SHARED = (
    "Dockerfile", ".new-api-ref", "backend/go.mod", "backend/go.sum",
    "backend/docker-entrypoint.sh", "backend/cmd/server/main.go",
    "backend/internal/config/", "backend/internal/pkg/", "backend/ent/",
    "backend/migrations/", "ops/qa/policy.yaml", "ops/qa/deploy_rollout.yaml",
    "backend/internal/observability/qa/bundle/",
    "backend/internal/observability/qa/lifecycle/",
    "backend/internal/observability/qa/archive/",
    "ops/stage0/prod_release_plan.py", "ops/qa/qa_bundle_release_surface.py",
)
MAINTENANCE = (
    "backend/cmd/server/qa_maintenance", "backend/cmd/server/qa_single_owner",
    "backend/cmd/server/qa_bundle_canary.go", "backend/cmd/qa-archive/",
    "backend/internal/observability/qa/", "backend/internal/repository/ops_repo",
    "backend/internal/service/ops_",
    "deploy/aws/stage0/tokenkey-qa-", "deploy/aws/stage0/qa-runtime",
    "ops/stage0/sync-qa-", "ops/stage0/qa-runtime", "ops/stage0/prod_release_state.py",
    "ops/stage0/run-qa-maintenance", "ops/stage0/run-qa-bundle-canary",
)
QA_ONLY = (
    "backend/cmd/server/qa_maintenance", "backend/cmd/server/qa_single_owner",
    "backend/cmd/server/qa_bundle_worker.go", "backend/cmd/server/qa_bundle_canary.go",
    "backend/cmd/qa-archive/", "backend/internal/observability/qa/archive/",
    "backend/internal/observability/qa/bundle/", "backend/internal/observability/qa/lifecycle/",
    "backend/internal/observability/qa/service_bundle_canary.go",
    "deploy/aws/stage0/tokenkey-qa-", "deploy/aws/stage0/qa-runtime",
    "deploy/aws/cloudformation/stage0-qa-", "ops/qa/", "ops/archive/",
    "ops/stage0/sync-qa-", "ops/stage0/run-qa-", "ops/stage0/qa-runtime",
)


def matches(path: str, prefixes: tuple[str, ...]) -> bool:
    return any(path.startswith(prefix) for prefix in prefixes)


def runtime_path(path: str) -> bool:
    name = Path(path).name
    return not (matches(path, ("docs/", ".testing/")) or name.endswith(("_test.go", ".md"))
                or name.startswith("test_") or name in {"VERSION", "AGENTS.md", "CLAUDE.md"})


def changed(repo: Path, before: str, after: str) -> list[str]:
    for tag in (before, after):
        if not TAG.fullmatch(tag):
            raise ValueError("component baseline must be an immutable release tag")
        if subprocess.run(["git", "-C", str(repo), "cat-file", "-e", f"v{tag}^{{tree}}"],
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode:
            subprocess.run(["git", "-C", str(repo), "fetch", "--no-tags", "origin",
                            f"refs/tags/v{tag}:refs/tags/v{tag}"], check=True)
    result = subprocess.run(["git", "-C", str(repo), "diff", "--name-only", "--no-renames",
                             f"v{before}", f"v{after}", "--"], check=True, capture_output=True, text=True)
    return [path for path in result.stdout.splitlines() if runtime_path(path)]


def plan(repo: Path, target: str, gateway: str, worker: str, state: dict, rollout: Path) -> dict:
    if not TAG.fullmatch(target) or not TAG.fullmatch(gateway):
        raise ValueError("invalid target or gateway tag")
    contract = release_contract(rollout)
    component = yaml.safe_load(rollout.read_text())["prod"].get("component_release", {})
    if not isinstance(component, dict):
        raise ValueError("component_release must be a mapping")
    independent = component.get("runtime_contract")
    if independent not in (None, "independent_v1"):
        raise ValueError("unsupported independent runtime contract")
    pinned = state.get("runtime", {})
    receipt = state.get("verified", {})
    maintenance_tag = pinned.get("tag", "")
    baseline_valid = (receipt.get("schema_version") == SCHEMA
                      and not state.get("pause_drop", False)
                      and not receipt.get("legacy_rollback", False)
                      and receipt.get("worker_image") == worker
                      and receipt.get("maintenance_tag") == maintenance_tag
                      and receipt.get("runtime_id") == pinned.get("id")
                      and receipt.get("runtime_host_sha") == pinned.get("host_sha")
                      and receipt.get("gateway_tag") == gateway)
    if contract is None or independent != "independent_v1":
        if not verified_worker_image(worker) or not pinned:
            raise ValueError("legacy rollback requires verified worker and pinned maintenance")
        return {"schema_version": SCHEMA, "mode": "legacy_rollback", "target_tag": target, "gateway_tag": target,
                "deploy_gateway": True, "deploy_worker": False, "deploy_maintenance": False,
                "run_canary": False, "legacy_rollback": True, "worker_image": worker,
                "maintenance_tag": maintenance_tag, "host_runtime_mode": "pinned_maintenance",
                "reason": "legacy_rollback_pause_drop"}

    app_paths = changed(repo, gateway, target)
    worker_tag = worker.removeprefix(REPOSITORY + ":")
    worker_paths = changed(repo, worker_tag, target) if TAG.fullmatch(worker_tag) else None
    maint_paths = changed(repo, maintenance_tag, target) if TAG.fullmatch(maintenance_tag) else None
    publisher_tag = receipt.get("publisher_tag", "") if baseline_valid else ""
    publisher_paths = changed(repo, publisher_tag, target) if TAG.fullmatch(publisher_tag) else None
    gateway_changed = any(matches(p, SHARED) or not matches(p, QA_ONLY) for p in app_paths)
    worker_changed = worker_paths is None or any(matches(p, SHARED + WORKER_SURFACE_PATHS) for p in worker_paths)
    maintenance_changed = maint_paths is None or any(matches(p, SHARED + MAINTENANCE) for p in maint_paths)
    canary = (not baseline_valid or worker_changed or publisher_paths is None
              or any(matches(p, SHARED + PUBLISHER_SURFACE_PATHS) for p in publisher_paths))
    # First activation must install the new host/runtime contract and verify it.
    maintenance_changed = maintenance_changed or not baseline_valid
    return {"schema_version": SCHEMA, "mode": "phase3", "target_tag": target,
            "gateway_tag": target if gateway_changed else gateway,
            "deploy_gateway": gateway_changed, "deploy_worker": worker_changed,
            "deploy_maintenance": maintenance_changed, "run_canary": canary,
            "legacy_rollback": False,
            "host_runtime_mode": "independent_maintenance",
            "worker_image": f"{REPOSITORY}:{target}" if worker_changed else worker,
            "maintenance_tag": target if maintenance_changed else maintenance_tag,
            "canary_tag": target if gateway_changed or maintenance_changed else gateway,
            "reason": "component_changes" if baseline_valid else "unverified_combination"}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", required=True)
    parser.add_argument("--gateway", required=True)
    parser.add_argument("--worker", default="")
    parser.add_argument("--state", type=Path, required=True)
    parser.add_argument("--rollout", type=Path, required=True)
    parser.add_argument("--repo", type=Path, default=Path("."))
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    result = plan(args.repo, args.target, args.gateway, args.worker,
                  json.loads(args.state.read_text()), args.rollout)
    result["plan_id"] = hashlib.sha256(json.dumps(result, sort_keys=True).encode()).hexdigest()
    args.output.write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    main()
