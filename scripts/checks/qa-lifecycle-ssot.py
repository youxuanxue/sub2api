#!/usr/bin/env python3
"""Guard the single QA lifecycle owner and retired conflicting surfaces."""
from __future__ import annotations

import argparse
import re
import shutil
import tempfile
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]

RETIRED = (
    "deploy/aws/stage0/tokenkey-qa-stale-cleanup.sh",
    "deploy/aws/stage0/tokenkey-qa-export-orphan.py",
    "ops/stage0/sync-qa-stale-cleanup-timer-via-ssm.sh",  # script-ref-allow-missing
    "ops/qa/prod_qa_stale_cleanup.py",  # script-ref-allow-missing
    "ops/qa/test_prod_qa_stale_cleanup.py",  # script-ref-allow-missing
    "backend/internal/observability/qa/service_traj_export_job.go",
    "backend/internal/observability/qa/service_traj_export.go",
    "backend/internal/observability/qa/lifecycle/cutover_inventory.go",
    "backend/internal/observability/qa/lifecycle/cutover_plan.go",
    "backend/internal/observability/qa/lifecycle/cutover_apply.go",
    "backend/internal/observability/qa/lifecycle/cutover_apply_test.go",
)

REQUIRED = {
    "docs/approved/design-prod-qa-24h-s3-lifecycle.md": (
        "status: approved",
        "tokenkey-qa-maintenance.timer",
        "single_owner_activate",
        "capture seal",
        "restore_verified",
        "source: s3_qa_bundle",
        "immutable S3",
        "qa_export_jobs",
        "prod_fallback: forbidden",
        "pause_capture: false",
        "resolved_worker_image",
        "qa_bundle_release_surface.py",
        "legacy_rollback",
        "bundle_runtime_contract: phase3_v1",
        "当前 release tree 的 Phase 3 runners",
        "按 resolver 的",
        "`run_canary`",
    ),
    "ops/qa/README.md": (
        "only target lifecycle owner",
        "single_owner_not_activated",
        "transition-only",
        "24-hour whole-partition cleanup",
        "App rollback does not imply QA control-plane rollback",
    ),
    "ops/qa/resolve_qa_bundle_worker_image.py": (
        'SUPPORTED_RUNTIME_CONTRACT = "phase3_v1"',
        '"mode": "phase3"',
        '"mode": "legacy_rollback"',
        '"worker_source": "verified_live_worker"',
        'IMAGE_REPOSITORY = "ghcr.io/youxuanxue/sub2api"',
        "worker_surface_changed is False",
    ),
    "ops/qa/qa_bundle_release_surface.py": (
        "WORKER_SURFACE_PATHS",
        "PUBLISHER_SURFACE_PATHS",
        "backend/internal/observability/qa/bundle/",
        "ops/stage0/run-qa-bundle-canary-via-ssm.sh",
    ),
    ".github/workflows/deploy-stage0.yml": (
        "ops/qa/resolve_qa_bundle_worker_image.py",
        "ops/qa/qa_bundle_release_surface.py",
        "steps.qa_infra.outputs.resolved_worker_image",
        "if: steps.qa_infra.outputs.mode == 'legacy_rollback'",
        "QA Phase 3 degraded",
        "--surface-json",
    ),
    "docs/deploy/aws-us-openai-gateway-deployment.md": (
        "tokenkey-qa-maintenance.sh",
        "transition-only",
        "single-owner activation",
        "不再打包",
    ),
    ".testing/user-stories/stories/US-044-qa-lifecycle-single-owner-and-export-contract.md": (
        "S3 Bundle",
        "prod fallback",
        "Observed live: transitional",
        "legacy app 只保留 fully verified live Worker",
    ),
    ".testing/user-stories/stories/US-045-qa-phase2-production-integrity.md": (
        "single_owner_not_activated",
        "ResumePendingHotCleanups",
        "TestQABoundaryCommandRejectsRetiredCutoverModes",
        "TestQABoundaryRunsTransitionCleanupBeforeSingleOwnerActivation",
    ),
    "backend/cmd/server/qa_maintenance.go": (
        "singleOwnerActive",
        "dropArchivedHour",
        "resumeHotCleanup",
        "lifecycle.RunProvision",
        "lifecycle.ResumePendingHotCleanups",
    ),
    "backend/cmd/server/qa_maintenance_boundary.go": (
        "single_owner_activate",
        "qa boundary is retired after single-owner activation",
        "runTransitionBoundary",
    ),
    "backend/cmd/server/qa_bundle_worker.go": (
        "qaBundleWorkerRequested",
        '"--qa-bundle-worker"',
    ),
    "backend/cmd/server/qa_bundle_worker_test.go": (
        "TestQABundleWorkerRequested",
        '[]string{"--qa-bundle-worker"}',
    ),
    "scripts/preflight.sh": (
        "python3 ./scripts/checks/qa-lifecycle-ssot.py; then",
    ),
    "backend/internal/observability/qa/lifecycle/boundary.go": (
        "MaxPendingHotCleanup = 48",
        "LIMIT $1",
        "ResumePendingHotCleanups",
        "exact source-hour upper bound and never authorizes deletion",
    ),
    "ops/stage0/sync-qa-boundary-timer-via-ssm.sh": (
        "qa_single_owner_active",
        "single-owner activation receipt count",
        "systemctl disable tokenkey-qa-boundary.timer",
        "systemctl stop tokenkey-qa-boundary.timer",
    ),
}

