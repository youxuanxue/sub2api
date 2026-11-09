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
    # QA S3 retired 2026-11-08: local 24h + node1 every 4h replaced these owners.
    "backend/cmd/qa-archive",
    "backend/cmd/server/qa_maintenance.go",
    "backend/cmd/server/qa_maintenance_boundary.go",
    "backend/cmd/server/qa_single_owner_activation.go",
    "backend/internal/observability/qa/archive/store.go",
    "backend/internal/observability/qa/archive/reconciler.go",
    "backend/internal/observability/qa/archive/gap_decision.go",
    "deploy/aws/stage0/tokenkey-qa-maintenance.sh",
    "deploy/aws/stage0/tokenkey-qa-boundary.sh",
    "deploy/aws/stage0/qa-runtime.sh",
    "deploy/aws/stage0/qa-runtime-compose.yml",
    "deploy/aws/cloudformation/stage0-qa-raw-archive.yaml",
    "ops/stage0/sync-qa-maintenance-timer-via-ssm.sh",  # script-ref-allow-missing: retired QA S3 owner
    "ops/stage0/sync-qa-boundary-timer-via-ssm.sh",  # script-ref-allow-missing: retired QA S3 owner
    "ops/stage0/activate-qa-single-owner-via-ssm.sh",  # script-ref-allow-missing: retired QA S3 owner
    "ops/qa/qa_archive_recovery_gate.py",  # script-ref-allow-missing: retired QA S3 owner
    "ops/qa/prod_qa_maintenance.py",  # script-ref-allow-missing: retired QA S3 owner
    "ops/qa/qa_phase2_health.py",  # script-ref-allow-missing: retired QA S3 owner
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
    "backend/internal/observability/qa/lifecycle/boundary_execution_test.go": (
        "func TestDropFixedAgeHourPersistsTerminalGapBeforeDrop(",
        "func TestDropFixedAgeHourPreservesUnknownCommitClassification(",
    ),
    "scripts/preflight.sh": ("python3 ./scripts/checks/qa-lifecycle-ssot.py; then",),
    "backend/internal/observability/qa/lifecycle/boundary.go": (
        "MaxPendingHotCleanup = 48",
        "ResumeDroppedHourCleanup",
        "exact source-hour upper bound and never authorizes deletion",
    ),
    # Superseded 2026-11-08 (QA S3 retired); the doc stays as the history record.
    "docs/approved/qa-raw-only.md": (
        "status: archived",
        "superseded_by: design-tencent-cos-backup-node1-qa.md",
        "raw S3",
        "不增加 SSE-C",
        "restore_verified",
        "DROP pause",
    ),
    "ops/qa/README.md": ("tokenkey-qa-cleanup", "raw"),
    "ops/qa/policy.yaml": ("raw_user_access: false",),
    "ops/stage0/deploy_via_ssm_bluegreen.sh": ("assert_post_cutover_environment",),
}

FORBIDDEN_TEXT = {
    "scripts/preflight.sh": ("qa-lifecycle-ssot.py --quiet",),
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
        "live_archive_coordinates_owner",
    ),
    # QA S3 retired 2026-11-08: deploys must not inject or demand S3 coordinates.
    "ops/stage0/deploy_via_ssm_bluegreen.sh": (
        "assert_live_qa_archive_coordinates",
        "assert_qa_provider_unchanged",
        "QA_LIVE_ARCHIVE_STORAGE_",
        "QA_RUNTIME_KEYS",
        "env_default QA_ARCHIVE_ENABLED",
        "- QA_ARCHIVE_",
        "qa-runtime-environment.sh",
    ),
    "ops/stage0/deploy_via_ssm.sh": ("QA_ARCHIVE_ENABLED:-true", "qa_archive_cmds"),
}

RETIRED_QA_S3_OWNER = "ops/stage0/qa-runtime-environment.sh"  # script-ref-allow-missing: retired QA S3 owner

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

