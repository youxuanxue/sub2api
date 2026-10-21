#!/usr/bin/env python3
"""Guard raw archive deletion safety and reject retired QA product owners."""

from __future__ import annotations

import argparse
import re
import shutil
import subprocess
import tempfile
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]

RETIRED = (
    "deploy/aws/stage0/tokenkey-qa-stale-cleanup.sh",
    "deploy/aws/stage0/tokenkey-qa-export-orphan.py",
    "ops/stage0/sync-qa-stale-cleanup-timer-via-ssm.sh",  # script-ref-allow-missing: retired deletion owner
    "ops/qa/prod_qa_stale_cleanup.py",  # script-ref-allow-missing: retired deletion owner
    "ops/qa/test_prod_qa_stale_cleanup.py",  # script-ref-allow-missing: retired deletion owner
    "backend/internal/observability/qa/service_traj_export_job.go",
    "backend/internal/observability/qa/service_traj_export.go",
    "backend/internal/observability/qa/lifecycle/cutover_inventory.go",
    "backend/internal/observability/qa/lifecycle/cutover_plan.go",
    "backend/internal/observability/qa/lifecycle/cutover_apply.go",
    "backend/internal/observability/qa/lifecycle/cutover_apply_test.go",
    "backend/cmd/server/qa_bundle_worker.go",
    "backend/cmd/server/qa_bundle_canary.go",
    "backend/internal/observability/qa/bundle",
    "frontend/src/api/qaBundle.ts",
    "frontend/src/composables/useTkQABundle.ts",
    "frontend/src/components/keys/QABundlePanel.vue",
    ".github/workflows/deploy-qa-bundle.yml",
)