FORBIDDEN_TEXT = {
    "backend/cmd/server/qa_maintenance_boundary.go": (
        "--qa-cutover-provision-only",
        "--qa-cutover-inventory",
        "--qa-cutover-plan",
        "--qa-cutover-apply",
        "--qa-cutover-finalize-plan",
        "--qa-cutover-finalize",
    ),
    "scripts/preflight.sh": ("qa-lifecycle-ssot.py --quiet",),
    "ops/stage0/sync-qa-boundary-timer-via-ssm.sh": (
        "qa_exports_tmp",
        "qa-export-orphan",
        "qa-stale-cleanup",
        "qa_finalized",
    ),
    "deploy/aws/stage0/tokenkey-qa-boundary.sh": (
        "qa_exports_tmp",
        "qa-export-orphan",
        "--qa-cutover-provision-only",
        "--qa-cutover-finalize",
    ),
    "deploy/aws/stage0/stage0-ec2-bootstrap.sh": (
        "qa_exports_tmp",
        "qa-export-orphan",
        "qa-stale-cleanup",
    ),
    "deploy/aws/stage0/build-cfn.sh": ("QA_CLEANUP", "QA_EXPORT_ORPHAN"),
    "deploy/aws/cloudformation/stage0-single-ec2.yaml": (
        "qa-stale-cleanup.gzip.b64",
        "qa-export-orphan.gzip.b64",
    ),
    "ops/qa/policy.yaml": (
        "owner: tokenkey-qa-boundary",
        "host_export_tmp_dir",
        "container_export_tmp_dir",
        "cleanup_target_percent",
    ),
    "ops/qa/deploy_rollout.yaml": (
        "tokenkey_qa_stale_cleanup",
        "enabled_after_finalize",
        "owns_export_orphans",
        "finalize_legacy_monthly_policy",
    ),
    "docs/approved/design-prod-qa-24h-s3-lifecycle.md": (
        "Phase 5",
        "eligible export scratch",
        "| export scratch |",
        "boundary orphan cleanup",
        "maintenance 过渡清理",
        "并执行 post-deploy Bundle canary",
    ),
    "docs/deploy/aws-us-openai-gateway-deployment.md": (
        "`tokenkey-qa-boundary.sh` 是 default-free 小时生命周期 owner",
        "`tokenkey-qa-stale-cleanup.sh` 只用于",
        "export-orphan helper 由两者按阶段复用",
    ),
}

