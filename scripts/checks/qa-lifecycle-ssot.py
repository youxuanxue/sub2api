#!/usr/bin/env python3
"""Guard the single QA lifecycle owner and retired conflicting surfaces."""
from __future__ import annotations

import argparse
import re
import tempfile
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
SSOT = Path("docs/approved/design-prod-qa-24h-s3-lifecycle.md")
PHASE2_DESIGN = Path("docs/approved/design-qa-phase2-archive-closeout.md")
PHASE2_STORY = Path(
    ".testing/user-stories/stories/US-045-qa-phase2-production-integrity.md"
)
USER_EXPORT_STORY = Path(
    ".testing/user-stories/stories/US-044-qa-lifecycle-single-owner-and-export-contract.md"
)
US044_BASELINE_ANCHOR = "本 Story 只记录已实现的 prod trajectory export 基线"
US044_TARGET_ANCHOR = "未来 S3-only list/detail/export 目标只由主 QA 设计定义"
US045_BASELINE_ANCHOR = "本 Story 只记录已实现的双 timer 固定时龄删除基线"
US045_TARGET_ANCHOR = "未来 archive-gated DROP 与 single maintenance owner 只由主 QA 设计定义"
US044_OBSOLETE_TARGET = "本 Story 只收窄当前路径，Phase 3 再原子迁往 Fargate"
US045_OBSOLETE_TARGET = "archive-gated stale cleanup 被重新引入"
POLICY = Path("ops/qa/policy.yaml")
MAINTENANCE_SCRIPT = Path("deploy/aws/stage0/tokenkey-qa-maintenance.sh")
BOUNDARY_SCRIPT = Path("deploy/aws/stage0/tokenkey-qa-boundary.sh")
CLEANUP_SCRIPT = Path("deploy/aws/stage0/tokenkey-qa-stale-cleanup.sh")
EXPORT_ORPHAN_HELPER = Path("deploy/aws/stage0/tokenkey-qa-export-orphan.py")
BOOTSTRAP = Path("deploy/aws/stage0/stage0-ec2-bootstrap.sh")
LIVE_HOST_ASSERT = Path("ops/stage0/assert-live-host-state.sh")
QA_SERVICE = Path("backend/internal/observability/qa/service.go")
QA_CONFIG = Path("backend/internal/config/config.go")
GO_MAINTENANCE = Path("backend/cmd/server/qa_maintenance.go")
RAW_ARCHIVE_CFN = Path("deploy/aws/cloudformation/stage0-qa-raw-archive.yaml")
RAW_ARCHIVE_DEPLOY = Path("ops/qa/deploy_qa_raw_archive_cfn.sh")
ARCHIVE_CLI = Path("backend/cmd/qa-archive/main.go")
GAP_DECISION_OWNER = Path("backend/internal/observability/qa/archive/gap_decision.go")
GAP_DECISION_RECEIPTS = Path("backend/migrations/tk_075_qa_archive_gap_decision_receipts.sql")
ARCHIVE_OPERATOR = Path("ops/qa/prod_qa_archive_closeout.py")
RECOVERY_GATE = Path("ops/qa/qa_archive_recovery_gate.py")
PREFLIGHT = Path("scripts/preflight.sh")
ARCHIVE_STATE = Path("backend/internal/observability/qa/archive/state.go")
ARCHIVE_CONTROL = Path("backend/internal/observability/qa/archive/control_store.go")
ROLLOUT = Path("ops/qa/deploy_rollout.yaml")
QA_README = Path("ops/qa/README.md")
STALE_OPERATOR = Path("ops/qa/prod_qa_stale_cleanup.py")
DEPLOY_SSM = Path("ops/stage0/deploy_via_ssm.sh")
DEPLOY_BG = Path("ops/stage0/deploy_via_ssm_bluegreen.sh")
LIVE_PROBE = Path("ops/observability/probe-qa-phase2-live-health.sh")
HEALTH_EVALUATOR = Path("ops/qa/qa_phase2_health.py")
CUTOVER_MIGRATION = Path("backend/migrations/tk_074_qa_hourly_cutover_receipts.sql")
BOUNDARY_OWNER_SWITCH = Path("ops/stage0/sync-qa-boundary-timer-via-ssm.sh")
STALE_TIMER_SYNC = Path("ops/stage0/sync-qa-stale-cleanup-timer-via-ssm.sh")
GENERIC_RETENTION_ACTIVATION = Path("ops/archive/data_layer_retention_activation.py")
GENERIC_PARTITION = Path("backend/internal/pkg/pgpartition/partition.go")
DEPLOY_GUIDE = Path("docs/deploy/aws-us-openai-gateway-deployment.md")
DEPLOY_README = Path("deploy/aws/README.md")
DR_RUNBOOK = Path("deploy/aws/RUNBOOK-disaster-recovery.md")
ARCHIVE_README = Path("ops/archive/README.md")
CUTOVER_PLAN = Path("backend/internal/observability/qa/lifecycle/cutover_plan.go")
CUTOVER_APPLY = Path("backend/internal/observability/qa/lifecycle/cutover_apply.go")
LIFECYCLE_EXECUTION_TEST = Path(
    "backend/internal/observability/qa/lifecycle/boundary_execution_test.go"
)

MUST_BE_ABSENT = (
    Path("docs/qa-export-s3-and-auto-archive.md"),
    Path("docs/operator/qa-export-partner.md"),
    Path("ops/prod/qa-export-and-purge.sh"),  # script-ref-allow-missing
    Path("ops/prod/fetch-qa-dump.sh"),  # script-ref-allow-missing
    Path("ops/qa/prod_qa_archive_backfill.py"),  # script-ref-allow-missing
    Path(".testing/user-stories/stories/US-033-qa-self-export-and-synth-fields.md"),
    Path("backend/internal/observability/qa/service_traj_export_auto.go"),
)

FORBIDDEN_BY_FILE = {
    USER_EXPORT_STORY: (
        US044_OBSOLETE_TARGET,
    ),
    PHASE2_STORY: (
        "不表示生产 schema、IAM、timer、恢复或清理已执行",
        "新的 prod T0 activate、至少 25 小时排空",
        US045_OBSOLETE_TARGET,
    ),
    Path("backend/internal/server/routes/user_tk_routes.go"): (
        'POST("/users/me/qa/export"',
        'GET("/users/me/qa/exports/*key"',
    ),
    Path("backend/internal/handler/qa_handler.go"): (
        "func (h *QAHandler) ExportSelf(",
        "func (h *QAHandler) DownloadSelfExport(",
    ),
    Path("backend/internal/observability/qa/service.go"): (
        "func (s *Service) ExportUserData(",
        "func (s *Service) DownloadUserExport(",
        "func (s *Service) DeleteUserData(",
        "StartAutoExportLoop",
    ),
    QA_CONFIG: (
        "AutoExportEnabled",
        'mapstructure:"auto_export_enabled"',
        'qa_capture.retention_days',
    ),
    Path("backend/internal/observability/qa/service_traj_export_job.go"): (
        "ArchiveAuto",
        "exportKindAuto",
        "autoExportJobID",
        "autoExportArtifactTTL",
    ),
    Path("ops/archive/data_layer_archive_rehearsal.py"): (
        '"qa_records"',
        '"qa": 2',
        '"qa-retention-days"',
    ),
    Path("ops/observability/probe-data-layer-retention-inventory.sh"): (
        "QA_RETENTION_DAYS",
        "qa_records",
        "RETBLOB",
        "TOKENKEY_QA_BLOB_DIR",
    ),
    Path("ops/stage0/sync-edge-host-units-via-ssm.sh"): (
        "tokenkey-qa-stale-cleanup",
        "qa-stale-retention.env",
        "TK_QA_STALE_RETENTION_DAYS",
    ),
    Path("ops/stage0/remediate-edge-disk-via-ssm.sh"): (
        "tokenkey-qa-stale-cleanup",
    ),
    BOUNDARY_OWNER_SWITCH: (
        "systemctl enable --now tokenkey-qa-stale-cleanup.timer",
    ),
}

