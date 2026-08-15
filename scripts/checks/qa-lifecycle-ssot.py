#!/usr/bin/env python3
"""Guard the single QA lifecycle owner and retired conflicting surfaces."""
from __future__ import annotations

import argparse
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
        "prod_fallback: forbidden",
        "pause_capture: false",
    ),
    "ops/qa/README.md": (
        "only target lifecycle owner",
        "single_owner_not_activated",
        "transition-only",
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
    ),
    ".testing/user-stories/stories/US-045-qa-phase2-production-integrity.md": (
        "historical/superseded",
        "single_owner_not_activated",
        "ResumePendingHotCleanups",
        "TestQABoundaryCommandRejectsRetiredCutoverModes",
        "TestQABoundaryProvisionOnlyRefusesAfterSingleOwnerActivation",
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
        "runProvision",
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
    ),
    "docs/approved/design-qa-phase2-archive-closeout.md": (
        "--qa-cutover-provision-only",
        "tokenkey-prod-qa-cutover-provision-v1",
        "legacy age cleanup stays active",
        "Export-orphan cleanup remains under the boundary runner",
        "The boundary is `date_trunc",
        "cutover inventory/activate/provision-only/finalize",
        "Before DEFAULT removal",
        "After DEFAULT removal",
        "the boundary phase owns `qa_exports_tmp`",
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


def _read(root: Path, rel: str) -> str:
    return (root / rel).read_text(encoding="utf-8")


def scan(root: Path) -> list[str]:
    failures: list[str] = []
    for rel in RETIRED:
        if (root / rel).exists():
            failures.append(f"retired QA owner still exists: {rel}")
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

    boundary_command = _read(root, "backend/cmd/server/qa_maintenance_boundary.go")
    if boundary_command.count(BOUNDARY_PRE_ACTIVATION_GUARD) != 2:
        failures.append("every QA boundary mode must fail after single-owner activation")

    for rel in FIXED_AGE_OWNER_SURFACES:
        path = root / rel
        if not path.is_file():
            failures.append(f"required QA owner surface missing: {rel}")
            continue
        body = path.read_text(encoding="utf-8")
        for needle in FIXED_AGE_OWNER_MARKERS:
            if needle in body:
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
        if boundary["observed_live_state"] != "single_owner_not_activated":
            failures.append("boundary observed state must remain not activated")
        if rollout["qa_records"]["partition_owner_repository"] != "tokenkey-qa-maintenance":
            failures.append("repository partition owner drift")
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
            "ops/qa/policy.yaml",
            "ops/qa/deploy_rollout.yaml",
        }:
            target = fixture / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(ROOT / rel, target)
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
        if not any("every QA boundary mode" in item for item in scan(fixture)):
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
        lifecycle_owner.write_text(
            lifecycle_owner.read_text(encoding="utf-8") + "\nfunc RunBoundary() {}\n",
            encoding="utf-8",
        )
        if not any("alternate fixed-age deletion owner" in item for item in scan(fixture)):
            print("self-test failed to detect an alternate fixed-age deletion owner")
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
        deploy_doc = fixture / "docs/deploy/aws-us-openai-gateway-deployment.md"
        deploy_doc.write_text(
            deploy_doc.read_text(encoding="utf-8")
            + "\n`tokenkey-qa-boundary.sh` 是 default-free 小时生命周期 owner\n",
            encoding="utf-8",
        )
        if not any("aws-us-openai-gateway-deployment.md" in item for item in scan(fixture)):
            print("self-test failed to detect the retired deployment-guide owner")
            return 1
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
