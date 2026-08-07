#!/usr/bin/env python3
"""Guard the single QA lifecycle owner and retired conflicting surfaces."""
from __future__ import annotations

import argparse
import tempfile
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[2]
SSOT = Path("docs/approved/design-prod-qa-24h-s3-lifecycle.md")
POLICY = Path("ops/qa/policy.yaml")
ROLLOUT = Path("ops/qa/deploy_rollout.yaml")
QA_README = Path("ops/qa/README.md")
DEPLOY_SSM = Path("ops/stage0/deploy_via_ssm.sh")
DEPLOY_BG = Path("ops/stage0/deploy_via_ssm_bluegreen.sh")

MUST_BE_ABSENT = (
    Path("docs/qa-export-s3-and-auto-archive.md"),
    Path("docs/operator/qa-export-partner.md"),
    Path("ops/prod/qa-export-and-purge.sh"),  # script-ref-allow-missing
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
    Path("backend/internal/config/config.go"): (
        "AutoExportEnabled",
        'mapstructure:"auto_export_enabled"',
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
        "### 8.5 四类存储与备份边界",
        "### 18.1 现状 owner → 唯一目标 owner → 退役门禁",
    ),
    POLICY: (
        "schema_version: 1",
        "capture_enabled: true",
        "online_window_hours: 24",
        "archive:",
        "enabled: true",
        "s3_retention_days: 7",
        "capture_enabled: false",
        "edge:",
    ),
    ROLLOUT: (
        "schema_version: 1",
        "deploy_inject_default: false",
        "policy_target: prod.archive.enabled",
        "design-qa-phase2-archive-closeout.md",
    ),
    QA_README: (
        "policy.yaml",
        "deploy_rollout.yaml",
        "qa-lifecycle-ssot.py",
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
    ),
    Path("deploy/aws/stage0/tokenkey-qa-maintenance.sh"): (
        "--qa-maintenance-once",
        "archive_start",
        "--install-units",
    ),
    Path("backend/cmd/server/qa_maintenance.go"): (
        "qa_maintenance_archive_only",
        "qa_maintenance_archive",
        "deletion_authorized",
        "archive.NewReconciler",
        "qa-maintenance-backfill-once",
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


def _load_yaml(path: Path) -> Any:
    try:
        import yaml
    except ImportError as exc:
        raise RuntimeError(f"PyYAML required to validate {path}") from exc
    payload = yaml.safe_load(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"{path} must parse to a mapping")
    return payload


def _require_mapping(value: Any, label: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ValueError(f"{label} must be a mapping")
    return value


def validate_policy(root: Path) -> list[str]:
    failures: list[str] = []
    path = root / POLICY
    if not path.is_file():
        return [f"required QA SSOT file missing: {POLICY}"]
    try:
        data = _load_yaml(path)
    except (OSError, ValueError, RuntimeError) as exc:
        return [f"policy.yaml invalid: {exc}"]

    if data.get("schema_version") != 1:
        failures.append("policy.yaml schema_version must be 1")

    prod = _require_mapping(data.get("prod"), "policy prod")
    edge = _require_mapping(data.get("edge"), "policy edge")
    archive = _require_mapping(prod.get("archive"), "policy prod.archive")

    checks = (
        (prod.get("capture_enabled") is True, "prod.capture_enabled must be true"),
        (prod.get("online_window_hours") == 24, "prod.online_window_hours must be 24"),
        (archive.get("enabled") is True, "prod.archive.enabled must be true"),
        (archive.get("s3_retention_days") == 7, "prod.archive.s3_retention_days must be 7"),
        (edge.get("capture_enabled") is False, "edge.capture_enabled must be false"),
        (edge.get("archive_enabled") is False, "edge.archive_enabled must be false"),
        (edge.get("cleanup_enabled") is False, "edge.cleanup_enabled must be false"),
    )
    for ok, message in checks:
        if not ok:
            failures.append(message)
    return failures


def validate_rollout(root: Path) -> list[str]:
    failures: list[str] = []
    path = root / ROLLOUT
    if not path.is_file():
        return [f"required QA rollout file missing: {ROLLOUT}"]
    try:
        data = _load_yaml(path)
    except (OSError, ValueError, RuntimeError) as exc:
        return [f"deploy_rollout.yaml invalid: {exc}"]

    prod = _require_mapping(data.get("prod"), "rollout prod")
    edge = _require_mapping(data.get("edge"), "rollout edge")
    prod_archive = _require_mapping(prod.get("QA_ARCHIVE_ENABLED"), "rollout prod.QA_ARCHIVE_ENABLED")
    edge_capture = _require_mapping(edge.get("QA_CAPTURE_ENABLED"), "rollout edge.QA_CAPTURE_ENABLED")

    if prod_archive.get("deploy_inject_default") is not False:
        failures.append("rollout prod.QA_ARCHIVE_ENABLED.deploy_inject_default must be false")
    if prod_archive.get("policy_target") != "prod.archive.enabled":
        failures.append("rollout prod.QA_ARCHIVE_ENABLED.policy_target drift")
    if edge_capture.get("deploy_inject_default") is not False:
        failures.append("rollout edge.QA_CAPTURE_ENABLED.deploy_inject_default must be false")
    return failures


def validate_deploy_defaults(root: Path) -> list[str]:
    failures: list[str] = []
    for rel in (DEPLOY_SSM, DEPLOY_BG):
        path = root / rel
        if not path.is_file():
            failures.append(f"deploy script missing: {rel}")
            continue
        body = path.read_text(encoding="utf-8")
        if "${QA_ARCHIVE_ENABLED:-false}" not in body:
            failures.append(
                f"{rel} must default QA_ARCHIVE_ENABLED to false (ops/qa/deploy_rollout.yaml)"
            )
        if "ops/qa/deploy_rollout.yaml" not in body:
            failures.append(f"{rel} must reference ops/qa/deploy_rollout.yaml rollout SSOT")
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

    failures.extend(validate_policy(root))
    failures.extend(validate_rollout(root))
    failures.extend(validate_deploy_defaults(root))
    return failures


def self_test() -> int:
    with tempfile.TemporaryDirectory() as temp_dir:
        root = Path(temp_dir)
        for rel in {*FORBIDDEN_BY_FILE, *REQUIRED_BY_FILE, POLICY, ROLLOUT, QA_README, DEPLOY_SSM, DEPLOY_BG}:
            src = ROOT / rel
            path = root / rel
            path.parent.mkdir(parents=True, exist_ok=True)
            if src.is_file():
                path.write_text(src.read_text(encoding="utf-8"), encoding="utf-8")
            else:
                required = REQUIRED_BY_FILE.get(rel, ())
                path.write_text("\n".join(required) + "\n", encoding="utf-8")

        failures = scan(root)
        if failures:
            print("self-test valid fixture failed:")
            for failure in failures:
                print(f"  - {failure}")
            return 1

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