REQUIRED_BY_FILE = {
    SSOT: (
        "status: approved",
        "ops/qa/policy.yaml",
        "ops/qa/deploy_rollout.yaml",
        "forward_cutover",
        "/var/lib/tokenkey/qa-maintenance-last-run.json",
        "durable capture ledger",
        "生产 `Submit` 必须忽略 caller 提供的 `CreatedAt`",
        "heartbeat 只是 Admin/Ops 镜像，不参与 DROP 授权",
        "Bundle 不创建 `records/*` 层",
        "### 8.5 四类存储与备份边界",
        "### 18.1 现状 owner → 唯一目标 owner → 退役门禁",
    ),
    PHASE2_DESIGN: (
        "status: approved",
        "forward_cutover",
        "/var/lib/tokenkey/app/qa_archive_tmp",
        "/var/lib/tokenkey/qa-maintenance-last-run.json",
        "systemd/host-receipt/DB-heartbeat/control-row",
        "qa_exports_tmp",
        "--qa-cutover-provision-only",
        "tokenkey-prod-qa-cutover-provision-v1",
        "qa_archive_gap_decision_receipts",
        "tokenkey-prod-qa-gap-decision-v1:<plan_hash>",
        "segment fingerprint",
        "production_recloseout_verified",
    ),
    PHASE2_STORY: (
        "production_recloseout_state: production_recloseout_verified",
        "retry_release_observation:",
        "2026-08-14 production recloseout",
        US045_BASELINE_ANCHOR,
        US045_TARGET_ANCHOR,
    ),
    USER_EXPORT_STORY: (
        US044_BASELINE_ANCHOR,
        US044_TARGET_ANCHOR,
    ),
    POLICY: (
        "schema_version: 1",
        "capture_enabled: true",
        "online_window_hours: 24",
        "owner: tokenkey-qa-boundary",
        'boundary_schedule_utc: "*:00"',
        "randomized_delay_minutes: 0",
        "future_horizon_hours: 72",
        "lock_timeout_ms: 100",
        'provision_lock_retry_sqlstate: "55P03"',
        "provision_lock_retry_backoff_ms: [250, 500, 1000, 2000, 4000, 8000]",
        "host_receipt_path: /var/lib/tokenkey/qa-boundary-last-run.json",
        "archive:",
        "enabled: true",
        "s3_retention_days: 7",
        "max_catchup_windows_per_run: 1",
        "host_scratch_dir: /var/lib/tokenkey/app/qa_archive_tmp",
        "host_receipt_path: /var/lib/tokenkey/qa-maintenance-last-run.json",
        "host_export_tmp_dir: /var/lib/tokenkey/app/qa_exports_tmp",
        "container_export_tmp_dir: /app/data/qa_exports_tmp",
        "export_tmp_owner: tokenkey-qa-boundary",
        "capture_enabled: false",
        "edge:",
    ),
    ROLLOUT: (
        "schema_version: 1",
        "deploy_inject_default: true",
        "target_deploy_inject_default: true",
        "repository_closeout_state: production_recloseout_verified",
        "observed_live_state: production_recloseout_verified",
        "min_consecutive_scheduled_runs: 2",
        "host_runner: /usr/local/bin/tokenkey-qa-maintenance.sh",
        "health_evaluator: ops/qa/qa_phase2_health.py",
        "live_health_probe: ops/observability/probe-qa-phase2-live-health.sh",
        "live_health_evaluator: ops/qa/prod_phase2_live_health.py",
        "catchup_gap_policy: accepted_terminal",
        "tokenkey_qa_boundary:",
        "policy_target_state: enabled_after_finalize",
        "host_runner: /usr/local/bin/tokenkey-qa-boundary.sh",
        "host_receipt: /var/lib/tokenkey/qa-boundary-last-run.json",
        "database_heartbeat_job: qa-boundary",
        "pre_finalize_provision_mode: qa-cutover-provision-only",
        "pre_finalize_confirmation: tokenkey-prod-qa-cutover-provision-v1",
        "pre_finalize_timer_state: disabled",
        "finalize_legacy_monthly_policy: drop_empty_hash_bound_with_default",
        "lifecycle_role: cutover_drain_only",
        "policy_target_state: disabled_after_finalize",
        "export_orphan_activation_marker: /var/lib/tokenkey/qa-export-orphan-cleanup-activated.json",
        "policy_target: prod.archive.enabled",
        "repository_iam_state: contract_ready",
        "observed_iam_state: applied",
        "iam_contract_verifier: ops/qa/verify_raw_archive_iam_contract.py",
        "iam_contract_reconciler: ops/qa/reconcile_raw_archive_iam_contract.sh",
        "reconcile_raw_archive_iam_contract.sh",
        "partition_owner_repository: qa_lifecycle_boundary",
        "phase3_worker_observed_state: transitional_in_prod",
        "design-qa-phase2-archive-closeout.md",
    ),
    QA_README: (
        "policy.yaml",
        "deploy_rollout.yaml",
        "qa-lifecycle-ssot.py",
        "qa_phase2_health.py",
        "tokenkey-qa-maintenance.sh",
        "tokenkey-qa-boundary.sh",
        "tokenkey-qa-stale-cleanup.sh",
        "apply-export-orphans",
        "prod_qa_cutover_drain_plan",
        "--qa-cutover-provision-only",
        "tokenkey-prod-qa-cutover-provision-v1",
        "gap-plan",
        "gap-apply",
        "failed/source_unavailable_after_retention",
    ),
    DEPLOY_GUIDE: (
        "tokenkey-qa-boundary.sh",
        "cutover drain only",
    ),
    DEPLOY_README: (
        "tokenkey-qa-boundary.sh",
        "cutover drain only",
    ),
    DR_RUNBOOK: (
        "qa_records*` 走 UTC 小时分区 boundary",
    ),
    ARCHIVE_README: (
        "data_layer_retention_activation.py",
        "usage/ops only",
        "It does not query,",
        "plan, or activate QA retention",
    ),
    Path("ops/stage0/deploy_via_ssm.sh"): (
        "edge_qa_capture_cmds",
        "QA_CAPTURE_ENABLED=false",
        "ops/qa/deploy_rollout.yaml",
    ),
    Path("ops/qa/edge_phase1_closeout.py"): (
        "TRUNCATE qa_records",
        "tokenkey-qa-stale-cleanup.timer",
    ),
    Path("ops/qa/prod_phase2_baseline.py"): (
        "tokenkey-prod-qa-raw-archive",
        "QA_CAPTURE_ENABLED",
    ),
    Path("deploy/aws/cloudformation/stage0-qa-raw-archive.yaml"): (
        "QaRawArchiveBucket",
        "raw/v1/",
        "raw/partial/",
        "orphan-evidence-index.jsonl.zst",
        "QaRawArchiveRecoveryRole",
        "QaRawArchiveS3Endpoint",
        "QaRawArchiveDataTrail",
    ),
    RAW_ARCHIVE_DEPLOY: (
        "OPS_RECOVERY_PRINCIPAL_ARN",
        "QA_RAW_ARCHIVE_VPC_ID",
        "QA_RAW_ARCHIVE_ROUTE_TABLE_IDS",
        "iam_boundary=shared_ec2_instance_role_no_process_isolation",
    ),
    Path("ops/qa/reconcile_raw_archive_iam_contract.sh"): (
        "deploy_qa_raw_archive_cfn.sh",
        "verify_raw_archive_iam_contract.py",
        "QA_RAW_ARCHIVE_CONFIRM",
    ),
    ARCHIVE_CLI: (
        "NewReadOnlyObjectStoreForWorkstation",
        "ops-workstation-s3",
        "database_accessed",
        "shared_ec2_instance_role_no_process_isolation",
        "window-bound privacy confirmation required",
        "gap-decision-db-plan",
        "gap-decision-s3-plan",
        "gap-decision-apply",
        "exact plan-hash confirmation required",
        "plan-gzip-base64",
        "maxGapDecisionPlanBytes",
    ),
    GAP_DECISION_OWNER: (
        "BuildGapDecisionDBPlan",
        "CompleteGapDecisionPlanFromStore",
        "ApplyGapDecisionPlan",
        "pg_try_advisory_xact_lock",
        "SegmentFingerprint",
        "PersistApprovedGapTerminal",
        "qa_archive_gap_decision_receipts",
    ),
    GAP_DECISION_RECEIPTS: (
        "qa_archive_gap_decision_receipts",
        "plan_json jsonb NOT NULL",
        "approved_by text NOT NULL",
        "BEFORE UPDATE OR DELETE",
        "BEFORE TRUNCATE",
        "append-only",
    ),
    ARCHIVE_OPERATOR: (
        "gap-plan",
        "gap-apply",
        "GAP_CONFIRMATION_PREFIX",
        "_run_gap_db_plan",
        "_run_gap_s3_plan",
        "_run_gap_apply",
        "target-tag qa-archive binary",
        "MAX_GAP_PLAN_BYTES",
        "plan_gzip_base64",
    ),
    RECOVERY_GATE: (
        "planned_transition_authorized",
        "planned_removal_only",
        "production_evidence_validated",
        "break_glass_state",
    ),
    PREFLIGHT: (
        "QA Phase 2 recovery and IAM contracts",
        "deploy.aws.cloudformation.test_stage0_qa_raw_archive_contract",
        "ops.qa.test_qa_archive_recovery_gate",
    ),
    MAINTENANCE_SCRIPT: (
        "--qa-maintenance-once",
        "tokenkey-prod-qa-maintenance-v1",
        "archive_start",
        "--install-units",
        "/usr/local/lib/tokenkey/resolve-app-container.sh",
        "/var/lib/tokenkey/app/qa_archive_tmp",
        "/var/lib/tokenkey/qa-maintenance-last-run.json",
        '"deletion_authorized": False',
    ),
    GO_MAINTENANCE: (
        "qa_maintenance_archive_only",
        "qa_maintenance_archive",
        "deletion_authorized",
        "archive.NewReconciler",
        "archive.PreviousSealedHour",
        "aggregate_record_count",
    ),
    Path("backend/internal/observability/qa/archive/segment_builder.go"): (
        "BuildSegment",
        "records.parquet",
        "SegmentKindDelta",
        "IntegrityMissingEvidence",
    ),
    Path("backend/internal/observability/qa/archive/reconciler.go"): (
        "CompareAndSwap",
        "commitExistenceUnknown",
        "IntegrityCommitExistenceUnknown",
        "cannot infer an empty source window while commit visibility is denied",
        "conditional create was rejected",
        'commitKey := ShardRelativePrefix(window.Start) + "/commit.json"',
        "DeletionAuthorized: false",
    ),
    ARCHIVE_CONTROL: (
        "WHERE id=$4 AND state IN ('pending','writing','verified','failed')",
        "code.String == IntegrityCommitExistenceUnknown",
    ),
    Path("backend/internal/observability/qa/archive/timeline_selector.go"): (
        "IntegrityCommitExistenceUnknown",
        "CatchupDispositionReconcile",
    ),
    Path("backend/internal/observability/qa/archive/verifier.go"): (
        "VerifyCommit",
        "manifest.BlobMissingCount != 0",
        "IntegrityCorruptArtifact",
    ),
    Path("ops/qa/prod_qa_maintenance.py"): (
        "/usr/local/bin/tokenkey-qa-maintenance.sh",
        "qa-maintenance-runner-v1",
        "deletion_authorized",
    ),
    Path("ops/stage0/sync-qa-maintenance-timer-via-ssm.sh"): (
        "tokenkey-qa-maintenance.timer",
        "/usr/local/lib/tokenkey/resolve-app-container.sh",
        "/var/lib/tokenkey/app/qa_archive_tmp",
    ),
    CLEANUP_SCRIPT: (
        "EXPORT_ORPHAN_HELPER",
        "/var/lib/tokenkey/app/qa_exports_tmp",
        "tokenkey-prod-qa-export-orphan-apply-v1:",
        "qa-export-orphan-cleanup-activated.json",
        "--apply-export-orphans",
    ),
    EXPORT_ORPHAN_HELPER: (
        "QA_EXPORT_TMP_DIR=",
        "/app/data/qa_exports_tmp",
        "qa-export-orphan-plan-v1",
    ),
    STALE_OPERATOR: (
        "apply_export_orphans",
        "--apply-export-orphans",
        "tokenkey-prod-qa-export-orphan-apply-v1:",
    ),
    LIVE_PROBE: (
        "YYYYMMDD_HH24",
        "finalize_receipt_present",
        "activate_t0_utc",
        "activate_plan_hash",
        "default_rows_after_t0",
        "last_error_at, last_error, last_result",
    ),
    Path("backend/cmd/server/qa_maintenance_boundary.go"): (
        "--qa-cutover-provision-only",
        "tokenkey-prod-qa-cutover-provision-v1",
        "runProvisionOnly",
        "provision_attempts=%d",
        "provision_lock_retries=%d",
    ),
    HEALTH_EVALUATOR: (
        "boundary_provision_attempts_invalid",
        "boundary_provision_lock_retries_invalid",
        "boundary_provision_attempts_heartbeat_mismatch",
        "boundary_provision_lock_retries_heartbeat_mismatch",
    ),
    LIFECYCLE_EXECUTION_TEST: (
        "TestRunProvisionRetriesOnlyLockContentionBeforeCoverageCheck",
        "TestRunProvisionDoesNotRetryNearMatchLockTimeoutText",
        "TestRunBoundaryLockRetryExhaustionRemainsFailClosed",
        "TestRunProvisionContextCancellationStopsLockRetry",
    ),
    BOUNDARY_SCRIPT: (
        "--qa-cutover-provision-only",
    ),
    CUTOVER_MIGRATION: (
        "tk_qa_lifecycle_receipts_insert_guard",
        "phase = 'activate' AND t0_utc = NEW.t0_utc",
    ),
    BOUNDARY_OWNER_SWITCH: (
        "JOIN qa_lifecycle_receipts a ON a.t0_utc=f.t0_utc",
        "rollback() { local rc=\\$?; trap - ERR;",
        "systemctl disable --now tokenkey-qa-stale-cleanup.timer || true",
        "systemctl enable --now tokenkey-qa-boundary.timer || true",
    ),
    Path("ops/archive/data_layer_archive_rehearsal.py"): (
        'DATASETS = ("usage", "ops")',
        'POSTGRES_TABLES = ("usage_logs", "ops_system_logs", "ops_error_logs")',
    ),
}