FORBIDDEN_TREE_TEXT = {
    "backend/internal/observability/qa/lifecycle": (
        "DropMonthly",
        "drop_empty_monthly",
        "drop empty legacy monthly child",
        "drop empty overlapping monthly child",
    ),
}

FORBIDDEN_LEGACY_PROD_QA_TEXT = {
    "backend/internal/server/routes/user_tk_routes.go": (
        '"/users/me/qa/traj/export"',
        '"/users/me/qa/traj/export/jobs"',
        '"/users/me/qa/traj/export/jobs/:job_id"',
        '"/users/me/qa/traj/exports/*key"',
    ),
}

FORBIDDEN_LEGACY_PROD_QA_TREES = {
    "backend/internal/handler": (
        "func (h *QAHandler) ExportSelfTrajectory(",
        "func (h *QAHandler) GetSelfTrajectoryExportJob(",
        "func (h *QAHandler) ListSelfTrajectoryExports(",
        "func (h *QAHandler) DownloadSelfTrajectoryExport(",
    ),
    "frontend/src/api": (
        "/users/me/qa/traj/",
    ),
}

BOUNDARY_PRE_ACTIVATION_GUARD = "ensureQABoundaryPreActivation(ctx, lockedDB, deps)"

FIXED_AGE_OWNER_SURFACES = (
    "backend/cmd/server/qa_maintenance.go",
    "backend/cmd/server/qa_maintenance_boundary.go",
    "backend/internal/observability/qa/lifecycle/boundary.go",
    "deploy/aws/stage0/tokenkey-qa-maintenance.sh",
    "deploy/aws/stage0/tokenkey-qa-boundary.sh",
    "ops/stage0/sync-qa-maintenance-timer-via-ssm.sh",
    "ops/stage0/sync-qa-boundary-timer-via-ssm.sh",
)

FIXED_AGE_OWNER_MARKERS = (
    "RunBoundary(",
    "DropExpiredHour(",
    "RetentionBoundary(",
    "RETENTION_HOURS",
    "retention_hours",
    "interval '24 hours'",
    "24 * time.Hour",
)

# The existing 24-hour cleanup remains available only through the explicitly
# pre-activation transition owner. Every other fixed-age deletion marker is a
# conflicting lifecycle owner.
TRANSITION_FIXED_AGE_OWNER = {
    "backend/internal/observability/qa/lifecycle/boundary.go": {
        "RetentionBoundary(": "boundary := pgpartition.RetentionBoundary(provision.DBAnchor)",
    },
}

BUNDLE_DEPLOY_OWNER_SURFACES = (
    ".github/workflows/deploy-stage0.yml",
    "ops/stage0/deploy_via_ssm.sh",
    "ops/stage0/deploy_via_ssm_bluegreen.sh",
)

FORBIDDEN_BUNDLE_COORDINATE_PATTERNS = (
    re.compile(r"https://sqs\.[a-z0-9-]+\.amazonaws\.com/[0-9]{12}/[A-Za-z0-9_.-]+"),
    re.compile(r"\btokenkey-[A-Za-z0-9-]*qa-bundles-[0-9]{12}\b"),
)


def _read(root: Path, rel: str) -> str:
    return (root / rel).read_text(encoding="utf-8")