FIXED_AGE_OWNER_SURFACES = (
    "backend/internal/observability/qa/lifecycle/boundary.go",
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

# The gateway transition boundary was retired with QA S3 (2026-11-08); the only
# fixed-age owner is local_cleanup.go, which REQUIRED pins.
TRANSITION_FIXED_AGE_OWNER: dict[str, dict[str, str]] = {}


def _read(root: Path, rel: str) -> str:
    return (root / rel).read_text(encoding="utf-8")


def scan(root: Path) -> list[str]:
    failures: list[str] = []
    for rel in RETIRED:
        if (root / rel).exists():
            failures.append(f"retired QA owner still exists: {rel}")
    if (root / RETIRED_QA_S3_OWNER).exists():
        failures.append(f"retired QA S3 coordinate owner still exists: {RETIRED_QA_S3_OWNER}")
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
            "archive_uploads": "retired",
        }:
            failures.append("independent local retention policy drift")
        if "lifecycle" in policy["prod"]:
            failures.append("retired S3-era QA lifecycle owner returned to policy")
        if policy["prod"]["disk_monitor"]["automatic_cleanup"] is not False:
            failures.append("QA disk monitor must not authorize automatic cleanup")
        if policy["prod"]["disk_monitor"]["pause_capture"] is not False:
            failures.append("QA disk monitor must not pause capture automatically")
        if (
            policy["prod"]["archive"]["raw_user_access"] is not False
            or "user_qa" in policy["prod"]
        ):
            failures.append("raw archive must remain operations-only")
        if policy["prod"]["archive"]["enabled"] is not False:
            failures.append("QA S3 archive is retired; policy archive.enabled must stay false")
    except (KeyError, TypeError, yaml.YAMLError) as exc:
        failures.append(f"QA policy invalid: {exc}")

    try:
        rollout = yaml.safe_load(_read(root, "ops/qa/deploy_rollout.yaml"))["prod"]
        for retired in ("tokenkey_qa_maintenance_timer", "tokenkey_qa_boundary"):
            if retired in rollout:
                failures.append(f"retired S3-era QA owner returned to rollout: {retired}")
        if rollout["qa_records"]["partition_owner_repository"] != "tokenkey-qa-provision":
            failures.append("repository partition owner drift")
        if (
            "user_export" in rollout
            or rollout["component_release"]["runtime_contract"] != "gateway_only_v1"
        ):
            failures.append("raw-only release contract drift")
        archive_gate = rollout["QA_ARCHIVE_ENABLED"]
        if (
            archive_gate.get("state") != "retired"
            or archive_gate.get("deploy_inject_default") is not False
            or archive_gate.get("target_deploy_inject_default") is not False
        ):
            failures.append("QA S3 retirement drift: deploys must not inject QA_ARCHIVE_ENABLED")
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
                "  archive:\n",
                "  lifecycle:\n    owner: tokenkey-qa-maintenance\n\n  archive:\n",
                "retired S3-era QA lifecycle owner",
            ),
            (
                "ops/qa/deploy_rollout.yaml",
                "  qa_records:\n",
                "  tokenkey_qa_boundary:\n    observed_live_state: deployed\n  qa_records:\n",
                "retired S3-era QA owner returned to rollout",
            ),
            (
                "ops/qa/deploy_rollout.yaml",
                "partition_owner_repository: tokenkey-qa-provision",
                "partition_owner_repository: tokenkey-qa-maintenance",
                "repository partition owner drift",
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
                "backend/internal/observability/qa/lifecycle/boundary.go",
                "",
                "\nRunBoundary(\n",
                "alternate fixed-age",
            ),
            (
                "backend/internal/observability/qa/lifecycle/boundary.go",
                "",
                "\nRetentionBoundary(\n",
                "alternate fixed-age",
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
            (
                "ops/qa/deploy_rollout.yaml",
                "deploy_inject_default: false",
                "deploy_inject_default: true",
                "QA S3 retirement drift",
            ),
            (
                "ops/qa/policy.yaml",
                "  archive:\n    enabled: false",
                "  archive:\n    enabled: true",
                "archive.enabled must stay false",
            ),
            (
                "ops/stage0/deploy_via_ssm_bluegreen.sh",
                "",
                "\nassert_live_qa_archive_coordinates\n",
                "assert_live_qa_archive_coordinates",
            ),
            (
                "ops/stage0/deploy_via_ssm_bluegreen.sh",
                "",
                '\n  env_default QA_ARCHIVE_ENABLED "${QA_ARCHIVE_ENABLED:-true}"\n',
                "env_default QA_ARCHIVE_ENABLED",
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
        for rel in (RETIRED[0], "frontend/src/api/qaBundle.ts", RETIRED_QA_S3_OWNER):
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