def _policy_failures(root: Path) -> list[str]:
    path = root / POLICY
    try:
        policy = yaml.safe_load(path.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError) as exc:
        return [f"QA policy is not valid YAML: {exc}"]
    if not isinstance(policy, dict):
        return ["QA policy root must be a mapping"]
    expected = {
        ("schema_version",): 1,
        ("prod", "capture_enabled"): True,
        ("prod", "online_window_hours"): 24,
        ("prod", "maintenance_schedule_utc"): "*:15",
        ("prod", "lifecycle", "owner"): "tokenkey-qa-boundary",
        ("prod", "lifecycle", "boundary_schedule_utc"): "*:00",
        ("prod", "lifecycle", "randomized_delay_minutes"): 0,
        ("prod", "lifecycle", "future_horizon_hours"): 72,
        ("prod", "lifecycle", "lock_timeout_ms"): 100,
        ("prod", "lifecycle", "provision_lock_retry_sqlstate"): "55P03",
        ("prod", "lifecycle", "provision_lock_retry_backoff_ms"): [
            250,
            500,
            1000,
            2000,
            4000,
            8000,
        ],
        ("prod", "lifecycle", "host_receipt_path"): "/var/lib/tokenkey/qa-boundary-last-run.json",
        ("prod", "archive", "enabled"): True,
        ("prod", "archive", "shard_minutes"): 60,
        ("prod", "archive", "seal_delay_minutes"): 15,
        ("prod", "archive", "max_catchup_windows_per_run"): 1,
        ("prod", "archive", "catchup_gap_policy"): "accepted_terminal",
        ("prod", "archive", "s3_retention_days"): 7,
        ("prod", "archive", "runner_uid"): 1000,
        ("prod", "archive", "runner_gid"): 1000,
        ("prod", "archive", "host_scratch_dir"): "/var/lib/tokenkey/app/qa_archive_tmp",
        ("prod", "archive", "container_scratch_dir"): "/app/data/qa_archive_tmp",
        ("prod", "archive", "host_receipt_path"): "/var/lib/tokenkey/qa-maintenance-last-run.json",
        ("prod", "cleanup", "host_export_tmp_dir"): "/var/lib/tokenkey/app/qa_exports_tmp",
        ("prod", "cleanup", "container_export_tmp_dir"): "/app/data/qa_exports_tmp",
        ("prod", "cleanup", "export_tmp_owner"): "tokenkey-qa-boundary",
        ("edge", "capture_enabled"): False,
        ("edge", "archive_enabled"): False,
        ("edge", "cleanup_enabled"): False,
        ("edge", "export_enabled"): False,
        ("edge", "s3_access"): False,
    }
    failures: list[str] = []
    for keys, wanted in expected.items():
        value = policy
        try:
            for key in keys:
                value = value[key]
        except (KeyError, TypeError):
            value = None
        if value != wanted:
            failures.append(f"QA policy drift at {'.'.join(keys)}: expected {wanted!r}, got {value!r}")

    prod = policy.get("prod", {})
    if isinstance(prod, dict):
        for retired in (
            "cleanup_schedule_utc",
            "cleanup_randomized_delay_minutes",
            "cleanup_batch_size",
            "physical_cleanup_max_lag_minutes",
        ):
            if retired in prod:
                failures.append(f"retired steady-state QA cleanup policy remains: prod.{retired}")

    archive = prod.get("archive", {}) if isinstance(prod, dict) else {}
    lifecycle = prod.get("lifecycle", {}) if isinstance(prod, dict) else {}
    online_hours = prod.get("online_window_hours")
    maintenance_schedule = prod.get("maintenance_schedule_utc")
    boundary_schedule = lifecycle.get("boundary_schedule_utc") if isinstance(lifecycle, dict) else None
    lock_timeout_ms = lifecycle.get("lock_timeout_ms") if isinstance(lifecycle, dict) else None
    retry_sqlstate = lifecycle.get("provision_lock_retry_sqlstate") if isinstance(lifecycle, dict) else None
    retry_backoff_ms = lifecycle.get("provision_lock_retry_backoff_ms") if isinstance(lifecycle, dict) else None
    raw_retention = archive.get("s3_retention_days") if isinstance(archive, dict) else None
    rendered = {
        MAINTENANCE_SCRIPT: (f"OnCalendar=*-*-* *:{str(maintenance_schedule).split(':')[-1]}:00",),
        BOUNDARY_SCRIPT: (
            f"OnCalendar=*-*-* *:{str(boundary_schedule).split(':')[-1]}:00",
            "/var/lib/tokenkey/qa-boundary-last-run.json",
            "action --mode plan",
            "action --mode apply",
            "--expected-hash",
            f"lock_timeout={lock_timeout_ms}ms",
        ),
        Path("backend/cmd/server/qa_maintenance_boundary.go"): (
            f"SET lock_timeout = '{lock_timeout_ms}ms'",
        ),
        CLEANUP_SCRIPT: (
            f"RETENTION_HOURS={online_hours}",
            "--resume-first",
            "flock -n 9",
            "/var/lib/tokenkey/app/qa_exports_tmp",
            "--apply-export-orphans",
        ),
        EXPORT_ORPHAN_HELPER: (
            "/app/data/qa_exports_tmp",
        ),
        QA_SERVICE: (f"input.CreatedAt.Add({online_hours} * time.Hour)",),
        BOOTSTRAP: (
            "tokenkey-qa-stale-cleanup.sh --install-units /etc/systemd/system",
            "tokenkey-qa-boundary.sh --install-units",
        ),
        RAW_ARCHIVE_CFN: (f"Default: {raw_retention}",),
    }
    for rel, needles in rendered.items():
        try:
            body = (root / rel).read_text(encoding="utf-8")
        except OSError as exc:
            failures.append(f"QA policy runtime file missing: {rel}: {exc}")
            continue
        for needle in needles:
            if needle not in body:
                failures.append(f"QA policy value is not rendered in {rel}: {needle}")
    boundary_command = root / "backend/cmd/server/qa_maintenance_boundary.go"
    try:
        boundary_command_body = boundary_command.read_text(encoding="utf-8")
    except OSError as exc:
        failures.append(f"QA boundary command missing for pooled connection timeouts: {exc}")
    else:
        normalized_boundary_command = re.sub(r"\s+", " ", boundary_command_body)
        expected_connection_options = (
            'qaBoundaryConnectionOptions = "-c '
            f'lock_timeout={lock_timeout_ms}ms -c statement_timeout=120s"'
        )
        expected_dsn_wiring = (
            'dsn := cfg.Database.DSNWithTimezone(cfg.Timezone) + " options=\'" + '
            'qaBoundaryConnectionOptions + "\'"'
        )
        if (
            expected_connection_options not in normalized_boundary_command
            or expected_dsn_wiring not in normalized_boundary_command
        ):
            failures.append("QA pooled connection timeout options drift")
    lifecycle_owner = root / "backend/internal/observability/qa/lifecycle/boundary.go"
    try:
        lifecycle_body = lifecycle_owner.read_text(encoding="utf-8")
    except OSError as exc:
        failures.append(f"QA lifecycle owner missing for lock retry policy: {exc}")
    else:
        normalized = re.sub(r"\s+", " ", lifecycle_body)
        if f'qaProvisionLockContentionSQLState = "{retry_sqlstate}"' not in normalized:
            failures.append("QA provision lock retry SQLSTATE drift")
        duration_tokens: list[str] = []
        if isinstance(retry_backoff_ms, list) and all(
            type(value) is int and value >= 0 for value in retry_backoff_ms
        ):
            for value in retry_backoff_ms:
                if value % 1000 == 0:
                    duration_tokens.append(f"{value // 1000} * time.Second")
                else:
                    duration_tokens.append(f"{value} * time.Millisecond")
        expected_backoff = (
            "var qaProvisionLockRetryBackoff = [...]time.Duration{ "
            + ", ".join(duration_tokens)
            + ", }"
        )
        if not duration_tokens or expected_backoff not in normalized:
            failures.append("QA provision lock retry backoff drift")
    go_maintenance = (root / GO_MAINTENANCE).read_text(encoding="utf-8") if (root / GO_MAINTENANCE).is_file() else ""
    py_maintenance_path = root / "ops/qa/prod_qa_maintenance.py"
    py_maintenance = py_maintenance_path.read_text(encoding="utf-8") if py_maintenance_path.is_file() else ""
    if any(token in go_maintenance + py_maintenance for token in (
        "backfillOnce", "qa-maintenance-backfill-once", "backfill_once", "--backfill-once"
    )):
        failures.append("retired QA backfill state remains in maintenance owner")
    config = (root / QA_CONFIG).read_text(encoding="utf-8") if (root / QA_CONFIG).is_file() else ""
    qa_config = re.search(r"type QACaptureConfig struct \{(?P<body>.*?)\n\}", config, re.DOTALL)
    if not qa_config:
        failures.append("QACaptureConfig owner is missing")
    elif "RetentionDays" in qa_config.group("body") or 'mapstructure:"retention_days"' in qa_config.group("body"):
        failures.append("QACaptureConfig still exposes a second retention owner")
    cleanup = (root / CLEANUP_SCRIPT).read_text(encoding="utf-8") if (root / CLEANUP_SCRIPT).is_file() else ""
    boundary = (root / BOUNDARY_SCRIPT).read_text(encoding="utf-8") if (root / BOUNDARY_SCRIPT).is_file() else ""
    maintenance = (root / MAINTENANCE_SCRIPT).read_text(encoding="utf-8") if (root / MAINTENANCE_SCRIPT).is_file() else ""
    if "RandomizedDelaySec" in boundary:
        failures.append("QA boundary timer must not use randomized delay")
    if "RandomizedDelaySec" in maintenance:
        failures.append("QA archive timer must not use randomized delay")
    state = (root / ARCHIVE_STATE).read_text(encoding="utf-8") if (root / ARCHIVE_STATE).is_file() else ""
    lock_match = re.search(r"MaintenanceAdvisoryLockID int64 = (0x[0-9A-Fa-f]+|[0-9]+)", state)
    if not lock_match:
        failures.append("QA maintenance advisory lock owner is missing")
    elif f"pg_try_advisory_xact_lock({int(lock_match.group(1), 0)})" not in cleanup:
        failures.append("QA cleanup does not use the archive maintenance advisory lock")
    bootstrap = (root / BOOTSTRAP).read_text(encoding="utf-8") if (root / BOOTSTRAP).is_file() else ""
    if "QASVEOF" in bootstrap or "QATIMEOF" in bootstrap:
        failures.append("bootstrap duplicates the QA cleanup systemd owner")
    if "retention_until" in cleanup:
        failures.append("QA cleanup must not read retention_until")
    if "tokenkey-qa-maintenance.timer" in cleanup:
        failures.append("QA cleanup must not depend on maintenance timer state")
    if re.search(r"DELETE\s+FROM\s+qa_export_jobs", cleanup, re.IGNORECASE):
        failures.append("QA cleanup must not delete qa_export_jobs")
    if re.search(r"OnCalendar=\*-\*-\* 04:15:00|Description=Daily QA", cleanup):
        failures.append(f"retired daily QA cleanup schedule remains in {CLEANUP_SCRIPT}")
    live_assert = (root / LIVE_HOST_ASSERT).read_text(encoding="utf-8") if (root / LIVE_HOST_ASSERT).is_file() else ""
    if "qa-stale-retention.env" in live_assert or "TOKENKEY_QA_STALE_RETENTION_DAYS" in live_assert:
        failures.append("live-host assert still reads the retired QA retention owner")
    return failures


