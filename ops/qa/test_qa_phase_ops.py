#!/usr/bin/env python3
"""Regression checks for QA Phase 1 closeout and Phase 2 baseline artifacts."""
from __future__ import annotations

import datetime as dt
import importlib.util
import json
import shlex
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def _load_module(name: str, relative: str):
    path = ROOT / relative
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {name}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _load_closeout_module():
    return _load_module("prod_qa_archive_closeout", "ops/qa/prod_qa_archive_closeout.py")


class TestQAPhaseOps(unittest.TestCase):
    def test_closeout_script_compiles(self) -> None:
        proc = subprocess.run(
            [sys.executable, "-m", "py_compile", str(ROOT / "ops/qa/edge_phase1_closeout.py")],
            capture_output=True,
            text=True,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_phase2_baseline_compiles(self) -> None:
        proc = subprocess.run(
            [sys.executable, "-m", "py_compile", str(ROOT / "ops/qa/prod_phase2_baseline.py")],
            capture_output=True,
            text=True,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_edge_host_units_sync_has_no_qa_wiring(self) -> None:
        body = (ROOT / "ops/stage0/sync-edge-host-units-via-ssm.sh").read_text(encoding="utf-8")
        for needle in (
            "tokenkey-qa-stale-cleanup",
            "qa-stale-retention.env",
            "TK_QA_STALE_RETENTION_DAYS",
        ):
            self.assertNotIn(needle, body, f"unexpected QA wiring: {needle}")

    def test_closeout_purges_qa_and_removes_timer(self) -> None:
        body = (ROOT / "ops/qa/edge_phase1_closeout.py").read_text(encoding="utf-8")
        self.assertIn("TRUNCATE qa_records", body)
        self.assertIn("tokenkey-qa-stale-cleanup.timer", body)
        self.assertIn("--apply", body)

    def test_raw_archive_cfn_has_lifecycle_prefixes(self) -> None:
        body = (ROOT / "deploy/aws/cloudformation/stage0-qa-raw-archive.yaml").read_text(
            encoding="utf-8"
        )
        self.assertIn("QaRawArchiveBucket", body)
        self.assertIn("raw/v1/", body)
        self.assertIn("raw/partial/", body)
        # S3 Bucket Key grants kms:GenerateDataKey with bucket-level encryption context.
        self.assertIn(
            "qa-raw-archive-${AWS::AccountId}'",
            body,
        )
        self.assertIn("kms:EncryptionContext:aws:s3:arn", body)

    def test_raw_archive_cfn_grants_kms_to_bucket_principals(self) -> None:
        body = (ROOT / "deploy/aws/cloudformation/stage0-qa-raw-archive.yaml").read_text(
            encoding="utf-8"
        )
        self.assertIn("AllowAppInstanceRoleUseViaS3", body)
        self.assertIn("kms:GenerateDataKey*", body)
        self.assertIn("kms:ViaService", body)
        self.assertIn("AllowOpsRecoveryRoleReadViaS3", body)

    def test_qa_stale_cleanup_has_read_only_plan_and_exact_cutoff_apply(self) -> None:
        body = (ROOT / "deploy/aws/stage0/tokenkey-qa-stale-cleanup.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("--plan", body)
        self.assertIn("--apply-first", body)
        self.assertIn("--resume-first", body)
        self.assertIn("flock -n 9", body)
        self.assertIn("tokenkey-prod-qa-retention-apply-v1:", body)
        self.assertIn("created_at < TIMESTAMPTZ", body)
        self.assertIn('TOKENKEY_ROOT="${TOKENKEY_ROOT:-/var/lib/tokenkey}"', body)
        self.assertIn("app/qa_blobs", body)
        self.assertIn("app/qa_dlq", body)
        self.assertIn("expected-active-image", body)
        self.assertIn("pg_try_advisory_xact_lock(1363234113)", body)
        self.assertIn("first-run cleanup is incomplete", body)
        self.assertNotIn('rm -f "${FIRST_PLAN_MARKER}"', body)
        self.assertNotIn("TOKENKEY_QA_STALE_RETENTION_DAYS", body)

    def test_qa_stale_cleanup_plan_is_read_only_behaviorally(self) -> None:
        script = ROOT / "deploy/aws/stage0/tokenkey-qa-stale-cleanup.sh"
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            fake_bin = root / "bin"
            fake_bin.mkdir()
            calls = root / "calls.log"
            (fake_bin / "docker").write_text(
                """#!/usr/bin/env bash
printf 'docker %s\n' "$*" >> "$CALLS"
if [ "$1" = ps ]; then echo tokenkey-postgres; exit 0; fi
if [ "$1" = inspect ]; then echo ghcr.io/youxuanxue/sub2api:1.8.140; exit 0; fi
if [ "$1" = exec ]; then
  echo '{"server_clock":"2026-08-07T12:00:00.000000Z","cutoff":"2026-08-06T12:00:00.000000Z","candidate_rows":42,"oldest_created_at":"2026-08-04T04:00:00.000000Z","newest_created_at":"2026-08-06T11:59:00.000000Z"}'
  exit 0
fi
exit 9
""",
                encoding="utf-8",
            )
            (fake_bin / "find").write_text(
                """#!/usr/bin/env bash
printf 'find %s\n' "$*" >> "$CALLS"
case "$1" in *qa_blobs) printf 'a\nb\n';; *qa_dlq) printf 'c\n';; esac
""",
                encoding="utf-8",
            )
            for name in ("docker", "find"):
                (fake_bin / name).chmod(0o755)
            (root / "active-color").write_text("blue\n", encoding="utf-8")
            (fake_bin / "install").write_text("#!/usr/bin/env bash\nexit 0\n", encoding="utf-8")
            (fake_bin / "install").chmod(0o755)
            proc = subprocess.run(
                ["bash", str(script), "--plan"],
                env={
                    "PATH": f"{fake_bin}:/opt/homebrew/bin:/usr/bin:/bin",
                    "CALLS": str(calls),
                    "TOKENKEY_ROOT": str(root),
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            payload = json.loads(proc.stdout)
            call_text = calls.read_text(encoding="utf-8")
        self.assertEqual(payload["candidate_rows"], 42)
        self.assertEqual(payload["active_image"], "ghcr.io/youxuanxue/sub2api:1.8.140")
        self.assertEqual(payload["candidate_blob_files"], 2)
        self.assertEqual(payload["candidate_dlq_files"], 1)
        self.assertFalse(payload["deletion_authorized"])
        self.assertNotIn("DELETE FROM", call_text)
        self.assertNotIn("-delete", call_text)

    def test_qa_stale_cleanup_delete_batches_are_bounded_and_locked(self) -> None:
        body = (ROOT / "deploy/aws/stage0/tokenkey-qa-stale-cleanup.sh").read_text(
            encoding="utf-8"
        )
        start = body.index("delete_rows_before() {")
        end = body.index("\n}\n\nbind_first_plan()", start) + 3
        function = body[start:end]
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            sequence = root / "sequence"
            sql_log = root / "sql.log"
            sequence.write_text("5000\n2\n0\n", encoding="utf-8")
            harness = f"""{function}
psql_value() {{
  printf '%s\\n' "$1" >> {shlex.quote(str(sql_log))}
  head -1 {shlex.quote(str(sequence))}
  tail -n +2 {shlex.quote(str(sequence))} > {shlex.quote(str(sequence))}.next
  mv {shlex.quote(str(sequence))}.next {shlex.quote(str(sequence))}
}}
fail() {{ printf '%s\\n' "$*" >&2; return 2; }}
DELETE_BATCH_SIZE=5000
delete_rows_before 2026-08-06T12:00:00.000000Z
"""
            proc = subprocess.run(
                ["bash"], input=harness, capture_output=True, text=True, check=False
            )
            sql = sql_log.read_text(encoding="utf-8")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(proc.stdout.strip(), "5002")
        self.assertEqual(sql.count("pg_try_advisory_xact_lock(1363234113)"), 3)
        self.assertEqual(sql.count("LIMIT 5000"), 3)

    def test_qa_stale_cleanup_service_is_bounded_and_disabled_by_default(self) -> None:
        bootstrap = (ROOT / "deploy/aws/stage0/stage0-ec2-bootstrap.sh").read_text(
            encoding="utf-8"
        )
        owner = (ROOT / "deploy/aws/stage0/tokenkey-qa-stale-cleanup.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn(
            "tokenkey-qa-stale-cleanup.sh --install-units /etc/systemd/system",
            bootstrap,
        )
        self.assertNotIn("QASVEOF", bootstrap)
        for needle in (
            "Nice=15",
            "IOSchedulingClass=idle",
            "CPUQuota=20%",
            "MemoryMax=1G",
            "TasksMax=128",
            "TimeoutStartSec=30min",
        ):
            self.assertIn(needle, owner)
        self.assertIn("systemctl disable --now tokenkey-qa-stale-cleanup.timer", bootstrap)
        self.assertNotIn("EnvironmentFile=-/etc/tokenkey/qa-stale-retention.env", owner)
        self.assertIn("OnCalendar=*-*-* *:45:00", owner)
        self.assertIn("RandomizedDelaySec=15min", owner)
        self.assertNotIn("OnCalendar=*-*-* 04:15:00", owner)

    def test_qa_stale_timer_enable_requires_first_apply_receipt_before_aws(self) -> None:
        script = ROOT / "ops/stage0/sync-qa-stale-cleanup-timer-via-ssm.sh"
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            fake_bin = root / "bin"
            fake_bin.mkdir()
            aws_log = root / "aws.log"
            (fake_bin / "aws").write_text(
                '#!/usr/bin/env bash\nprintf "%s\\n" "$*" >> "$AWS_LOG"\nexit 99\n',
                encoding="utf-8",
            )
            (fake_bin / "aws").chmod(0o755)
            proc = subprocess.run(
                ["bash", str(script), "i-0123456789abcdef0"],
                env={
                    "PATH": f"{fake_bin}:/opt/homebrew/bin:/usr/bin:/bin",
                    "AWS_LOG": str(aws_log),
                    "QA_STALE_TIMER_STATE": "enabled",
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(proc.returncode, 0)
            self.assertIn("QA_STALE_ACTIVATION_RECEIPT is required", proc.stderr)
            self.assertFalse(aws_log.exists())
        sync_body = script.read_text(encoding="utf-8")
        self.assertIn("qa-stale-first-plan.json", sync_body)
        self.assertIn('test -f \\"\\${marker}\\"', sync_body)
        self.assertIn('rm -f \\"\\${marker}\\"', sync_body)
        self.assertIn("systemctl is-enabled tokenkey-qa-stale-cleanup.timer", sync_body)
        self.assertNotIn(".activating", sync_body)

    def test_qa_stale_timer_enable_renders_marker_bound_shell(self) -> None:
        script = ROOT / "ops/stage0/sync-qa-stale-cleanup-timer-via-ssm.sh"
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            fake_bin = root / "bin"
            fake_bin.mkdir()
            (fake_bin / "aws").write_text("#!/usr/bin/env bash\nexit 99\n", encoding="utf-8")
            (fake_bin / "aws").chmod(0o755)
            now = dt.datetime.now(dt.timezone.utc)
            receipt = root / "receipt.json"
            receipt.write_text(
                json.dumps(
                    {
                        "mode": "prod_qa_age_retention_first_apply",
                        "instance_id": "i-0123456789abcdef0",
                        "deletion_authorized": True,
                        "cutoff": "2026-08-06T12:00:00.000000Z",
                        "applied_at": now.isoformat().replace("+00:00", "Z"),
                        "authorization_expires_at": (now + dt.timedelta(minutes=5)).isoformat().replace("+00:00", "Z"),
                        "planned_rows": 42,
                        "remaining_rows": 0,
                        "remaining_blob_files": 0,
                        "remaining_dlq_files": 0,
                        "marker_sha256": "a" * 64,
                    }
                ),
                encoding="utf-8",
            )
            output = root / "output"
            proc = subprocess.run(
                ["bash", str(script), "i-0123456789abcdef0"],
                env={
                    "PATH": f"{fake_bin}:/opt/homebrew/bin:/usr/bin:/bin",
                    "QA_STALE_TIMER_STATE": "enabled",
                    "QA_STALE_ACTIVATION_RECEIPT": str(receipt),
                    "STAGE0_SSM_OUTPUT_DIR": str(output),
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 99, (proc.stdout, proc.stderr))
            params = json.loads((output / "ssm-params.json").read_text(encoding="utf-8"))
        timer_command = params["commands"][5]
        self.assertIn("verify_marker", timer_command)
        self.assertIn("sha256sum", timer_command)
        self.assertIn("a" * 64, timer_command)
        for command in params["commands"]:
            parsed = subprocess.run(["bash", "-n"], input=command, text=True, capture_output=True)
            self.assertEqual(parsed.returncode, 0, parsed.stderr)

    def test_qa_maintenance_host_script_is_archive_only(self) -> None:
        body = (ROOT / "deploy/aws/stage0/tokenkey-qa-maintenance.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("--qa-maintenance-once", body)
        self.assertIn("tokenkey-prod-qa-maintenance-v1", body)
        self.assertIn("--install-units", body)
        self.assertIn("OnCalendar=*-*-* *:15:00", body)
        self.assertNotIn("DELETE FROM qa_records", body)

    def test_qa_maintenance_unit_has_resource_and_filesystem_limits(self) -> None:
        body = (ROOT / "deploy/aws/stage0/tokenkey-qa-maintenance.sh").read_text(
            encoding="utf-8"
        )
        for needle in (
            "Nice=15",
            "IOSchedulingClass=idle",
            "CPUQuota=20%",
            "MemoryMax=1G",
            "TasksMax=128",
            "PrivateTmp=true",
            "NoNewPrivileges=true",
            "ProtectSystem=strict",
            "ReadWritePaths=/var/lib/tokenkey",
            "--memory=1g",
            "--memory-swap=1g",
            "--cpus=0.20",
            "--pids-limit=128",
            '--network="container:${app_container}"',
            '--volumes-from="${app_container}:rw"',
            "{{.Image}}",
            "--read-only",
            "--cap-drop=ALL",
            "TMPDIR=/app/data/qa_archive_tmp",
            "install -d -m 0700 -o 1000 -g 1000 /var/lib/tokenkey/data/qa_archive_tmp",
        ):
            self.assertIn(needle, body)

    def test_qa_lifecycle_ssot_check_passes(self) -> None:
        proc = subprocess.run(
            [sys.executable, str(ROOT / "scripts/checks/qa-lifecycle-ssot.py")],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)

    def test_data_layer_archive_ssot_check_passes(self) -> None:
        proc = subprocess.run(
            [sys.executable, str(ROOT / "scripts/checks/data-layer-archive-ssot.py")],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)

    def test_qa_maintenance_sync_defaults_to_disabled_timer(self) -> None:
        body = (ROOT / "ops/stage0/sync-qa-maintenance-timer-via-ssm.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn('QA_MAINTENANCE_TIMER_STATE:-disabled', body)
        self.assertIn("systemctl disable --now tokenkey-qa-maintenance.timer", body)
        self.assertIn("systemctl is-enabled tokenkey-qa-maintenance.timer", body)
        self.assertIn("systemctl is-active tokenkey-qa-maintenance.timer", body)
        self.assertNotIn('\n      "sudo systemctl enable tokenkey-qa-maintenance.timer",', body)

    def test_qa_maintenance_sync_emits_disabled_timer_command_by_default(self) -> None:
        script = ROOT / "ops/stage0/sync-qa-maintenance-timer-via-ssm.sh"
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            fake_bin = root / "bin"
            output = root / "output"
            fake_bin.mkdir()
            output.mkdir()
            fake_aws = fake_bin / "aws"
            fake_aws.write_text(
                """#!/usr/bin/env bash
if [[ "$*" == *"ssm send-command"* ]]; then echo cmd-test; exit 0; fi
if [[ "$*" == *"--query Status"* ]]; then echo Success; exit 0; fi
if [[ "$*" == *"--query ResponseCode"* ]]; then echo 0; exit 0; fi
exit 0
""",
                encoding="utf-8",
            )
            fake_aws.chmod(0o755)
            env = {
                "PATH": f"{fake_bin}:/usr/bin:/bin",
                "STAGE0_SSM_OUTPUT_DIR": str(output),
                "STAGE0_SSM_TIMEOUT_SECONDS": "10",
            }
            proc = subprocess.run(
                ["bash", str(script), "i-0123456789abcdef0"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            payload = json.loads((output / "ssm-params.json").read_text(encoding="utf-8"))

        self.assertIn(
            "sudo systemctl disable --now tokenkey-qa-maintenance.timer",
            payload["commands"],
        )
        self.assertNotIn(
            "sudo systemctl enable --now tokenkey-qa-maintenance.timer",
            payload["commands"],
        )
        self.assertIn(
            'test "$(sudo systemctl is-active tokenkey-qa-maintenance.timer)" = "inactive"',
            payload["commands"],
        )

    def test_qa_maintenance_sync_explicit_enable_starts_and_verifies_timer(self) -> None:
        script = ROOT / "ops/stage0/sync-qa-maintenance-timer-via-ssm.sh"
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            fake_bin = root / "bin"
            output = root / "output"
            fake_bin.mkdir()
            output.mkdir()
            fake_aws = fake_bin / "aws"
            fake_aws.write_text(
                """#!/usr/bin/env bash
if [[ "$*" == *"ssm send-command"* ]]; then echo cmd-test; exit 0; fi
if [[ "$*" == *"--query Status"* ]]; then echo Success; exit 0; fi
if [[ "$*" == *"--query ResponseCode"* ]]; then echo 0; exit 0; fi
exit 0
""",
                encoding="utf-8",
            )
            fake_aws.chmod(0o755)
            env = {
                "PATH": f"{fake_bin}:/usr/bin:/bin",
                "QA_MAINTENANCE_TIMER_STATE": "enabled",
                "STAGE0_SSM_OUTPUT_DIR": str(output),
                "STAGE0_SSM_TIMEOUT_SECONDS": "10",
            }
            proc = subprocess.run(
                ["bash", str(script), "i-0123456789abcdef0"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            payload = json.loads((output / "ssm-params.json").read_text(encoding="utf-8"))

        self.assertIn(
            "sudo systemctl enable --now tokenkey-qa-maintenance.timer",
            payload["commands"],
        )
        self.assertIn(
            'test "$(sudo systemctl is-active tokenkey-qa-maintenance.timer)" = "active"',
            payload["commands"],
        )

    def test_raw_archive_cfn_keeps_app_and_recovery_permissions_separate(self) -> None:
        body = (ROOT / "deploy/aws/cloudformation/stage0-qa-raw-archive.yaml").read_text(
            encoding="utf-8"
        )
        app_policy = body[
            body.index("Sid: AllowAppInstanceRoleWriteRaw") :
            body.index("Sid: AllowOpsRecoveryRoleReadRaw")
        ]
        self.assertNotIn("s3:DeleteObject", app_policy)
        self.assertIn("Sid: AllowAppInstanceRoleListRawPrefix", app_policy)
        self.assertIn("s3:ListBucket", app_policy)
        self.assertIn("raw/v1/*", app_policy)
        self.assertIn("raw/partial/*", app_policy)
        recovery_policy = body[
            body.index("Sid: AllowOpsRecoveryRoleReadRaw") :
            body.index("QaRawArchiveAuditBucket:")
        ]
        self.assertIn("Sid: AllowOpsRecoveryRoleReadRaw", recovery_policy)
        self.assertIn("s3:GetObject", recovery_policy)
        self.assertIn("Sid: AllowOpsRecoveryRoleListRawPrefix", recovery_policy)
        self.assertIn("s3:ListBucket", recovery_policy)
        self.assertIn("'s3:prefix':", recovery_policy)
        self.assertIn("- raw/*", recovery_policy)
        self.assertNotIn("s3:PutObject", recovery_policy)
        self.assertNotIn("s3:DeleteObject", recovery_policy)

    def test_raw_archive_cfn_has_required_recovery_endpoint_and_data_events(self) -> None:
        body = (ROOT / "deploy/aws/cloudformation/stage0-qa-raw-archive.yaml").read_text(
            encoding="utf-8"
        )
        for needle in (
            "OpsRecoveryPrincipalArn:",
            "QaRawArchiveRecoveryRole:",
            "AWS::IAM::Role",
            "sts:AssumeRole",
            "QaRawArchiveAuditBucket:",
            "AllowCloudTrailWrite",
            "cloudtrail.amazonaws.com",
            "aws:SourceArn",
            "VpcId:",
            "RouteTableIds:",
            "AWS::EC2::VPCEndpoint",
            "Gateway",
            "AWS::CloudTrail::Trail",
            "DataResources:",
            "AWS::S3::Object",
            "EnableLogFileValidation: true",
        ):
            self.assertIn(needle, body)
        self.assertNotIn("OpsRecoveryRoleArn:", body)
        self.assertNotIn("AuditLogBucketName:", body)

    def test_raw_archive_deploy_refuses_blank_security_parameters_before_aws(self) -> None:
        script = ROOT / "ops/qa/deploy_qa_raw_archive_cfn.sh"
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_bin = Path(temp_dir) / "bin"
            fake_bin.mkdir()
            marker = Path(temp_dir) / "aws-called"
            fake_aws = fake_bin / "aws"
            fake_aws.write_text(f"#!/bin/sh\ntouch {marker}\nexit 0\n", encoding="utf-8")
            fake_aws.chmod(0o755)
            env = {
                "PATH": f"{fake_bin}:/usr/bin:/bin",
                "APP_INSTANCE_ROLE_ARN": "arn:aws:iam::123456789012:role/app",
                "OPS_RECOVERY_PRINCIPAL_ARN": "",
                "QA_RAW_ARCHIVE_VPC_ID": "vpc-1234",
                "QA_RAW_ARCHIVE_ROUTE_TABLE_IDS": "rtb-1234",
                "QA_RAW_ARCHIVE_CONFIRM": "yes",
            }
            proc = subprocess.run(["bash", str(script)], env=env, capture_output=True, text=True)
            self.assertNotEqual(proc.returncode, 0)
            self.assertIn("OPS_RECOVERY_PRINCIPAL_ARN", proc.stderr)
            self.assertFalse(marker.exists(), "aws must not run after blank security input")

    def test_raw_archive_deploy_prints_change_set_before_execute(self) -> None:
        body = (ROOT / "ops/qa/deploy_qa_raw_archive_cfn.sh").read_text(encoding="utf-8")
        self.assertIn("cloudformation create-change-set", body)
        self.assertIn("cloudformation describe-change-set", body)
        self.assertIn("cloudformation execute-change-set", body)
        self.assertIn("CAPABILITY_IAM", body)
        self.assertNotIn("cloudformation deploy", body)

    def test_raw_archive_deploy_rejects_replacement_even_when_confirmed(self) -> None:
        script = ROOT / "ops/qa/deploy_qa_raw_archive_cfn.sh"
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            fake_bin = root / "bin"
            fake_bin.mkdir()
            marker = root / "execute-called"
            fake_aws = fake_bin / "aws"
            fake_aws.write_text(
                """#!/usr/bin/env bash
if [[ "$*" == *"sts get-caller-identity"* ]]; then echo 123456789012; exit 0; fi
if [[ "$*" == *"cloudformation describe-stacks"* ]]; then echo '{}'; exit 0; fi
if [[ "$*" == *"cloudformation create-change-set"* ]]; then exit 0; fi
if [[ "$*" == *"cloudformation describe-change-set"* && "$*" == *"--query Status"* ]]; then
  echo CREATE_COMPLETE
  exit 0
fi
if [[ "$*" == *"cloudformation describe-change-set"* && "$*" == *"--output json"* ]]; then
  printf '%s\n' '{"Changes":[{"ResourceChange":{"Action":"Modify","LogicalResourceId":"RawBucket","ResourceType":"AWS::S3::Bucket","Replacement":"Conditional"}}]}'
  exit 0
fi
if [[ "$*" == *"cloudformation execute-change-set"* ]]; then touch "$EXECUTE_MARKER"; exit 0; fi
exit 1
""",
                encoding="utf-8",
            )
            fake_aws.chmod(0o755)
            proc = subprocess.run(
                ["bash", str(script)],
                env={
                    "PATH": f"{fake_bin}:/usr/bin:/bin",
                    "APP_INSTANCE_ROLE_ARN": "arn:aws:iam::123456789012:role/app",
                    "OPS_RECOVERY_PRINCIPAL_ARN": "arn:aws:iam::123456789012:user/operator",
                    "QA_RAW_ARCHIVE_VPC_ID": "vpc-1234",
                    "QA_RAW_ARCHIVE_ROUTE_TABLE_IDS": "rtb-1234",
                    "QA_RAW_ARCHIVE_CONFIRM": "yes",
                    "EXECUTE_MARKER": str(marker),
                },
                capture_output=True,
                text=True,
                check=False,
            )
            execute_called = marker.exists()

        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("unsafe CloudFormation change set", proc.stderr)
        self.assertIn("replacement=Conditional", proc.stderr)
        self.assertFalse(execute_called)

    def test_release_images_include_qa_archive_binary(self) -> None:
        for rel in ("Dockerfile", "deploy/Dockerfile", "backend/Dockerfile"):
            body = (ROOT / rel).read_text(encoding="utf-8")
            self.assertIn("./cmd/qa-archive", body, rel)
            self.assertIn("/app/qa-archive", body, rel)

    def test_published_release_image_check_verifies_both_platforms(self) -> None:
        script = ROOT / "scripts/checks/release-image-binaries.sh"
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            fake_bin = root / "bin"
            fake_bin.mkdir()
            log = root / "docker.log"
            fake_docker = fake_bin / "docker"
            fake_docker.write_text(
                """#!/usr/bin/env bash
printf '%s\n' "$*" >> "$DOCKER_LOG"
if [ "$1" = run ] && [ -n "${FAIL_PLATFORM:-}" ] && [[ "$*" == *"$FAIL_PLATFORM"* ]]; then
  exit 9
fi
exit 0
""",
                encoding="utf-8",
            )
            fake_docker.chmod(0o755)
            env = {
                "PATH": f"{fake_bin}:/usr/bin:/bin",
                "DOCKER_LOG": str(log),
            }
            proc = subprocess.run(
                [
                    "bash",
                    str(script),
                    "ghcr.io/youxuanxue/sub2api:1.8.139",
                    "linux/amd64,linux/arm64",
                ],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            calls = log.read_text(encoding="utf-8")

        for platform in ("linux/amd64", "linux/arm64"):
            self.assertIn(f"pull --platform {platform}", calls)
            self.assertIn(f"run --rm --pull=never --platform {platform}", calls)
        self.assertIn("test -x /app/sub2api", calls)
        self.assertIn("test -x /app/qa-archive", calls)
        self.assertIn("/app/sub2api -version", calls)
        self.assertIn("/app/qa-archive", calls)
        self.assertIn('test "$qa_rc" -eq 2', calls)
        self.assertIn(r'*\"error\":\"command\ required:*', calls)

    def test_published_release_image_check_fails_on_missing_platform_binary(self) -> None:
        script = ROOT / "scripts/checks/release-image-binaries.sh"
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            fake_bin = root / "bin"
            fake_bin.mkdir()
            log = root / "docker.log"
            fake_docker = fake_bin / "docker"
            fake_docker.write_text(
                """#!/usr/bin/env bash
printf '%s\n' "$*" >> "$DOCKER_LOG"
if [ "$1" = run ] && [[ "$*" == *"linux/arm64"* ]]; then exit 9; fi
exit 0
""",
                encoding="utf-8",
            )
            fake_docker.chmod(0o755)
            proc = subprocess.run(
                [
                    "bash",
                    str(script),
                    "ghcr.io/youxuanxue/sub2api:1.8.139",
                    "linux/amd64,linux/arm64",
                ],
                env={
                    "PATH": f"{fake_bin}:/usr/bin:/bin",
                    "DOCKER_LOG": str(log),
                },
                capture_output=True,
                text=True,
                check=False,
            )

        self.assertEqual(proc.returncode, 9)

    def test_historical_backfill_entrypoints_are_absent(self) -> None:
        self.assertFalse((ROOT / "ops/qa/prod_qa_archive_backfill.py").exists())  # script-ref-allow-missing
        maintenance = (ROOT / "ops/qa/prod_qa_maintenance.py").read_text(encoding="utf-8")
        go_owner = (ROOT / "backend/cmd/server/qa_maintenance.go").read_text(encoding="utf-8")
        self.assertNotIn("backfill_once", maintenance)
        self.assertNotIn("--backfill-once", maintenance)
        self.assertNotIn("backfillOnce", go_owner)
        self.assertNotIn("qa-maintenance-backfill-once", go_owner)

    def test_historical_closeout_has_fixed_targets_and_safety_guards(self) -> None:
        module = _load_module(
            "prod_qa_historical_closeout", "ops/qa/prod_qa_historical_closeout.py"
        )
        plan = module._remote_script(apply=False)
        apply = module._remote_script(apply=True)
        for script in (plan, apply):
            self.assertIn("2026-08-07 01:00:00+00", script)
            self.assertIn("2026-08-04 04:00:00+00", script)
            self.assertIn("commit_mismatch", script)
            self.assertIn("missing_evidence", script)
            self.assertIn("tokenkey-qa-maintenance.timer", script)
            self.assertIn("tokenkey-qa-stale-cleanup.timer", script)
            self.assertIn("ops:cleanup:leader", script)
            self.assertNotIn("$WINDOW", script)
        self.assertNotIn("UPDATE qa_archive_shards", plan)
        self.assertIn("UPDATE qa_archive_shards", apply)
        self.assertIn("pg_try_advisory_xact_lock", apply)
        self.assertIn("cleanup_eligible=false", apply)
        self.assertIn("deletion_authorized", apply)

    def test_historical_closeout_rejects_wrong_confirmation_before_aws(self) -> None:
        module = _load_module(
            "prod_qa_historical_closeout", "ops/qa/prod_qa_historical_closeout.py"
        )
        with self.assertRaisesRegex(module.HistoricalCloseoutError, "confirmation"):
            module.run("apply", "wrong")

    def test_qa_archive_closeout_controller_is_fail_closed(self) -> None:
        module = _load_closeout_module()
        window = module._parse_window("2026-08-07T01:00:00Z")
        self.assertEqual(
            module._window_token(module.REPAIR_CONFIRMATION_PREFIX, window),
            "tokenkey-prod-qa-archive-repair-v1:2026-08-07T01:00:00Z",
        )
        proof = module._safety_proof(
            window, dt.datetime(2026, 8, 7, 2, 0, tzinfo=dt.timezone.utc)
        )
        proof_payload = json.loads(proof)
        self.assertEqual(proof_payload["schema_version"], module.SAFETY_SCHEMA)
        self.assertTrue(proof_payload["cleanup_runtime_disabled"])
        guard = "\n".join(module._timer_guard_shell())
        remote = module._remote_command(
            "repair-apply",
            window,
            "",
            module._window_token(module.REPAIR_CONFIRMATION_PREFIX, window),
        )
        self.assertNotIn("--safety-proof", remote)
        for sidecar_needle in (
            "{{.Image}}",
            "docker run --rm",
            "--user=1000:1000",
            "--read-only",
            "--cap-drop=ALL",
            "--memory=1g",
            "--memory-swap=1g",
            "--cpus=0.20",
            "--pids-limit=128",
            "TMPDIR=/app/data/qa_archive_tmp",
            "chmod 0444",
            "$proof_file:/run/tokenkey-qa-archive-safety-proof.json:ro",
        ):
            self.assertIn(sidecar_needle, remote)
        for needle in (
            "tokenkey-qa-maintenance.timer",
            "tokenkey-qa-stale-cleanup.timer",
            "disabled:inactive",
            "cleanup_enabled=false",
            "ops:cleanup:leader",
        ):
            self.assertIn(needle, guard)

    def test_qa_archive_closeout_rejects_incomplete_receipt(self) -> None:
        module = _load_closeout_module()
        window = module._parse_window("2026-08-07T01:00:00Z")
        receipt = {
            "ok": True,
            "command": "repair-apply",
            "window_start": "2026-08-07T01:00:00Z",
            "cleanup_eligible": False,
            "deletion_authorized": False,
            "cleanup_hold_active": True,
            "maintenance_timer_disabled": True,
            "maintenance_timer_inactive": True,
            "stale_cleanup_timer_disabled": True,
            "stale_cleanup_timer_inactive": True,
            "cleanup_runtime_disabled": True,
        }
        with self.assertRaises(module.QAArchiveCloseoutError):
            module._validate_receipt(json.dumps(receipt), "repair-apply", window)

    def test_qa_archive_closeout_restore_path_cannot_escape_root(self) -> None:
        module = _load_closeout_module()
        with self.assertRaises(module.QAArchiveCloseoutError):
            module._validate_restore_output("/app/data/qa_archive_restore/../escaped")

    def test_qa_maintenance_ops_scripts_compile(self) -> None:
        for rel in (
            "ops/qa/prod_qa_maintenance.py",
            "ops/qa/prod_apply_tk069_migration.py",
            "ops/qa/prod_qa_archive_closeout.py",
        ):
            proc = subprocess.run(
                [sys.executable, "-m", "py_compile", str(ROOT / rel)],
                capture_output=True,
                text=True,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)

if __name__ == "__main__":
    raise SystemExit(unittest.main())
