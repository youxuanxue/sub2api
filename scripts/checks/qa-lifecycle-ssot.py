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
POLICY = Path("ops/qa/policy.yaml")
MAINTENANCE_SCRIPT = Path("deploy/aws/stage0/tokenkey-qa-maintenance.sh")
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
RECOVERY_GATE = Path("ops/qa/qa_archive_recovery_gate.py")
PREFLIGHT = Path("scripts/preflight.sh")
ARCHIVE_STATE = Path("backend/internal/observability/qa/archive/state.go")
ROLLOUT = Path("ops/qa/deploy_rollout.yaml")
QA_README = Path("ops/qa/README.md")
STALE_OPERATOR = Path("ops/qa/prod_qa_stale_cleanup.py")
DEPLOY_SSM = Path("ops/stage0/deploy_via_ssm.sh")
DEPLOY_BG = Path("ops/stage0/deploy_via_ssm_bluegreen.sh")

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
}

REQUIRED_BY_FILE = {
    SSOT: (
        "status: approved",
        "ops/qa/policy.yaml",
        "ops/qa/deploy_rollout.yaml",
        "forward_cutover",
        "/var/lib/tokenkey/qa-maintenance-last-run.json",
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
    ),
    POLICY: (
        "schema_version: 1",
        "capture_enabled: true",
        "online_window_hours: 24",
        "cleanup_schedule_utc",
        "archive:",
        "enabled: true",
        "s3_retention_days: 7",
        "max_catchup_windows_per_run: 1",
        "host_scratch_dir: /var/lib/tokenkey/app/qa_archive_tmp",
        "host_receipt_path: /var/lib/tokenkey/qa-maintenance-last-run.json",
        "host_export_tmp_dir: /var/lib/tokenkey/app/qa_exports_tmp",
        "container_export_tmp_dir: /app/data/qa_exports_tmp",
        "capture_enabled: false",
        "edge:",
    ),
    ROLLOUT: (
        "schema_version: 1",
        "deploy_inject_default: true",
        "target_deploy_inject_default: true",
        "repository_closeout_state: implementation_ready_pending_live_verification",
        "observed_live_state: pending_live_reconciliation",
        "min_consecutive_scheduled_runs: 2",
        "host_runner: /usr/local/bin/tokenkey-qa-maintenance.sh",
        "health_evaluator: ops/qa/qa_phase2_health.py",
        "live_health_probe: ops/observability/probe-qa-phase2-live-health.sh",
        "live_health_evaluator: ops/qa/prod_phase2_live_health.py",
        "catchup_gap_policy: accepted_terminal",
        "export_orphan_activation_marker: /var/lib/tokenkey/qa-export-orphan-cleanup-activated.json",
        "policy_target: prod.archive.enabled",
        "repository_iam_state: contract_ready",
        "observed_iam_state: pending_live_verification",
        "iam_contract_verifier: ops/qa/verify_raw_archive_iam_contract.py",
        "iam_contract_reconciler: ops/qa/reconcile_raw_archive_iam_contract.sh",
        "reconcile_raw_archive_iam_contract.sh",
        "partition_owner_repository: ops_partition_maintenance",
        "phase3_worker_observed_state: transitional_in_prod",
        "design-qa-phase2-archive-closeout.md",
    ),
    QA_README: (
        "policy.yaml",
        "deploy_rollout.yaml",
        "qa-lifecycle-ssot.py",
        "qa_phase2_health.py",
        "tokenkey-qa-maintenance.sh",
        "tokenkey-qa-stale-cleanup.sh",
        "apply-export-orphans",
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
        'commitKey := ShardRelativePrefix(window.Start) + "/commit.json"',
        "DeletionAuthorized: false",
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
        ("prod", "cleanup_schedule_utc"): "*:45",
        ("prod", "cleanup_randomized_delay_minutes"): 15,
        ("prod", "cleanup_batch_size"): 5000,
        ("prod", "physical_cleanup_max_lag_minutes"): 75,
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
        ("prod", "cleanup", "export_tmp_owner"): "tokenkey-qa-stale-cleanup",
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
    if isinstance(prod, dict) and (
        prod.get("physical_cleanup_max_lag_minutes")
        != 60 + prod.get("cleanup_randomized_delay_minutes", -1)
    ):
        failures.append("QA cleanup max lag must equal hourly cadence plus randomized delay")

    archive = prod.get("archive", {}) if isinstance(prod, dict) else {}
    online_hours = prod.get("online_window_hours")
    maintenance_schedule = prod.get("maintenance_schedule_utc")
    cleanup_schedule = prod.get("cleanup_schedule_utc")
    cleanup_delay = prod.get("cleanup_randomized_delay_minutes")
    cleanup_batch = prod.get("cleanup_batch_size")
    max_lag = prod.get("physical_cleanup_max_lag_minutes")
    raw_retention = archive.get("s3_retention_days") if isinstance(archive, dict) else None
    rendered = {
        MAINTENANCE_SCRIPT: (f"OnCalendar=*-*-* *:{str(maintenance_schedule).split(':')[-1]}:00",),
        CLEANUP_SCRIPT: (
            f"RETENTION_HOURS={online_hours}",
            f"DELETE_BATCH_SIZE={cleanup_batch}",
            f"OnCalendar=*-*-* *:{str(cleanup_schedule).split(':')[-1]}:00",
            f"RandomizedDelaySec={cleanup_delay}min",
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
        if prod_archive.get("repository_closeout_state") != "implementation_ready_pending_live_verification":
            failures.append("rollout prod.QA_ARCHIVE_ENABLED repository closeout state drift")
        if prod_archive.get("observed_live_state") != "pending_live_reconciliation":
            failures.append("rollout prod.QA_ARCHIVE_ENABLED observed live state drift")
    if not isinstance(prod_timer, dict):
        failures.append("rollout prod.tokenkey_qa_maintenance_timer must be a mapping")
    else:
        if prod_timer.get("closeout_deploy_state") != "enabled":
            failures.append("rollout maintenance timer closeout deploy state drift")
        if prod_timer.get("repository_closeout_state") != "implementation_ready_pending_live_verification":
            failures.append("rollout maintenance timer repository closeout state drift")
        if prod_timer.get("observed_live_state") != "pending_live_reconciliation":
            failures.append("rollout maintenance timer observed live state drift")
        if prod_timer.get("policy_target_state") != "enabled":
            failures.append("rollout maintenance timer target drift")
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
        if prod_cleanup.get("policy_target_state") != "enabled":
            failures.append("rollout stale cleanup target drift")
        if prod_cleanup.get("archive_independent") is not True:
            failures.append("rollout stale cleanup must remain archive-independent")
        if prod_cleanup.get("activation_state") != "production_export_orphan_activated":
            failures.append("rollout export orphan activation evidence drift")
    if not isinstance(edge_capture, dict):
        failures.append("rollout edge.QA_CAPTURE_ENABLED must be a mapping")
    elif edge_capture.get("deploy_inject_default") is not False:
        failures.append("rollout edge.QA_CAPTURE_ENABLED.deploy_inject_default must be false")
    recovery = prod.get("raw_archive_recovery")
    expected_recovery = {
        "repository_state": "ready",
        "repository_iam_state": "contract_ready",
        "observed_iam_state": "pending_live_verification",
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
        "partition_owner_repository": "ops_partition_maintenance",
        "partition_owner_observed": "pending_live_probe",
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
    probe = root / "ops/observability/probe-qa-phase2-live-health.sh"
    if probe.is_file():
        body = probe.read_text(encoding="utf-8")
        if "docker exec -i " not in body and "docker exec -i" not in body.replace("\n", " "):
            failures.append("phase2 live probe must use docker exec -i for psql stdin")
    maintenance = root / GO_MAINTENANCE
    if maintenance.is_file():
        body = maintenance.read_text(encoding="utf-8")
        if "compensation_terminal" not in body:
            failures.append("qa maintenance must fail closed on terminal compensation")
    rehome = root / "backend/internal/pkg/pgpartition/rehome_default.go"
    if not rehome.is_file():
        failures.append("qa_records default rehome implementation missing")
    elif "PARTITION OF" not in rehome.read_text(encoding="utf-8"):
        failures.append("qa_records rehome must attach bounded partitions after draining DEFAULT")
    elif "_rehome_staging" not in rehome.read_text(encoding="utf-8"):
        failures.append("qa_records rehome must copy into detached staging before finalize attach")
    elif "copyDefaultToStaging" not in rehome.read_text(encoding="utf-8"):
        failures.append("qa_records rehome must copy-only into staging until finalize transaction")
    elif "created_at" not in rehome.read_text(encoding="utf-8") or "request_id" not in rehome.read_text(encoding="utf-8"):
        failures.append("qa_records rehome dedup must use (created_at, request_id) composite identity")
    elif "rehome requires dedup identity columns" not in rehome.read_text(encoding="utf-8"):
        failures.append("qa_records rehome must fail closed when dedup identity is missing")
    elif "SHARE ROW EXCLUSIVE" not in rehome.read_text(encoding="utf-8"):
        failures.append("qa_records rehome finalize must lock parent table against concurrent capture")
    partition_maintenance = root / "backend/internal/pkg/partitionmaintenance/maintenance.go"
    if partition_maintenance.is_file():
        body = partition_maintenance.read_text(encoding="utf-8")
        rehome_at = body.find("RehomeDefaultMonthly(")
        ensure_at = body.find("EnsureMonthly(ctx, db, target.table")
        if rehome_at < 0 or ensure_at < 0 or rehome_at > ensure_at:
            failures.append("qa_records rehome must run before EnsureMonthly in partition maintenance")
        if "opts.RehomeQaRecordsDefault" not in body:
            failures.append("qa_records rehome must be gated behind explicit maintainer options")
        if "OpsCleanupOptions" not in body:
            failures.append("ops cleanup rehome budget must be declared in OpsCleanupOptions")
        if "PendingFinalize" not in body or "BudgetExhausted" not in body:
            failures.append("partition maintenance must skip EnsureMonthly while qa_records rehome is partial")
        if "rehome_remaining=" not in body:
            failures.append("partition maintenance heartbeat must expose partial default_rehome receipt")
    host_runner = root / MAINTENANCE_SCRIPT
    if host_runner.is_file():
        body = host_runner.read_text(encoding="utf-8")
        for forbidden in (
            "--partition-maintenance-once",
            "run_partition_maintenance",
            "tokenkey-prod-partition-maintenance-v1",
        ):
            if forbidden in body:
                failures.append(
                    f"qa host runner must not invoke partition maintenance ({forbidden})"
                )
    once_partition = root / "backend/cmd/server/partition_maintenance.go"
    if once_partition.is_file():
        body = once_partition.read_text(encoding="utf-8")
        if "statement_timeout = '120s'" in body:
            failures.append("one-shot partition maintenance must keep approved 5s statement timeout")
        if "partitionmaintenance.Options{}" not in body:
            failures.append("one-shot partition maintenance must skip qa_records default rehome")
    ops_cleanup = root / "backend/internal/service/ops_cleanup_service.go"
    if ops_cleanup.is_file():
        body = ops_cleanup.read_text(encoding="utf-8")
        if "partitionmaintenance.OpsCleanupOptions" not in body:
            failures.append("OpsCleanupService must own qa_records rehome via OpsCleanupOptions")
    archive_health = root / "ops/observability/data_layer_archive_health.py"
    if archive_health.is_file():
        body = archive_health.read_text(encoding="utf-8")
        if "TAIL_EXPORT_MAX_AGE = dt.timedelta(days=7)" in body:
            failures.append("archive health stale threshold must follow ops retention SSOT")
        if "_ops_retention_days" not in body:
            failures.append("archive health must derive tail export freshness from retention SSOT")
    rollout = root / ROLLOUT
    if rollout.is_file():
        try:
            data = yaml.safe_load(rollout.read_text(encoding="utf-8"))
            prod_archive = (data.get("prod") or {}).get("QA_ARCHIVE_ENABLED") or {}
            if prod_archive.get("repository_closeout_state") == "production_closeout_verified":
                failures.append("rollout repository closeout must not claim production_closeout_verified before live reconciliation")
        except (OSError, yaml.YAMLError):
            pass
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
            ROLLOUT,
            QA_README,
            DEPLOY_SSM,
            DEPLOY_BG,
            Path("ops/observability/probe-qa-phase2-live-health.sh"),
            Path("ops/observability/data_layer_archive_health.py"),
            Path("backend/internal/pkg/pgpartition/rehome_default.go"),
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
  cleanup_schedule_utc: "*:45"
  cleanup_randomized_delay_minutes: 15
  cleanup_batch_size: 5000
  physical_cleanup_max_lag_minutes: 75
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
    export_tmp_owner: tokenkey-qa-stale-cleanup
edge:
  capture_enabled: false
  archive_enabled: false
  cleanup_enabled: false
  export_enabled: false
  s3_access: false
"""
        (root / POLICY).write_text(policy_fixture, encoding="utf-8")
        with (root / MAINTENANCE_SCRIPT).open("a", encoding="utf-8") as handle:
            handle.write("OnCalendar=*-*-* *:15:00\n")
        with (root / CLEANUP_SCRIPT).open("a", encoding="utf-8") as handle:
            handle.write("RETENTION_HOURS=24\nDELETE_BATCH_SIZE=5000\nOnCalendar=*-*-* *:45:00\nRandomizedDelaySec=15min\n--resume-first\nflock -n 9\npg_try_advisory_xact_lock(1363234113)\n")
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

        (root / POLICY).write_text(
            policy_fixture.replace("online_window_hours: 24", "online_window_hours: 25"),
            encoding="utf-8",
        )
        failures = scan(root)
        if not any("online_window_hours" in item for item in failures):
            print("self-test failed to detect QA policy drift")
            return 1
        (root / POLICY).write_text(policy_fixture, encoding="utf-8")

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