def scan(root: Path) -> list[str]:
    failures: list[str] = []
    for rel in RETIRED:
        if (root / rel).exists():
            failures.append(f"retired QA owner still exists: {rel}")
    for rel in BUNDLE_DEPLOY_OWNER_SURFACES:
        path = root / rel
        if not path.is_file():
            failures.append(f"required QA Bundle deploy owner missing: {rel}")
            continue
        body = path.read_text(encoding="utf-8")
        for pattern in FORBIDDEN_BUNDLE_COORDINATE_PATTERNS:
            match = pattern.search(body)
            if match:
                failures.append(f"hardcoded QA Bundle coordinate remains in {rel}: {match.group(0)}")
    workflow = root / ".github/workflows/deploy-stage0.yml"
    if workflow.is_file():
        workflow_body = workflow.read_text(encoding="utf-8")
        for line in workflow_body.splitlines():
            if "describe-stacks" in line and "QA_STACK_NAME" in line and "|| true" in line:
                failures.append("QA stack discovery must fail closed except for explicit stack-not-found")
        resolved_binding = "QA_BUNDLE_WORKER_IMAGE: ${{ steps.qa_infra.outputs.resolved_worker_image }}"
        if workflow_body.count(resolved_binding) != 2:
            failures.append("QA deploy and verifier must share exactly one resolved Worker image")
        legacy_maintenance = workflow_body.find(
            "name: Converge current QA maintenance runner before legacy app rollback"
        )
        legacy_boundary = workflow_body.find(
            "name: Disable QA boundary before legacy app rollback"
        )
        app_mutation = workflow_body.find("name: Deploy via SSM Run-Command")
        if not (0 <= legacy_maintenance < legacy_boundary < app_mutation):
            failures.append("legacy host safety must converge before app mutation")
        if "QA_BOUNDARY_TIMER_STATE: disabled" not in workflow_body:
            failures.append("legacy rollback must force the QA boundary disabled")
        if "QA_BUNDLE_VERIFY_MODE: discovery" not in workflow_body:
            failures.append("legacy Worker fallback must come from full live discovery")
        if "QA_BUNDLE_WORKER_IMAGE: ghcr.io/youxuanxue/sub2api:${{ env.INPUT_TAG }}" in workflow_body:
            failures.append("QA Bundle Worker image must not be coupled directly to the app tag")
    for rel, needles in REQUIRED.items():
        path = root / rel
        if not path.is_file():
            failures.append(f"required QA SSOT file missing: {rel}")
            continue
        body = _read(root, rel)
        for needle in needles:
            if needle not in body:
                failures.append(f"required QA SSOT anchor missing from {rel}: {needle}")
    for rel, needles in FORBIDDEN_TEXT.items():
        path = root / rel
        if not path.is_file():
            failures.append(f"required QA SSOT file missing: {rel}")
            continue
        body = _read(root, rel)
        for needle in needles:
            if needle in body:
                failures.append(f"retired QA contract remains in {rel}: {needle}")
    for rel, needles in FORBIDDEN_TREE_TEXT.items():
        path = root / rel
        if not path.is_dir():
            failures.append(f"required QA SSOT directory missing: {rel}")
            continue
        for source in path.glob("*.go"):
            body = source.read_text(encoding="utf-8")
            for needle in needles:
                if needle in body:
                    source_rel = source.relative_to(root)
                    failures.append(
                        f"retired monthly cutover owner remains in {source_rel}: {needle}"
                    )

    for rel, needles in FORBIDDEN_LEGACY_PROD_QA_TEXT.items():
        path = root / rel
        if not path.is_file():
            failures.append(f"required QA implementation surface missing: {rel}")
            continue
        body = path.read_text(encoding="utf-8")
        for needle in needles:
            if needle in body:
                failures.append(f"legacy prod QA route remains in {rel}: {needle}")

    for rel, needles in FORBIDDEN_LEGACY_PROD_QA_TREES.items():
        path = root / rel
        if not path.is_dir():
            failures.append(f"required QA implementation surface missing: {rel}")
            continue
        suffix = ".go" if rel.endswith("handler") else ".ts"
        for source in path.glob(f"*{suffix}"):
            if source.name.endswith("_test.go") or source.name.endswith(".spec.ts"):
                continue
            body = source.read_text(encoding="utf-8")
            for needle in needles:
                if needle in body:
                    source_rel = source.relative_to(root)
                    surface = "legacy prod QA handler" if suffix == ".go" else "legacy frontend QA fallback"
                    failures.append(f"{surface} remains in {source_rel}: {needle}")

    boundary_command = _read(root, "backend/cmd/server/qa_maintenance_boundary.go")
    if boundary_command.count(BOUNDARY_PRE_ACTIVATION_GUARD) != 1:
        failures.append("the QA transition boundary must fail after single-owner activation")

    for rel in FIXED_AGE_OWNER_SURFACES:
        path = root / rel
        if not path.is_file():
            failures.append(f"required QA owner surface missing: {rel}")
            continue
        body = path.read_text(encoding="utf-8")
        for needle in FIXED_AGE_OWNER_MARKERS:
            occurrences = body.count(needle)
            allowed_anchor = TRANSITION_FIXED_AGE_OWNER.get(rel, {}).get(needle)
            if allowed_anchor is not None:
                if occurrences != 1 or body.count(allowed_anchor) != 1:
                    failures.append(
                        f"transition fixed-age owner drift in {rel}: {needle}"
                    )
                continue
            if occurrences:
                failures.append(f"alternate fixed-age deletion owner remains in {rel}: {needle}")

    try:
        policy = yaml.safe_load(_read(root, "ops/qa/policy.yaml"))
        lifecycle = policy["prod"]["lifecycle"]
        if lifecycle != {
            "owner": "tokenkey-qa-maintenance",
            "future_horizon_hours": 72,
            "drop_requires_raw_commit": True,
            "drop_requires_restore_verified": True,
            "drop_requires_capture_seal": True,
            "provision_lock_retry_sqlstate": "55P03",
            "provision_lock_retry_backoff_ms": [250, 500, 1000, 2000, 4000, 8000],
        }:
            failures.append("QA policy lifecycle contract drift")
        if policy["prod"]["disk_monitor"]["automatic_cleanup"] is not False:
            failures.append("QA disk monitor must not authorize automatic cleanup")
        if policy["prod"]["disk_monitor"]["pause_capture"] is not False:
            failures.append("QA disk monitor must not pause capture automatically")
        if policy["prod"]["user_qa"] != {
            "entitlement": "users.traj_export_enabled",
            "source": "s3_qa_bundle",
            "compute": "ecs_fargate",
            "download": "direct_s3",
            "job_registry": "immutable_s3_spec",
            "database_job_registry": False,
            "prod_fallback": "forbidden",
        }:
            failures.append("QA user source must remain S3-only with no prod fallback")
    except (KeyError, TypeError, yaml.YAMLError) as exc:
        failures.append(f"QA policy invalid: {exc}")

    try:
        rollout = yaml.safe_load(_read(root, "ops/qa/deploy_rollout.yaml"))["prod"]
        maintenance = rollout["tokenkey_qa_maintenance_timer"]
        boundary = rollout["tokenkey_qa_boundary"]
        if maintenance["repository_closeout_state"] != "single_owner_ready":
            failures.append("maintenance repository readiness drift")
        if maintenance["observed_live_state"] != "single_owner_not_activated":
            failures.append("maintenance observed state must remain not activated")
        if boundary["policy_target_state"] != "disabled_after_single_owner_activate":
            failures.append("boundary target state drift")
        if boundary["repository_closeout_state"] != "pre_activation_fixed_age_transition":
            failures.append("boundary transition repository state drift")
        if boundary["pre_activation_role"] != "provision_and_fixed_age_whole_partition_cleanup":
            failures.append("boundary transition role drift")
        if boundary["pre_activation_retention_hours"] != 24:
            failures.append("boundary transition retention drift")
        if boundary["pre_activation_terminal_gap_policy"] != "persist_before_drop":
            failures.append("boundary transition terminal-gap policy drift")
        if boundary["observed_live_state"] != "single_owner_not_activated":
            failures.append("boundary observed state must remain not activated")
        if rollout["qa_records"]["partition_owner_repository"] != "tokenkey-qa-maintenance":
            failures.append("repository partition owner drift")
        if rollout["user_export"] != {
            "phase3_worker_repository_state": "s3_bundle_ready",
            "phase3_worker_observed_state": "transitional_in_prod",
            "job_registry": "immutable_s3_spec",
            "database_job_registry": "retired",
            "bundle_infra_repository_state": "ready",
            "bundle_infra_observed_state": "not_verified",
            "bundle_runtime_contract": "phase3_v1",
            "bundle_worker_desired_count": 1,
            "bundle_readiness_verifier": "ops/qa/verify_qa_bundle_infra.sh",
            "bundle_canary": "ops/stage0/run-qa-bundle-canary-via-ssm.sh",
        }:
            failures.append("QA Bundle rollout contract drift")
    except (KeyError, TypeError, yaml.YAMLError) as exc:
        failures.append(f"QA rollout invalid: {exc}")
    return failures