def _rollout_failures(root: Path) -> list[str]:
    path = root / ROLLOUT
    if not path.is_file():
        return [f"required QA rollout file missing: {ROLLOUT}"]
    try:
        data = yaml.safe_load(path.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError) as exc:
        return [f"deploy_rollout.yaml invalid: {exc}"]
    if not isinstance(data, dict):
        return ["deploy_rollout.yaml root must be a mapping"]
    failures: list[str] = []
    prod = data.get("prod")
    edge = data.get("edge")
    if not isinstance(prod, dict) or not isinstance(edge, dict):
        return ["deploy_rollout.yaml prod/edge must be mappings"]
    prod_archive = prod.get("QA_ARCHIVE_ENABLED")
    prod_timer = prod.get("tokenkey_qa_maintenance_timer")
    prod_cleanup = prod.get("tokenkey_qa_stale_cleanup")
    prod_boundary = prod.get("tokenkey_qa_boundary")
    edge_capture = edge.get("QA_CAPTURE_ENABLED")
    if not isinstance(prod_archive, dict):
        failures.append("rollout prod.QA_ARCHIVE_ENABLED must be a mapping")
    else:
        if prod_archive.get("deploy_inject_default") is not True:
            failures.append("rollout prod.QA_ARCHIVE_ENABLED.deploy_inject_default must be true")
        if prod_archive.get("policy_target") != "prod.archive.enabled":
            failures.append("rollout prod.QA_ARCHIVE_ENABLED.policy_target drift")
        if prod_archive.get("target_deploy_inject_default") is not True:
            failures.append("rollout prod.QA_ARCHIVE_ENABLED target must become true after closeout")
        if prod_archive.get("repository_closeout_state") != "production_recloseout_verified":
            failures.append("rollout prod.QA_ARCHIVE_ENABLED repository closeout state drift")
        if prod_archive.get("observed_live_state") != "production_recloseout_verified":
            failures.append("rollout prod.QA_ARCHIVE_ENABLED observed live state drift")
    if not isinstance(prod_timer, dict):
        failures.append("rollout prod.tokenkey_qa_maintenance_timer must be a mapping")
    else:
        if prod_timer.get("closeout_deploy_state") != "enabled":
            failures.append("rollout maintenance timer closeout deploy state drift")
        if prod_timer.get("repository_closeout_state") != "production_recloseout_verified":
            failures.append("rollout maintenance timer repository closeout state drift")
        if prod_timer.get("observed_live_state") != "production_recloseout_verified":
            failures.append("rollout maintenance timer observed live state drift")
        if prod_timer.get("policy_target_state") != "enabled":
            failures.append("rollout maintenance timer target drift")
        if prod_timer.get("host_artifact_sync_on_prod_deploy") != "required":
            failures.append("rollout maintenance host artifact sync drift")
        if prod_timer.get("host_artifact_source") != "target_release_tag":
            failures.append("rollout maintenance host artifact source drift")
        if prod_timer.get("sync_active_service_policy") != "bounded_drain_then_replace":
            failures.append("rollout maintenance active-service sync policy drift")
        if prod_timer.get("sync_failure_restore_policy") != "pre_sync_timer_state":
            failures.append("rollout maintenance sync failure restore drift")
        if prod_timer.get("min_consecutive_scheduled_runs") != 2:
            failures.append("rollout maintenance timer observation gate drift")
        if prod_timer.get("catchup_gap_policy") != "accepted_terminal":
            failures.append("rollout maintenance catchup gap policy drift")
        if prod_timer.get("live_health_probe") != "ops/observability/probe-qa-phase2-live-health.sh":
            failures.append("rollout maintenance live health probe drift")
        if prod_timer.get("live_health_evaluator") != "ops/qa/prod_phase2_live_health.py":
            failures.append("rollout maintenance live health evaluator drift")
        if prod_timer.get("health_evidence") != [
            "systemd", "host_receipt", "database_heartbeat", "archive_control_rows"
        ]:
            failures.append("rollout maintenance health evidence drift")
    if not isinstance(prod_cleanup, dict):
        failures.append("rollout prod.tokenkey_qa_stale_cleanup must be a mapping")
    else:
        if prod_cleanup.get("policy_target_state") != "disabled_after_finalize":
            failures.append("rollout stale cleanup target drift")
        if prod_cleanup.get("archive_independent") is not True:
            failures.append("rollout stale cleanup must remain archive-independent")
        if prod_cleanup.get("lifecycle_role") != "cutover_drain_only":
            failures.append("rollout stale cleanup must be cutover drain only")
        if prod_cleanup.get("disable_after_receipt_phase") != "finalize":
            failures.append("rollout stale cleanup finalize gate drift")
    if not isinstance(prod_boundary, dict):
        failures.append("rollout prod.tokenkey_qa_boundary must be a mapping")
    else:
        expected_boundary = {
            "repository_closeout_state": "production_recloseout_verified",
            "observed_live_state": "production_recloseout_verified",
            "policy_target_state": "enabled_after_finalize",
            "host_artifact_sync_on_prod_deploy": "required",
            "host_artifact_source": "target_release_tag",
            "sync_active_service_policy": "bounded_drain_then_replace",
            "sync_failure_restore_policy": "pre_finalize_snapshot_or_finalized_boundary",
            "deploy_owner_restore_mode": "durable_finalize_receipt",
            "schedule_utc": "*:00",
            "randomized_delay_minutes": 0,
            "host_runner": "/usr/local/bin/tokenkey-qa-boundary.sh",
            "host_receipt": "/var/lib/tokenkey/qa-boundary-last-run.json",
            "database_heartbeat_job": "qa-boundary",
            "pre_finalize_provision_mode": "qa-cutover-provision-only",
            "pre_finalize_confirmation": "tokenkey-prod-qa-cutover-provision-v1",
            "pre_finalize_timer_state": "disabled",
            "finalize_legacy_monthly_policy": "drop_empty_hash_bound_with_default",
            "replaces_timer": "tokenkey-qa-stale-cleanup.timer",
            "owns_export_orphans": True,
            "export_orphan_activation_marker": "/var/lib/tokenkey/qa-export-orphan-cleanup-activated.json",
            "health_evidence": [
                "boundary_systemd",
                "boundary_host_receipt",
                "boundary_database_heartbeat",
                "qa_records_catalog",
            ],
        }
        if prod_boundary != expected_boundary:
            failures.append("rollout boundary owner contract drift")
    if not isinstance(edge_capture, dict):
        failures.append("rollout edge.QA_CAPTURE_ENABLED must be a mapping")
    elif edge_capture.get("deploy_inject_default") is not False:
        failures.append("rollout edge.QA_CAPTURE_ENABLED.deploy_inject_default must be false")
    recovery = prod.get("raw_archive_recovery")
    expected_recovery = {
        "repository_state": "ready",
        "repository_iam_state": "contract_ready",
        "observed_iam_state": "applied",
        "iam_contract_verifier": "ops/qa/verify_raw_archive_iam_contract.py",
        "iam_contract_reconciler": "ops/qa/reconcile_raw_archive_iam_contract.sh",
        "independent_evidence_state": "production_workstation_recovery_verified",
        "recovery_cli": "backend/cmd/qa-archive",
        "retirement_gate": "ops/qa/qa_archive_recovery_gate.py",
        "break_glass_state": "retired",
        "shared_role_boundary": "shared_ec2_instance_role_no_process_isolation",
    }
    if not isinstance(recovery, dict) or recovery != expected_recovery:
        failures.append("rollout raw archive recovery contract drift")
    qa_records = prod.get("qa_records")
    if not isinstance(qa_records, dict) or qa_records != {
        "partition_owner_repository": "qa_lifecycle_boundary",
        "partition_owner_observed": "qa_lifecycle_boundary",
    }:
        failures.append("rollout qa_records partition owner contract drift")
    user_export = prod.get("user_export")
    if not isinstance(user_export, dict) or user_export.get("phase3_worker_observed_state") != "transitional_in_prod":
        failures.append("rollout user export phase3 observed state drift")
    return failures