REQUIRED = {
    "backend/Makefile": (
        "test-integration: test-qa-local",
        "test-qa-local: verify-toolchain",
        'QA_LOCAL_BINARY="$$qa_test_dir/qa-local-maintenance" python3 -m unittest discover -s ops/qa',
    ),
    "backend/cmd/qa-local-maintenance/main.go": (
        "lifecycle.RunProvision(ctx, conn",
        "lifecycle.RunLocalCleanup(ctx, conn",
        "pg_try_advisory_lock($1)",
        '"deletion_authorized": false',
    ),
    "backend/internal/observability/qa/lifecycle/local_cleanup.go": (
        "pgpartition.RetentionBoundary(anchor)",
        "dropFixedAgeHour(ctx, conn",
        "ResumeDroppedHourCleanup(ctx, conn",
        "LOCK TABLE ONLY public.qa_records IN SHARE UPDATE EXCLUSIVE MODE",
    ),
    "ops/qa/local_maintenance_host.py": (
        "require_legacy_paused()",
        "fcntl.LOCK_EX | fcntl.LOCK_NB",
        "hashlib.sha256(binary.read_bytes())",
    ),
    "ops/qa/test_local_maintenance_integration.py": (
        "def test_provision_is_idempotent_accepts_live_writes_and_preserves_old_records(",
        "def test_wrong_parent_key_fails_before_creating_children(",
        "def test_cleanup_plan_then_drop_unarchived_preserves_retention_boundary_and_other_data(",
        "def test_symlink_and_default_partition_block_orphan_deletion(",
        "def test_locked_partition_keeps_rows_and_files_for_next_run(",
    ),
    "backend/internal/observability/qa/archive/reconciler_test.go": (
        "func TestReconcilerVerificationFailureMarksSegmentFailed(",
        "func TestReconcilerMissingEvidenceBlocksShardBeforeUpload(",
    ),
    "backend/internal/observability/qa/lifecycle/boundary_execution_test.go": (
        "func TestDropCommittedHourRollsBackWhenSealChangesAfterLock(",
        "func TestResumePendingHotCleanupsKeepsPerHourFailureVisibleAfterDrop(",
    ),
    "deploy/aws/stage0/qa-runtime.sh": (
        "tk_resolve_qa_runtime",
        "tokenkey-qa-runtime",
        "independent-v1",
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
    "scripts/preflight.sh": ("python3 ./scripts/checks/qa-lifecycle-ssot.py; then",),
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
    "docs/approved/qa-raw-only.md": (
        "status: approved",
        "raw S3",
        "不增加 SSE-C",
        "restore_verified",
        "DROP pause",
    ),
    "ops/qa/README.md": ("tokenkey-qa-maintenance", "raw"),
    "ops/qa/policy.yaml": ("raw_user_access: false",),
    "ops/stage0/deploy_via_ssm_bluegreen.sh": ("assert_qa_provider_unchanged",),
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
}

FORBIDDEN_TREE_TEXT = {
    "backend/internal/observability/qa/lifecycle": (
        "DropMonthly",
        "drop_empty_monthly",
        "drop empty legacy monthly child",
        "drop empty overlapping monthly child",
    )
}

FORBIDDEN_LEGACY_PROD_QA_TEXT = {
    "backend/internal/server/routes/user_tk_routes.go": (
        '"/users/me/qa/traj/export"',
        '"/users/me/qa/traj/export/jobs"',
        '"/users/me/qa/traj/export/jobs/:job_id"',
        '"/users/me/qa/traj/exports/*key"',
    )
}

FORBIDDEN_LEGACY_PROD_QA_TREES = {
    "backend/internal/handler": (
        "func (h *QAHandler) ExportSelfTrajectory(",
        "func (h *QAHandler) GetSelfTrajectoryExportJob(",
        "func (h *QAHandler) ListSelfTrajectoryExports(",
        "func (h *QAHandler) DownloadSelfTrajectoryExport(",
    ),
    "frontend/src/api": ("/users/me/qa/",),
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

TRANSITION_FIXED_AGE_OWNER = {
    "backend/internal/observability/qa/lifecycle/boundary.go": {
        "RetentionBoundary(": "boundary := "
        "pgpartition.RetentionBoundary(provision.DBAnchor)"
    }
}


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
                    surface = (
                        "legacy prod QA handler"
                        if suffix == ".go"
                        else "legacy frontend QA fallback"
                    )
                    failures.append(f"{surface} remains in {source_rel}: {needle}")

    boundary_command = _read(root, "backend/cmd/server/qa_maintenance_boundary.go")
    if boundary_command.count(BOUNDARY_PRE_ACTIVATION_GUARD) != 1:
        failures.append(
            "the QA transition boundary must fail after single-owner activation"
        )

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
                failures.append(
                    f"alternate fixed-age deletion owner remains in {rel}: {needle}"
                )

    try:
        policy = yaml.safe_load(_read(root, "ops/qa/policy.yaml"))
        if policy["prod"]["local_lifecycle"] != {
            "provision_owner": "tokenkey-qa-provision",
            "cleanup_owner": "tokenkey-qa-cleanup",
            "retention_hours": 24,
            "delete_unarchived": True,
            "archive_uploads": "paused",
        }:
            failures.append("independent local retention policy drift")
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
        if (
            policy["prod"]["archive"]["raw_user_access"] is not False
            or "user_qa" in policy["prod"]
        ):
            failures.append("raw archive must remain operations-only")
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
        if (
            boundary["repository_closeout_state"]
            != "pre_activation_fixed_age_transition"
        ):
            failures.append("boundary transition repository state drift")
        if (
            boundary["pre_activation_role"]
            != "provision_and_fixed_age_whole_partition_cleanup"
        ):
            failures.append("boundary transition role drift")
        if boundary["pre_activation_retention_hours"] != 24:
            failures.append("boundary transition retention drift")
        if boundary["pre_activation_terminal_gap_policy"] != "persist_before_drop":
            failures.append("boundary transition terminal-gap policy drift")
        if boundary["observed_live_state"] != "single_owner_not_activated":
            failures.append("boundary observed state must remain not activated")
        if (
            rollout["qa_records"]["partition_owner_repository"]
            != "tokenkey-qa-maintenance"
        ):
            failures.append("repository partition owner drift")
        if (
            "user_export" in rollout
            or rollout["component_release"]["runtime_contract"] != "raw_archive_v1"
        ):
            failures.append("raw-only release contract drift")
        if rollout["component_release"]["observed_live_state"] != "not_deployed":
            failures.append("raw-only release must not claim unobserved deployment")
    except (KeyError, TypeError, yaml.YAMLError) as exc:
        failures.append(f"QA rollout invalid: {exc}")
    failures.extend(scan_retired_product(root))
    return failures


TEXT_SUFFIXES = {".go", ".py", ".sh", ".ts", ".vue", ".yaml", ".yml", ".json", ".html", ".md"}
PRODUCT_MARKER = re.compile(r"QA_BUNDLE|qa_bundle|qa-bundle|QABundle|qaBundle")
EXPORT_PROMISE = re.compile(
    r"authorized trajectory exports|view and export their QA history|"
    r"经授权的会话导出|经授权的用户可查看、导出自己的 QA 历史"
)


def scan_retired_product(root: Path) -> list[str]:
    # Git inventory avoids dependency directories, caches and binary artifacts.
    if root == ROOT:
        names = subprocess.check_output(
            ["git", "ls-files", "--cached", "--others", "--exclude-standard"],
            cwd=root,
            text=True,
        ).splitlines()
        paths = [root / name for name in names]
    else:
        paths = list(root.rglob("*"))
    failures = []
    for path in paths:
        rel = path.relative_to(root).as_posix()
        if not path.is_file() or (path.suffix not in TEXT_SUFFIXES and rel != "Makefile"):
            continue
        if not rel.startswith(
            ("backend/", "frontend/src/", "ops/", "deploy/", ".github/workflows/", ".cursor/skills/")
        ) and rel != "Makefile":
            continue
        if (
            "/ent/" in rel
            or "/migrations/" in rel
            or "test" in path.name.lower()
            or "/__tests__/" in rel
        ):
            continue
        text = path.read_text(encoding="utf-8")
        if PRODUCT_MARKER.search(text):
            failures.append(f"retired QA product runtime remains: {rel}")
        if path.suffix == ".html" and EXPORT_PROMISE.search(text):
            failures.append(f"retired QA export promise remains: {rel}")
    return failures


def self_test() -> int:
    baseline = scan(ROOT)
    if baseline:
        print("\n".join(baseline))
        return 1
    with tempfile.TemporaryDirectory() as directory:
        fixture = Path(directory)
        files = {
            *REQUIRED,
            *FORBIDDEN_TEXT,
            *FIXED_AGE_OWNER_SURFACES,
            *FORBIDDEN_LEGACY_PROD_QA_TEXT,
            "ops/qa/policy.yaml",
            "ops/qa/deploy_rollout.yaml",
        }
        for tree in (*FORBIDDEN_TREE_TEXT, *FORBIDDEN_LEGACY_PROD_QA_TREES):
            files.update(
                p.relative_to(ROOT).as_posix()
                for p in (ROOT / tree).glob("*")
                if p.is_file()
            )
        for rel in files:
            target = fixture / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(ROOT / rel, target)
        if scan(fixture):
            raise AssertionError("fixture must start clean")
        mutations = (
            (
                "backend/Makefile",
                "test-integration: test-qa-local",
                "test-integration:",
                "test-integration: test-qa-local",
            ),
            (
                "ops/qa/policy.yaml",
                "drop_requires_raw_commit: true",
                "drop_requires_raw_commit: false",
                "policy lifecycle",
            ),
            (
                "ops/qa/policy.yaml",
                "drop_requires_restore_verified: true",
                "drop_requires_restore_verified: false",
                "policy lifecycle",
            ),
            (
                "ops/qa/policy.yaml",
                "drop_requires_capture_seal: true",
                "drop_requires_capture_seal: false",
                "policy lifecycle",
            ),
            (
                "ops/qa/policy.yaml",
                "pause_capture: false",
                "pause_capture: true",
                "pause capture",
            ),
            (
                "ops/qa/policy.yaml",
                "raw_user_access: false",
                "raw_user_access: true",
                "operations-only",
            ),
            (
                "backend/cmd/server/qa_maintenance_boundary.go",
                BOUNDARY_PRE_ACTIVATION_GUARD,
                "",
                "transition boundary",
            ),
            (
                "backend/cmd/server/qa_maintenance_boundary.go",
                "",
                "\n--qa-cutover-finalize\n",
                "--qa-cutover-finalize",
            ),
            (
                "backend/internal/observability/qa/lifecycle/boundary.go",
                "",
                "\nRunBoundary(\n",
                "alternate fixed-age",
            ),
            (
                "backend/internal/observability/qa/lifecycle/boundary.go",
                "",
                "\nRetentionBoundary(\n",
                "transition fixed-age",
            ),
            (
                "backend/internal/observability/qa/lifecycle/boundary.go",
                "",
                "\ndrop_empty_monthly\n",
                "monthly cutover",
            ),
            (
                "ops/qa/deploy_rollout.yaml",
                "observed_live_state: not_deployed",
                "observed_live_state: deployed",
                "unobserved deployment",
            ),
        )
        for rel, old, new, expected in mutations:
            target = fixture / rel
            original = target.read_text()
            if old and old not in original:
                raise AssertionError(f"stale mutation: {old}")
            target.write_text(original.replace(old, new, 1) if old else original + new)
            if not any(expected in item for item in scan(fixture)):
                raise AssertionError(f"missed mutation: {rel}: {expected}")
            target.write_text(original)
        for rel in (RETIRED[0], "frontend/src/api/qaBundle.ts"):
            target = fixture / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text("qaBundle")
            if not any("retired" in item for item in scan(fixture)):
                raise AssertionError(f"missed retired surface: {rel}")
            target.unlink()
        for rel, text in (
            ("Makefile", "test: src/api/__tests__/qaBundle.spec.ts\n"),
            (".cursor/skills/release/references/tools.md", "Run deploy-qa-bundle.yml\n"),
            ("deploy/aws/README.md", "Dispatch deploy-qa-bundle.yml\n"),
            ("deploy/aws/stage0/legal-page/privacy.html", "Authorized users can view and export their QA history."),
            ("deploy/aws/stage0/legal-page/privacy.html", "经授权的用户可查看、导出自己的 QA 历史"),
        ):
            target = fixture / rel
            original = target.read_text() if target.exists() else None
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(text)
            if not any("retired QA" in item for item in scan(fixture)):
                raise AssertionError(f"missed retired guidance: {rel}")
            if original is None:
                target.unlink()
            else:
                target.write_text(original)
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