def self_test() -> int:
    failures = scan(ROOT)
    if failures:
        for failure in failures:
            print(failure)
        return 1
    with tempfile.TemporaryDirectory() as temp_dir:
        fixture = Path(temp_dir)
        for rel in {
            *REQUIRED,
            *FORBIDDEN_TEXT,
            *FIXED_AGE_OWNER_SURFACES,
            *FORBIDDEN_LEGACY_PROD_QA_TEXT,
            *BUNDLE_DEPLOY_OWNER_SURFACES,
            "ops/qa/policy.yaml",
            "ops/qa/deploy_rollout.yaml",
        }:
            target = fixture / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(ROOT / rel, target)
        for rel in FORBIDDEN_LEGACY_PROD_QA_TREES:
            source_dir = ROOT / rel
            target_dir = fixture / rel
            target_dir.mkdir(parents=True, exist_ok=True)
            suffix = "*.go" if rel.endswith("handler") else "*.ts"
            for source in source_dir.glob(suffix):
                if source.name.endswith("_test.go") or source.name.endswith(".spec.ts"):
                    continue
                shutil.copy2(source, target_dir / source.name)
        retired = fixture / RETIRED[0]
        retired.parent.mkdir(parents=True, exist_ok=True)
        retired.write_text("#!/bin/sh\n", encoding="utf-8")
        if not any("retired QA owner" in item for item in scan(fixture)):
            print("self-test failed to detect a retired deletion owner")
            return 1
        retired.unlink()
        policy = fixture / "ops/qa/policy.yaml"
        policy.write_text(policy.read_text(encoding="utf-8").replace("owner: tokenkey-qa-maintenance", "owner: tokenkey-qa-boundary"), encoding="utf-8")
        if not any("policy" in item.lower() for item in scan(fixture)):
            print("self-test failed to detect lifecycle owner drift")
            return 1
        policy.write_text((ROOT / "ops/qa/policy.yaml").read_text(encoding="utf-8"), encoding="utf-8")
        boundary_command = fixture / "backend/cmd/server/qa_maintenance_boundary.go"
        boundary_command.write_text(
            boundary_command.read_text(encoding="utf-8") + "\n--qa-cutover-provision-only\n",
            encoding="utf-8",
        )
        if not any("--qa-cutover-provision-only" in item for item in scan(fixture)):
            print("self-test failed to detect the retired provision-only mode")
            return 1
        shutil.copy2(ROOT / "backend/cmd/server/qa_maintenance_boundary.go", boundary_command)
        boundary_command.write_text(
            boundary_command.read_text(encoding="utf-8") + "\n--qa-cutover-finalize\n",
            encoding="utf-8",
        )
        if not any("--qa-cutover-finalize" in item for item in scan(fixture)):
            print("self-test failed to detect a retired backend cutover flag")
            return 1
        shutil.copy2(ROOT / "backend/cmd/server/qa_maintenance_boundary.go", boundary_command)
        lifecycle_owner = fixture / "backend/internal/observability/qa/lifecycle/boundary.go"
        lifecycle_owner.write_text(
            lifecycle_owner.read_text(encoding="utf-8") + "\nvar _ = \"drop_empty_monthly\"\n",
            encoding="utf-8",
        )
        if not any("drop_empty_monthly" in item for item in scan(fixture)):
            print("self-test failed to detect a retired monthly cutover owner")
            return 1
        shutil.copy2(ROOT / "backend/internal/observability/qa/lifecycle/boundary.go", lifecycle_owner)
        boundary_command.write_text(
            boundary_command.read_text(encoding="utf-8").replace(
                BOUNDARY_PRE_ACTIVATION_GUARD, "", 1
            ),
            encoding="utf-8",
        )
        if not any("QA transition boundary" in item for item in scan(fixture)):
            print("self-test failed to detect an unguarded boundary mode")
            return 1
        shutil.copy2(ROOT / "backend/cmd/server/qa_maintenance_boundary.go", boundary_command)
        policy = fixture / "ops/qa/policy.yaml"
        for current, replacement, expected in (
            ("source: s3_qa_bundle", "source: prod_database", "S3-only"),
            ("prod_fallback: forbidden", "prod_fallback: allowed", "S3-only"),
            ("pause_capture: false", "pause_capture: true", "pause capture"),
        ):
            body = policy.read_text(encoding="utf-8")
            policy.write_text(body.replace(current, replacement), encoding="utf-8")
            if not any(expected in item for item in scan(fixture)):
                print(f"self-test failed to detect policy drift: {current}")
                return 1
            shutil.copy2(ROOT / "ops/qa/policy.yaml", policy)
        rollout = fixture / "ops/qa/deploy_rollout.yaml"
        rollout.write_text(
            rollout.read_text(encoding="utf-8").replace(
                "repository_closeout_state: pre_activation_fixed_age_transition",
                "repository_closeout_state: pre_activation_provision_only",
            ),
            encoding="utf-8",
        )
        if not any("boundary transition repository state drift" in item for item in scan(fixture)):
            print("self-test failed to detect boundary transition rollout drift")
            return 1
        shutil.copy2(ROOT / "ops/qa/deploy_rollout.yaml", rollout)
        lifecycle_owner.write_text(
            lifecycle_owner.read_text(encoding="utf-8") + "\nfunc RunBoundary() {}\n",
            encoding="utf-8",
        )
        if not any("alternate fixed-age deletion owner" in item for item in scan(fixture)):
            print("self-test failed to detect an alternate fixed-age deletion owner")
            return 1
        shutil.copy2(ROOT / "backend/internal/observability/qa/lifecycle/boundary.go", lifecycle_owner)
        lifecycle_owner.write_text(
            lifecycle_owner.read_text(encoding="utf-8")
            + "\nfunc alternateRetentionOwner() { _ = pgpartition.RetentionBoundary(time.Now()) }\n",
            encoding="utf-8",
        )
        if not any("transition fixed-age owner drift" in item for item in scan(fixture)):
            print("self-test failed to detect a second fixed-age transition owner")
            return 1
        shutil.copy2(ROOT / "backend/internal/observability/qa/lifecycle/boundary.go", lifecycle_owner)
        preflight = fixture / "scripts/preflight.sh"
        preflight.write_text(
            preflight.read_text(encoding="utf-8").replace(
                "qa-lifecycle-ssot.py; then", "qa-lifecycle-ssot.py --quiet; then"
            ),
            encoding="utf-8",
        )
        if not any("--quiet" in item for item in scan(fixture)):
            print("self-test failed to detect the retired sentinel invocation")
            return 1
        workflow = fixture / ".github/workflows/deploy-stage0.yml"
        workflow.write_text(
            workflow.read_text(encoding="utf-8")
            + "\n# https://sqs.us-east-1.amazonaws.com/682751977094/tokenkey-prod-qa-bundle\n",
            encoding="utf-8",
        )
        if not any("hardcoded QA Bundle coordinate" in item for item in scan(fixture)):
            print("self-test failed to detect a hardcoded QA Bundle coordinate")
            return 1
        shutil.copy2(ROOT / ".github/workflows/deploy-stage0.yml", workflow)
        workflow.write_text(
            workflow.read_text(encoding="utf-8").replace(
                "QA_BUNDLE_WORKER_IMAGE: ${{ steps.qa_infra.outputs.resolved_worker_image }}",
                "QA_BUNDLE_WORKER_IMAGE: ghcr.io/youxuanxue/sub2api:${{ env.INPUT_TAG }}",
                1,
            ),
            encoding="utf-8",
        )
        if not any("resolved Worker image" in item for item in scan(fixture)):
            print("self-test failed to detect app/Worker image recoupling")
            return 1
        shutil.copy2(ROOT / ".github/workflows/deploy-stage0.yml", workflow)
        workflow.write_text(
            workflow.read_text(encoding="utf-8")
            + '\nOPS_RECOVERY_PRINCIPAL_ARN="$(aws cloudformation describe-stacks --stack-name "$QA_STACK_NAME" || true)"\n',
            encoding="utf-8",
        )
        if not any("fail closed" in item for item in scan(fixture)):
            print("self-test failed to detect catch-all QA stack discovery fallback")
            return 1
        shutil.copy2(ROOT / ".github/workflows/deploy-stage0.yml", workflow)
        deploy_doc = fixture / "docs/deploy/aws-us-openai-gateway-deployment.md"
        deploy_doc.write_text(
            deploy_doc.read_text(encoding="utf-8")
            + "\n`tokenkey-qa-boundary.sh` 是 default-free 小时生命周期 owner\n",
            encoding="utf-8",
        )
        if not any("aws-us-openai-gateway-deployment.md" in item for item in scan(fixture)):
            print("self-test failed to detect the retired deployment-guide owner")
            return 1
        shutil.copy2(ROOT / "docs/deploy/aws-us-openai-gateway-deployment.md", deploy_doc)
        design = fixture / "docs/approved/design-prod-qa-24h-s3-lifecycle.md"
        design.write_text(
            design.read_text(encoding="utf-8") + "\n并执行 post-deploy Bundle canary\n",
            encoding="utf-8",
        )
        if not any("并执行 post-deploy Bundle canary" in item for item in scan(fixture)):
            print("self-test failed to detect unconditional phase3 canary contract")
            return 1
        shutil.copy2(ROOT / "docs/approved/design-prod-qa-24h-s3-lifecycle.md", design)
        for rel, marker, label in (
            (
                "backend/internal/server/routes/user_tk_routes.go",
                'dualAuth.POST("/users/me/qa/traj/export", h.QA.ExportSelfTrajectory)',
                "legacy prod QA route",
            ),
            (
                "backend/internal/handler/qa_handler_bundle.go",
                "func (h *QAHandler) ExportSelfTrajectory(c *gin.Context) {}",
                "legacy prod QA handler",
            ),
            (
                "frontend/src/api/qaBundle.ts",
                "apiClient.post('/users/me/qa/traj/export')",
                "legacy frontend QA fallback",
            ),
        ):
            target = fixture / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(ROOT / rel, target)
            target.write_text(target.read_text(encoding="utf-8") + "\n" + marker + "\n", encoding="utf-8")
            if not any(label in item for item in scan(fixture)):
                print(f"self-test failed to detect {label}")
                return 1
            shutil.copy2(ROOT / rel, target)
    print("qa lifecycle ssot self-test: ok")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        return self_test()
    failures = scan(ROOT)
    if failures:
        for failure in failures:
            print(failure)
        return 1
    print("qa lifecycle ssot: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