def _deploy_rollout_failures(root: Path) -> list[str]:
    failures: list[str] = []
    for rel in (DEPLOY_SSM, DEPLOY_BG):
        path = root / rel
        if not path.is_file():
            failures.append(f"deploy script missing: {rel}")
            continue
        body = path.read_text(encoding="utf-8")
        if "${QA_ARCHIVE_ENABLED:-true}" not in body:
            failures.append(
                f"{rel} must default QA_ARCHIVE_ENABLED to true (ops/qa/deploy_rollout.yaml)"
            )
        if "ops/qa/deploy_rollout.yaml" not in body:
            failures.append(f"{rel} must reference ops/qa/deploy_rollout.yaml rollout SSOT")
    return failures


def _closeout_implementation_failures(root: Path) -> list[str]:
    failures: list[str] = []
    hourly = root / "backend/internal/pkg/pgpartition/hourly.go"
    lifecycle_pkg = root / "backend/internal/observability/qa/lifecycle/boundary.go"
    if not hourly.is_file():
        failures.append("qa_records hourly partition implementation missing")
    elif "EnsureHourly" not in hourly.read_text(encoding="utf-8"):
        failures.append("qa_records EnsureHourly owner missing")
    if not lifecycle_pkg.is_file():
        failures.append("qa lifecycle boundary owner missing")
    else:
        body = lifecycle_pkg.read_text(encoding="utf-8")
        for needle in ("RunProvision", "RunCutoverProvisionOnly", "DropExpiredHour", "RunBoundary"):
            if needle not in body:
                failures.append(f"qa lifecycle boundary missing {needle}")
        for needle in (
            "result.Attempts++",
            "isProvisionLockContention(err)",
            "result.LockRetries >= len(backoff)",
            "retrySleep(ctx, backoff[result.LockRetries])",
        ):
            if needle not in body:
                failures.append(f"QA provision lock retry behavior drift: missing {needle}")
        run_boundary = body[body.find("func RunBoundary") :]
        provision_call = run_boundary.find("RunProvision(ctx")
        expiry_call = run_boundary.find("runExpiryDrops(ctx")
        if provision_call < 0 or expiry_call < 0 or provision_call > expiry_call:
            failures.append("QA boundary no longer provisions before expiry DROP")
        if "status.VerificationErrorCode == archive.IntegrityCommitExistenceUnknown" not in body:
            failures.append("qa lifecycle boundary must preserve unknown commit existence while dropping hot source")
    cutover_plan = root / CUTOVER_PLAN
    cutover_apply = root / CUTOVER_APPLY
    if not cutover_plan.is_file() or not cutover_apply.is_file():
        failures.append("qa finalize empty-monthly owner missing")
    else:
        plan_body = cutover_plan.read_text(encoding="utf-8")
        apply_body = cutover_apply.read_text(encoding="utf-8")
        if (
            'SchemaVersion: "qa-hourly-cutover-finalize-plan-v2"' not in plan_body
            or "DropMonthly:   dropMonthly" not in plan_body
            or "monthly partition inventory drift" not in apply_body
            or "drop empty legacy monthly child" not in apply_body
        ):
            failures.append("qa finalize must hash-bind and atomically drop empty legacy monthly children")
    rehome = root / "backend/internal/pkg/pgpartition/rehome_default.go"
    if rehome.is_file():
        failures.append("retired qa_records rehome implementation still present")
    gap_owner = root / GAP_DECISION_OWNER
    if gap_owner.is_file():
        body = gap_owner.read_text(encoding="utf-8")
        for forbidden in (
            "qa_archive_gaps",
            "DELETE FROM qa_records",
            "DROP TABLE",
            "s3:PutObject",
            "RehomeDefaultMonthly",
        ):
            if forbidden in body:
                failures.append(f"QA gap decision reintroduced a forbidden owner or mutation: {forbidden}")
    partition_maintenance = root / "backend/internal/pkg/partitionmaintenance/maintenance.go"
    if partition_maintenance.is_file():
        body = partition_maintenance.read_text(encoding="utf-8")
        if "RehomeDefaultMonthly" in body or 'table: "qa_records"' in body:
            failures.append("ops partition maintenance must not own qa_records rehome or provisioning")
    generic_partition = root / GENERIC_PARTITION
    if not generic_partition.is_file():
        failures.append("generic partition provisioner missing")
    else:
        body = generic_partition.read_text(encoding="utf-8")
        if (
            body.count("rejectQAHourlyOwner(table)") != 3
            or "qa_records is owned by EnsureHourly" not in body
        ):
            failures.append(
                "generic partition provisioner can own qa_records; monthly, daily, and generic DROP paths must reject"
            )
    boundary_cmd = root / "backend/cmd/server/qa_maintenance_boundary.go"
    if not boundary_cmd.is_file():
        failures.append("qa boundary maintenance command missing")
    elif "--qa-boundary-once" not in boundary_cmd.read_text(encoding="utf-8"):
        failures.append("qa boundary maintenance entrypoint missing")
    elif "--qa-cutover-plan" not in boundary_cmd.read_text(encoding="utf-8"):
        failures.append("qa cutover plan entrypoint missing")
    boundary_runner = root / BOUNDARY_SCRIPT
    if not boundary_runner.is_file():
        failures.append("qa boundary host runner missing")
    elif "tokenkey-qa-boundary.timer" not in boundary_runner.read_text(encoding="utf-8"):
        failures.append("qa boundary timer unit missing")
    elif "TimeoutStartSec=2400" not in (root / MAINTENANCE_SCRIPT).read_text(encoding="utf-8"):
        failures.append("qa archive maintenance timer must allow 40 minute start window")
    once_partition = root / "backend/cmd/server/partition_maintenance.go"
    if once_partition.is_file() and "partitionmaintenance.Options{}" not in once_partition.read_text(encoding="utf-8"):
        failures.append("one-shot partition maintenance must skip qa_records lifecycle")
    rollout = root / ROLLOUT
    if rollout.is_file():
        try:
            data = yaml.safe_load(rollout.read_text(encoding="utf-8"))
            qa_records = (data.get("prod") or {}).get("qa_records") or {}
            if qa_records.get("partition_owner_repository") != "qa_lifecycle_boundary":
                failures.append("rollout qa_records partition owner must be qa_lifecycle_boundary")
        except (OSError, yaml.YAMLError):
            pass
    return failures


