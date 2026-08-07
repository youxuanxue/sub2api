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
POLICY = Path("ops/qa/policy.yaml")
MAINTENANCE_SCRIPT = Path("deploy/aws/stage0/tokenkey-qa-maintenance.sh")
CLEANUP_SCRIPT = Path("deploy/aws/stage0/tokenkey-qa-stale-cleanup.sh")
BOOTSTRAP = Path("deploy/aws/stage0/stage0-ec2-bootstrap.sh")
LIVE_HOST_ASSERT = Path("ops/stage0/assert-live-host-state.sh")
QA_SERVICE = Path("backend/internal/observability/qa/service.go")
QA_CONFIG = Path("backend/internal/config/config.go")
GO_MAINTENANCE = Path("backend/cmd/server/qa_maintenance.go")
RAW_ARCHIVE_CFN = Path("deploy/aws/cloudformation/stage0-qa-raw-archive.yaml")
ARCHIVE_STATE = Path("backend/internal/observability/qa/archive/state.go")

MUST_BE_ABSENT = (
    Path("docs/qa-export-s3-and-auto-archive.md"),
    Path("docs/operator/qa-export-partner.md"),
    Path("ops/prod/qa-export-and-purge.sh"),  # script-ref-allow-missing
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
        "### 8.5 四类存储与备份边界",
        "### 18.1 现状 owner → 唯一目标 owner → 退役门禁",
    ),
    POLICY: (
        "schema_version: 1",
        "capture_enabled: false",
        "edge:",
    ),
    Path("ops/stage0/deploy_via_ssm.sh"): (
        "edge_qa_capture_cmds",
        "QA_CAPTURE_ENABLED=false",
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
    ),
    MAINTENANCE_SCRIPT: (
        "--qa-maintenance-once",
        "archive_start",
        "--install-units",
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
        "tokenkey-prod-qa-maintenance-v1",
        "deletion_authorized",
    ),
    Path("ops/stage0/sync-qa-maintenance-timer-via-ssm.sh"): (
        "tokenkey-qa-maintenance.timer",
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
        ("prod", "archive", "s3_retention_days"): 7,
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
        SSOT: (
            f"online_window_hours: {online_hours}",
            f'maintenance_schedule_utc: "{maintenance_schedule}"',
            f'cleanup_schedule_utc: "{cleanup_schedule}"',
            f"cleanup_randomized_delay_minutes: {cleanup_delay}",
            f"cleanup_batch_size: {cleanup_batch}",
            f"physical_cleanup_max_lag_minutes: {max_lag}",
            f"s3_retention_days: {raw_retention}",
        ),
        MAINTENANCE_SCRIPT: (f"OnCalendar=*-*-* *:{str(maintenance_schedule).split(':')[-1]}:00",),
        CLEANUP_SCRIPT: (
            f"RETENTION_HOURS={online_hours}",
            f"DELETE_BATCH_SIZE={cleanup_batch}",
            f"OnCalendar=*-*-* *:{str(cleanup_schedule).split(':')[-1]}:00",
            f"RandomizedDelaySec={cleanup_delay}min",
            "--resume-first",
            "flock -n 9",
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
    if re.search(r"OnCalendar=\*-\*-\* 04:15:00|Description=Daily QA", cleanup):
        failures.append(f"retired daily QA cleanup schedule remains in {CLEANUP_SCRIPT}")
    live_assert = (root / LIVE_HOST_ASSERT).read_text(encoding="utf-8") if (root / LIVE_HOST_ASSERT).is_file() else ""
    if "qa-stale-retention.env" in live_assert or "TOKENKEY_QA_STALE_RETENTION_DAYS" in live_assert:
        failures.append("live-host assert still reads the retired QA retention owner")
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
            LIVE_HOST_ASSERT,
            RAW_ARCHIVE_CFN,
            ARCHIVE_STATE,
        }
        for rel in fixture_files:
            path = root / rel
            path.parent.mkdir(parents=True, exist_ok=True)
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
    s3_retention_days: 7
edge:
  capture_enabled: false
  archive_enabled: false
  cleanup_enabled: false
  export_enabled: false
  s3_access: false
"""
        (root / POLICY).write_text(policy_fixture, encoding="utf-8")
        with (root / SSOT).open("a", encoding="utf-8") as handle:
            handle.write(
                'online_window_hours: 24\nmaintenance_schedule_utc: "*:15"\n'
                'cleanup_schedule_utc: "*:45"\ncleanup_randomized_delay_minutes: 15\n'
                'cleanup_batch_size: 5000\nphysical_cleanup_max_lag_minutes: 75\n'
                's3_retention_days: 7\n'
            )
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