def _ownership_boundary_failures(root: Path) -> list[str]:
    failures: list[str] = []
    required = {
        CLEANUP_SCRIPT: (
            "require_cutover_drain_open",
            "qa_lifecycle_receipts",
            "phase='finalize'",
            "QA cutover drain is closed by the durable finalize receipt",
        ),
        STALE_TIMER_SYNC: (
            "qa_lifecycle_receipts",
            "drain_open=",
            "systemctl disable --now tokenkey-qa-stale-cleanup.timer",
        ),
        STALE_OPERATOR: (
            "prod_qa_cutover_drain_plan",
            "/usr/local/bin/tokenkey-qa-stale-cleanup.sh --plan",
        ),
    }
    labels = {
        CLEANUP_SCRIPT: "legacy QA drain durable finalize guard",
        STALE_TIMER_SYNC: "stale timer finalize guard",
        STALE_OPERATOR: "dedicated cutover drain plan",
    }
    for rel, needles in required.items():
        path = root / rel
        if not path.is_file():
            failures.append(f"{labels[rel]} owner missing: {rel}")
            continue
        body = path.read_text(encoding="utf-8")
        for needle in needles:
            if needle not in body:
                failures.append(f"{labels[rel]} anchor missing from {rel}: {needle}")

    generic_path = root / GENERIC_RETENTION_ACTIVATION
    if not generic_path.is_file():
        failures.append(f"generic usage/ops retention activation missing: {GENERIC_RETENTION_ACTIVATION}")
    else:
        generic_body = generic_path.read_text(encoding="utf-8")
        if re.search(r"(?i)(?<![a-z0-9])qa(?:\b|[_-])", generic_body):
            failures.append(
                "generic retention activation owns QA; QA lifecycle must remain under ops/qa"
            )
    return failures


def scan(root: Path) -> list[str]:
    failures: list[str] = []
    for rel in MUST_BE_ABSENT:
        if (root / rel).exists():
            failures.append(f"retired QA owner still exists: {rel}")

    for rel, needles in FORBIDDEN_BY_FILE.items():
        path = root / rel
        if not path.is_file():
            failures.append(f"required QA contract file missing: {rel}")
            continue
        body = path.read_text(encoding="utf-8")
        for needle in needles:
            if needle in body:
                failures.append(f"retired QA contract reintroduced in {rel}: {needle}")

    for rel, needles in REQUIRED_BY_FILE.items():
        path = root / rel
        if not path.is_file():
            failures.append(f"required QA SSOT file missing: {rel}")
            continue
        body = path.read_text(encoding="utf-8")
        for needle in needles:
            if needle not in body:
                failures.append(f"required QA SSOT anchor missing from {rel}: {needle}")
    failures.extend(_policy_failures(root))
    failures.extend(_rollout_failures(root))
    failures.extend(_deploy_rollout_failures(root))
    failures.extend(_closeout_implementation_failures(root))
    failures.extend(_ownership_boundary_failures(root))
    return failures


def self_test() -> int:
    with tempfile.TemporaryDirectory() as temp_dir:
        root = Path(temp_dir)
        fixture_files = {
            *FORBIDDEN_BY_FILE,
            *REQUIRED_BY_FILE,
            CLEANUP_SCRIPT,
            BOOTSTRAP,
            QA_SERVICE,
            QA_CONFIG,
            GO_MAINTENANCE,
            LIVE_HOST_ASSERT,
            RAW_ARCHIVE_CFN,
            ARCHIVE_STATE,
            ARCHIVE_CONTROL,
            ROLLOUT,
            QA_README,
            DEPLOY_SSM,
            DEPLOY_BG,
            LIVE_PROBE,
            Path("ops/observability/data_layer_archive_health.py"),
            Path("backend/internal/pkg/pgpartition/hourly.go"),
            Path("backend/internal/observability/qa/lifecycle/boundary.go"),
            CUTOVER_PLAN,
            CUTOVER_APPLY,
            Path("backend/cmd/server/qa_maintenance_boundary.go"),
            BOUNDARY_SCRIPT,
            MAINTENANCE_SCRIPT,
            STALE_TIMER_SYNC,
            GENERIC_RETENTION_ACTIVATION,
            GENERIC_PARTITION,
        }
        for rel in fixture_files:
            src = ROOT / rel
            path = root / rel
            path.parent.mkdir(parents=True, exist_ok=True)
            if src.is_file():
                path.write_text(src.read_text(encoding="utf-8"), encoding="utf-8")
                continue
            required = REQUIRED_BY_FILE.get(rel, ())
            path.write_text("\n".join(required) + "\n", encoding="utf-8")
        policy_fixture = """schema_version: 1
prod:
  capture_enabled: true
  online_window_hours: 24
  maintenance_schedule_utc: "*:15"
  lifecycle:
    owner: tokenkey-qa-boundary
    boundary_schedule_utc: "*:00"
    randomized_delay_minutes: 0
    future_horizon_hours: 72
    lock_timeout_ms: 100
    provision_lock_retry_sqlstate: "55P03"
    provision_lock_retry_backoff_ms: [250, 500, 1000, 2000, 4000, 8000]
    host_receipt_path: /var/lib/tokenkey/qa-boundary-last-run.json
  archive:
    enabled: true
    shard_minutes: 60
    seal_delay_minutes: 15
    max_catchup_windows_per_run: 1
    catchup_gap_policy: accepted_terminal
    s3_retention_days: 7
    runner_uid: 1000
    runner_gid: 1000
    host_scratch_dir: /var/lib/tokenkey/app/qa_archive_tmp
    container_scratch_dir: /app/data/qa_archive_tmp
    host_receipt_path: /var/lib/tokenkey/qa-maintenance-last-run.json
  cleanup:
    host_export_tmp_dir: /var/lib/tokenkey/app/qa_exports_tmp
    container_export_tmp_dir: /app/data/qa_exports_tmp
    export_tmp_owner: tokenkey-qa-boundary
edge:
  capture_enabled: false
  archive_enabled: false
  cleanup_enabled: false
  export_enabled: false
  s3_access: false
"""
        (root / POLICY).write_text(policy_fixture, encoding="utf-8")
        with (root / MAINTENANCE_SCRIPT).open("a", encoding="utf-8") as handle:
            handle.write("OnCalendar=*-*-* *:15:00\nTimeoutStartSec=2400\n")
        with (root / CLEANUP_SCRIPT).open("a", encoding="utf-8") as handle:
            handle.write("RETENTION_HOURS=24\nDELETE_BATCH_SIZE=5000\nOnCalendar=*-*-* *:45:00\nRandomizedDelaySec=15min\n--resume-first\nflock -n 9\npg_try_advisory_xact_lock(1363234113)\n")
        with (root / BOUNDARY_SCRIPT).open("a", encoding="utf-8") as handle:
            handle.write(
                "OnCalendar=*-*-* *:00:00\n"
                "/var/lib/tokenkey/qa-boundary-last-run.json\n"
                "action --mode plan\n"
                "action --mode apply\n"
                "--expected-hash\n"
                "lock_timeout=100ms\n"
            )
        with (root / BOOTSTRAP).open("a", encoding="utf-8") as handle:
            handle.write("tokenkey-qa-stale-cleanup.sh --install-units /etc/systemd/system\n")
        with (root / QA_SERVICE).open("a", encoding="utf-8") as handle:
            handle.write("input.CreatedAt.Add(24 * time.Hour)\n")
        with (root / QA_CONFIG).open("a", encoding="utf-8") as handle:
            handle.write("type QACaptureConfig struct {\n  Enabled bool\n}\n")
        with (root / RAW_ARCHIVE_CFN).open("a", encoding="utf-8") as handle:
            handle.write("Default: 7\n")
        with (root / ARCHIVE_STATE).open("a", encoding="utf-8") as handle:
            handle.write("MaintenanceAdvisoryLockID int64 = 0x51414D41\n")
        failures = scan(root)
        if failures:
            print("self-test valid fixture failed:")
            for failure in failures:
                print(f"  - {failure}")
            return 1

        story = root / PHASE2_STORY
        story_body = story.read_text(encoding="utf-8")
        story.write_text(
            story_body.replace(
                "production_recloseout_state: production_recloseout_verified",
                "production_recloseout_state: pending_live_reconciliation",
            ),
            encoding="utf-8",
        )
        failures = scan(root)
        if not any(
            "production_recloseout_state: production_recloseout_verified" in item
            for item in failures
        ):
            print("self-test failed to detect QA Story production recloseout drift")
            return 1
        story.write_text(
            story_body + "\n新的 prod T0 activate、至少 25 小时排空仍须执行。\n",
            encoding="utf-8",
        )
        failures = scan(root)
        if not any("新的 prod T0 activate" in item for item in failures):
            print("self-test failed to detect stale QA Story rollout claim")
            return 1
        story.write_text(story_body, encoding="utf-8")

        story_boundaries = (
            (USER_EXPORT_STORY, US044_BASELINE_ANCHOR, US044_OBSOLETE_TARGET, "US-044"),
            (PHASE2_STORY, US045_TARGET_ANCHOR, US045_OBSOLETE_TARGET, "US-045"),
        )
        for rel, required, forbidden, label in story_boundaries:
            path = root / rel
            body = path.read_text(encoding="utf-8")
            path.write_text(body.replace(required, "", 1), encoding="utf-8")
            failures = scan(root)
            if not any(required in item for item in failures):
                print(f"self-test failed to detect missing {label} scope boundary")
                return 1
            path.write_text(body + f"\n{forbidden}\n", encoding="utf-8")
            failures = scan(root)
            if not any(forbidden in item for item in failures):
                print(f"self-test failed to detect obsolete {label} target wording")
                return 1
            path.write_text(body, encoding="utf-8")

        (root / POLICY).write_text(
            policy_fixture.replace("online_window_hours: 24", "online_window_hours: 25"),
            encoding="utf-8",
        )
        failures = scan(root)
        if not any("online_window_hours" in item for item in failures):
            print("self-test failed to detect QA policy drift")
            return 1
        (root / POLICY).write_text(policy_fixture, encoding="utf-8")

        lifecycle_owner = root / "backend/internal/observability/qa/lifecycle/boundary.go"
        lifecycle_body = lifecycle_owner.read_text(encoding="utf-8")
        lifecycle_owner.write_text(
            lifecycle_body.replace(
                'qaProvisionLockContentionSQLState = "55P03"',
                'qaProvisionLockContentionSQLState = "40001"',
            ),
            encoding="utf-8",
        )
        failures = scan(root)
        if not any("lock retry SQLSTATE drift" in item for item in failures):
            print("self-test failed to detect QA provision lock retry SQLSTATE drift")
            return 1
        lifecycle_owner.write_text(lifecycle_body, encoding="utf-8")

        lifecycle_owner.write_text(
            lifecycle_body.replace(
                "if !isProvisionLockContention(err) || result.LockRetries >= len(backoff) {",
                "if result.LockRetries >= len(backoff) {",
            ),
            encoding="utf-8",
        )
        failures = scan(root)
        if not any("lock retry behavior drift" in item for item in failures):
            print("self-test failed to detect QA provision lock retry behavior drift")
            return 1
        lifecycle_owner.write_text(lifecycle_body, encoding="utf-8")

        boundary_command = root / "backend/cmd/server/qa_maintenance_boundary.go"
        boundary_command_body = boundary_command.read_text(encoding="utf-8")
        boundary_command.write_text(
            boundary_command_body.replace(
                ' + " options=\'" + qaBoundaryConnectionOptions + "\'"',
                "",
            ),
            encoding="utf-8",
        )
        failures = scan(root)
        if not any("pooled connection timeout options" in item for item in failures):
            print("self-test failed to detect QA pooled connection timeout drift")
            return 1
        boundary_command.write_text(boundary_command_body, encoding="utf-8")

        probe = root / LIVE_PROBE
        probe_body = probe.read_text(encoding="utf-8")
        probe.write_text(probe_body.replace("YYYYMMDD_HH24", "YYYYMMDD_HH"), encoding="utf-8")
        failures = scan(root)
        if not any("YYYYMMDD_HH24" in item for item in failures):
            print("self-test failed to detect 12-hour QA partition-name probe drift")
            return 1
        probe.write_text(probe_body, encoding="utf-8")

        cleanup = root / CLEANUP_SCRIPT
        cleanup_body = cleanup.read_text(encoding="utf-8")
        cleanup.write_text(
            cleanup_body.replace("phase='finalize'", "phase='retired'"),
            encoding="utf-8",
        )
        failures = scan(root)
        if not any("durable finalize" in item for item in failures):
            print("self-test failed to detect legacy QA drain finalize-guard removal")
            return 1
        cleanup.write_text(cleanup_body, encoding="utf-8")

        timer_sync = root / STALE_TIMER_SYNC
        timer_sync_body = timer_sync.read_text(encoding="utf-8")
        timer_sync.write_text(
            timer_sync_body.replace("qa_lifecycle_receipts", "qa_retired_receipts"),
            encoding="utf-8",
        )
        failures = scan(root)
        if not any("stale timer finalize" in item for item in failures):
            print("self-test failed to detect stale timer finalize-guard removal")
            return 1
        timer_sync.write_text(timer_sync_body, encoding="utf-8")

        stale_operator = root / STALE_OPERATOR
        stale_operator_body = stale_operator.read_text(encoding="utf-8")
        stale_operator.write_text(
            stale_operator_body.replace(
                "prod_qa_cutover_drain_plan", "prod_qa_generic_retention_plan"
            ),
            encoding="utf-8",
        )
        failures = scan(root)
        if not any("dedicated cutover drain plan" in item for item in failures):
            print("self-test failed to detect dedicated QA drain-plan removal")
            return 1
        stale_operator.write_text(stale_operator_body, encoding="utf-8")

        generic_activation = root / GENERIC_RETENTION_ACTIVATION
        generic_activation_body = generic_activation.read_text(encoding="utf-8")
        generic_activation.write_text(
            generic_activation_body + "\nQA_RETENTION_DAYS = 24\n",
            encoding="utf-8",
        )
        failures = scan(root)
        if not any("generic retention activation owns QA" in item for item in failures):
            print("self-test failed to detect generic retention reacquiring QA ownership")
            return 1
        generic_activation.write_text(generic_activation_body, encoding="utf-8")

        generic_partition = root / GENERIC_PARTITION
        generic_partition_body = generic_partition.read_text(encoding="utf-8")
        generic_partition.write_text(
            generic_partition_body.replace("rejectQAHourlyOwner(table)", "nil"),
            encoding="utf-8",
        )
        failures = scan(root)
        if not any("generic partition provisioner can own qa_records" in item for item in failures):
            print("self-test failed to detect generic QA partition-owner guard removal")
            return 1
        generic_partition.write_text(generic_partition_body, encoding="utf-8")

        cutover_apply = root / CUTOVER_APPLY
        cutover_apply_body = cutover_apply.read_text(encoding="utf-8")
        cutover_apply.write_text(
            cutover_apply_body.replace(
                "drop empty legacy monthly child", "reject every legacy monthly child"
            ),
            encoding="utf-8",
        )
        failures = scan(root)
        if not any("atomically drop empty legacy monthly" in item for item in failures):
            print("self-test failed to detect finalize empty-monthly owner removal")
            return 1
        cutover_apply.write_text(cutover_apply_body, encoding="utf-8")

        preflight = root / PREFLIGHT
        preflight.write_text(
            preflight.read_text(encoding="utf-8").replace(
                "ops.qa.test_qa_archive_recovery_gate",
                "ops.qa.test_removed_recovery_gate",
            ),
            encoding="utf-8",
        )
        failures = scan(root)
        if not any("ops.qa.test_qa_archive_recovery_gate" in item for item in failures):
            print("self-test failed to detect recovery contract test unwiring")
            return 1
        preflight.write_text((ROOT / PREFLIGHT).read_text(encoding="utf-8"), encoding="utf-8")

        retired = root / MUST_BE_ABSENT[0]
        retired.parent.mkdir(parents=True, exist_ok=True)
        retired.write_text("old owner\n", encoding="utf-8")
        route = root / "backend/internal/server/routes/user_tk_routes.go"
        route.write_text('dualAuth.POST("/users/me/qa/export", handler)\n', encoding="utf-8")
        failures = scan(root)
        if not any("retired QA owner still exists" in item for item in failures):
            print("self-test failed to detect retired file")
            return 1
        if not any("retired QA contract reintroduced" in item for item in failures):
            print("self-test failed to detect retired route")
            return 1
        retired.unlink()
        route.write_text("retired surface absent\n", encoding="utf-8")
        (root / RECOVERY_GATE).unlink()
        failures = scan(root)
        if not any(str(RECOVERY_GATE) in item for item in failures):
            print("self-test failed to detect missing recovery retirement gate")
            return 1
    print("qa lifecycle SSOT self-test: OK")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--self-test", action="store_true")
    parser.add_argument("--quiet", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        return self_test()

    failures = scan(ROOT)
    if failures:
        for failure in failures:
            print(f"FAIL: {failure}")
        return 1
    if not args.quiet:
        print(f"qa lifecycle SSOT: OK ({SSOT})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
