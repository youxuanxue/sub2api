#!/usr/bin/env python3
"""Regression checks for QA Phase 1 closeout and Phase 2 baseline artifacts."""
from __future__ import annotations

import datetime as dt
import base64
import gzip
import importlib.util
import json
import os
import re
import shlex
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

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
    def test_prod_rollout_separates_repository_readiness_from_live_activation(self) -> None:
        import yaml

        rollout = yaml.safe_load(
            (ROOT / "ops/qa/deploy_rollout.yaml").read_text(encoding="utf-8")
        )["prod"]
        self.assertEqual(rollout["tokenkey_qa_maintenance_timer"]["repository_closeout_state"], "single_owner_ready")
        self.assertEqual(rollout["tokenkey_qa_boundary"]["repository_closeout_state"], "pre_activation_fixed_age_transition")
        for owner in ("tokenkey_qa_maintenance_timer", "tokenkey_qa_boundary"):
            self.assertEqual(rollout[owner]["observed_live_state"], "single_owner_not_activated")
        self.assertEqual(
            rollout["qa_records"]["partition_owner_observed"],
            "qa_lifecycle_boundary",
        )
        self.assertEqual(
            rollout["raw_archive_recovery"]["observed_iam_state"],
            "applied",
        )
        user_export = rollout["user_export"]
        self.assertEqual(user_export["phase3_worker_repository_state"], "s3_bundle_ready")
        self.assertEqual(user_export["phase3_worker_observed_state"], "transitional_in_prod")
        self.assertEqual(user_export["job_registry"], "immutable_s3_spec")
        self.assertEqual(user_export["database_job_registry"], "retired")
        self.assertEqual(user_export["bundle_runtime_contract"], "phase3_v1")
        self.assertEqual(user_export["bundle_worker_desired_count"], 1)

    def test_maintenance_is_the_only_target_lifecycle_owner(self) -> None:
        import yaml

        policy = yaml.safe_load((ROOT / "ops/qa/policy.yaml").read_text(encoding="utf-8"))
        lifecycle = policy["prod"]["lifecycle"]
        self.assertEqual(lifecycle["owner"], "tokenkey-qa-maintenance")
        self.assertEqual(lifecycle["future_horizon_hours"], 72)
        self.assertTrue(lifecycle["drop_requires_raw_commit"])
        self.assertTrue(lifecycle["drop_requires_restore_verified"])
        self.assertTrue(lifecycle["drop_requires_capture_seal"])
        self.assertNotIn("cleanup", policy["prod"])
        self.assertEqual(policy["prod"]["user_qa"], {
            "entitlement": "users.traj_export_enabled",
            "source": "s3_qa_bundle",
            "compute": "ecs_fargate",
            "download": "direct_s3",
            "job_registry": "immutable_s3_spec",
            "database_job_registry": False,
            "prod_fallback": "forbidden",
        })

        rollout = yaml.safe_load(
            (ROOT / "ops/qa/deploy_rollout.yaml").read_text(encoding="utf-8")
        )["prod"]
        self.assertEqual(
            rollout["tokenkey_qa_boundary"]["policy_target_state"],
            "disabled_after_single_owner_activate",
        )
        self.assertEqual(
            rollout["tokenkey_qa_boundary"]["host_runner"],
            "/usr/local/bin/tokenkey-qa-boundary.sh",
        )
        self.assertEqual(
            rollout["tokenkey_qa_boundary"]["pre_activation_role"],
            "provision_and_fixed_age_whole_partition_cleanup",
        )
        self.assertEqual(rollout["tokenkey_qa_boundary"]["pre_activation_retention_hours"], 24)
        self.assertEqual(
            rollout["tokenkey_qa_boundary"]["pre_activation_terminal_gap_policy"],
            "persist_before_drop",
        )
        boundary = (ROOT / "deploy/aws/stage0/tokenkey-qa-boundary.sh").read_text(
            encoding="utf-8"
        )
        archive = (ROOT / "deploy/aws/stage0/tokenkey-qa-maintenance.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("OnCalendar=*-*-* *:00:00", boundary)
        self.assertIn("--qa-boundary-once", boundary)
        self.assertNotIn("--qa-cutover-provision-only", boundary)
        self.assertNotIn("--qa-cutover-finalize", boundary)
        self.assertNotIn("qa_exports_tmp", boundary)
        self.assertNotIn("RandomizedDelaySec", boundary)
        self.assertIn("OnCalendar=*-*-* *:15:00", archive)
        self.assertNotIn("RandomizedDelaySec", archive)

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

    def test_qa_maintenance_host_script_runs_archive_only(self) -> None:
        body = (ROOT / "deploy/aws/stage0/tokenkey-qa-maintenance.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("--qa-maintenance-once", body)
        self.assertIn("tokenkey-prod-qa-maintenance-v1", body)
        self.assertIn("archive_start", body)
        self.assertIn("--install-units", body)
        self.assertIn("OnCalendar=*-*-* *:15:00", body)
        self.assertNotIn("DELETE FROM qa_records", body)
        self.assertNotIn("--partition-maintenance-once", body)
        self.assertNotIn("run_partition_maintenance", body)
        self.assertNotIn("tokenkey-prod-partition-maintenance-v1", body)

    def test_verify_edge_qa_boundary_accepts_exact_dual_bucket_denies(self) -> None:
        iam_contract = _load_module(
            "verify_raw_archive_iam_contract", "ops/qa/verify_raw_archive_iam_contract.py"
        )
        account_id = "123456789012"
        edge_role = (
            f"arn:aws:iam::{account_id}:role/tokenkey-lightsail-ssm-hybrid"
        )

        def statements(bucket: str) -> list[dict]:
            return [
                {
                    "Sid": "DenyLightsailEdgeRole",
                    "Effect": "Deny",
                    "Principal": "*",
                    "Action": "s3:*",
                    "Resource": [
                        f"arn:aws:s3:::{bucket}",
                        f"arn:aws:s3:::{bucket}/*",
                    ],
                    "Condition": {
                        "ArnEquals": {"aws:PrincipalArn": edge_role}
                    },
                },
                {
                    "Sid": "AllowProdApp",
                    "Effect": "Allow",
                    "Principal": {
                        "AWS": f"arn:aws:iam::{account_id}:role/prod-app"
                    },
                    "Action": "s3:GetObject",
                    "Resource": f"arn:aws:s3:::{bucket}/*",
                },
            ]

        buckets = {
            "tokenkey-prod-qa-raw-archive-123456789012": statements(
                "tokenkey-prod-qa-raw-archive-123456789012"
            ),
            "tokenkey-prod-qa-exports-123456789012": statements(
                "tokenkey-prod-qa-exports-123456789012"
            ),
        }
        verdict = iam_contract.evaluate_edge_qa_boundary(
            account_id=account_id, buckets=buckets
        )
        self.assertTrue(verdict["ok"], verdict)
        self.assertEqual(verdict["status"], "applied")
        self.assertEqual(verdict["edge_role_arn"], edge_role)

    def test_verify_edge_qa_boundary_rejects_missing_deny_and_edge_allow(self) -> None:
        iam_contract = _load_module(
            "verify_raw_archive_iam_contract", "ops/qa/verify_raw_archive_iam_contract.py"
        )
        account_id = "123456789012"
        edge_role = (
            f"arn:aws:iam::{account_id}:role/tokenkey-lightsail-ssm-hybrid"
        )
        bucket = "tokenkey-prod-qa-exports-123456789012"
        verdict = iam_contract.evaluate_edge_qa_boundary(
            account_id=account_id,
            buckets={
                bucket: [
                    {
                        "Sid": "AllowEdgeByMistake",
                        "Effect": "Allow",
                        "Principal": "*",
                        "Action": "s3:GetObject",
                        "Resource": f"arn:aws:s3:::{bucket}/*",
                        "Condition": {
                            "ArnEquals": {"aws:PrincipalArn": edge_role}
                        },
                    }
                ]
            },
        )
        self.assertFalse(verdict["ok"])
        self.assertIn(f"{bucket}:edge_deny_count:0", verdict["failures"])
        self.assertIn(
            f"{bucket}:edge_role_allowed:AllowEdgeByMistake", verdict["failures"]
        )

    def test_verify_edge_qa_boundary_rejects_wrong_role_or_resource_scope(self) -> None:
        iam_contract = _load_module(
            "verify_raw_archive_iam_contract", "ops/qa/verify_raw_archive_iam_contract.py"
        )
        bucket = "tokenkey-prod-qa-raw-archive-123456789012"
        verdict = iam_contract.evaluate_edge_qa_boundary(
            account_id="123456789012",
            buckets={
                bucket: [
                    {
                        "Sid": "DenyLightsailEdgeRole",
                        "Effect": "Deny",
                        "Principal": "*",
                        "Action": "s3:*",
                        "Resource": [
                            f"arn:aws:s3:::{bucket}",
                            f"arn:aws:s3:::{bucket}/*",
                            "arn:aws:s3:::tokenkey-prod-pgdump-123456789012/*",
                        ],
                        "Condition": {
                            "ArnEquals": {
                                "aws:PrincipalArn": "arn:aws:iam::123456789012:role/wrong"
                            }
                        },
                    }
                ]
            },
        )
        self.assertFalse(verdict["ok"])
        self.assertIn(f"{bucket}:edge_deny_resources", verdict["failures"])
        self.assertIn(f"{bucket}:edge_deny_condition", verdict["failures"])

    def test_verify_live_edge_qa_boundary_reads_both_stack_outputs_and_policies(self) -> None:
        iam_contract = _load_module(
            "verify_raw_archive_iam_contract", "ops/qa/verify_raw_archive_iam_contract.py"
        )
        output_calls: list[tuple[str, str]] = []
        policy_calls: list[str] = []

        def fake_stack_output(stack: str, key: str) -> str:
            output_calls.append((stack, key))
            return {
                (iam_contract.RAW_ARCHIVE_STACK, "QaRawArchiveBucketName"): "raw-bucket",
                (iam_contract.BACKUPS_STACK, "QaExportsBucketName"): "exports-bucket",
            }[(stack, key)]

        def fake_policy(bucket: str) -> list[dict]:
            policy_calls.append(bucket)
            edge_role = (
                "arn:aws:iam::123456789012:role/tokenkey-lightsail-ssm-hybrid"
            )
            return [
                {
                    "Sid": "DenyLightsailEdgeRole",
                    "Effect": "Deny",
                    "Principal": "*",
                    "Action": "s3:*",
                    "Resource": [
                        f"arn:aws:s3:::{bucket}",
                        f"arn:aws:s3:::{bucket}/*",
                    ],
                    "Condition": {
                        "ArnEquals": {"aws:PrincipalArn": edge_role}
                    },
                }
            ]

        with mock.patch.object(iam_contract, "_stack_output", fake_stack_output), mock.patch.object(
            iam_contract, "_live_bucket_policy", fake_policy
        ):
            verdict = iam_contract.verify_live_edge_qa_boundary("123456789012")

        self.assertTrue(verdict["ok"], verdict)
        self.assertEqual(
            output_calls,
            [
                (iam_contract.RAW_ARCHIVE_STACK, "QaRawArchiveBucketName"),
                (iam_contract.BACKUPS_STACK, "QaExportsBucketName"),
            ],
        )
        self.assertEqual(policy_calls, ["raw-bucket", "exports-bucket"])

    def test_verify_raw_archive_iam_contract_flags_missing_s3_gateway_route(self) -> None:
        iam_contract = _load_module(
            "verify_raw_archive_iam_contract", "ops/qa/verify_raw_archive_iam_contract.py"
        )

        def fake_aws_json(args: list[str]) -> dict:
            if "describe-vpc-endpoints" in args:
                return {"VpcEndpoints": []}
            raise AssertionError(f"unexpected aws call: {args}")

        original = iam_contract._aws_json
        iam_contract._aws_json = fake_aws_json
        try:
            failures = iam_contract._verify_s3_gateway_endpoint(
                vpc_id="vpc-abc",
                route_table_ids=["rtb-public"],
                endpoint_id="vpce-qa",
            )
        finally:
            iam_contract._aws_json = original
        self.assertIn("s3_endpoint_missing:vpce-qa", failures)

    def test_verify_raw_archive_iam_contract_detects_detached_route_table(self) -> None:
        iam_contract = _load_module(
            "verify_raw_archive_iam_contract", "ops/qa/verify_raw_archive_iam_contract.py"
        )

        def fake_aws_json(args: list[str]) -> dict:
            if "describe-vpc-endpoints" in args:
                return {
                    "VpcEndpoints": [
                        {
                            "VpcId": "vpc-abc",
                            "State": "available",
                            "VpcEndpointType": "Gateway",
                            "ServiceName": "com.amazonaws.us-east-1.s3",
                            "PrefixListId": "pl-s3",
                            "RouteTableIds": ["rtb-other"],
                        }
                    ]
                }
            if "describe-route-tables" in args:
                return {
                    "RouteTables": [
                        {"Routes": [{"DestinationCidrBlock": "0.0.0.0/0", "GatewayId": "igw-1"}]}
                    ]
                }
            raise AssertionError(f"unexpected aws call: {args}")

        original = iam_contract._aws_json
        iam_contract._aws_json = fake_aws_json
        try:
            failures = iam_contract._verify_s3_gateway_endpoint(
                vpc_id="vpc-abc",
                route_table_ids=["rtb-public"],
                endpoint_id="vpce-qa",
            )
        finally:
            iam_contract._aws_json = original
        self.assertIn("s3_endpoint_route_table_not_attached:rtb-public", failures)
        self.assertIn("route_table_missing_s3_gateway_route:rtb-public", failures)

    def test_verify_raw_archive_iam_contract_requires_gateway_and_prefix_on_same_route(self) -> None:
        iam_contract = _load_module(
            "verify_raw_archive_iam_contract", "ops/qa/verify_raw_archive_iam_contract.py"
        )

        def fake_aws_json(args: list[str]) -> dict:
            if "describe-vpc-endpoints" in args:
                return {
                    "VpcEndpoints": [
                        {
                            "VpcId": "vpc-abc",
                            "State": "available",
                            "VpcEndpointType": "Gateway",
                            "ServiceName": "com.amazonaws.us-east-1.s3",
                            "PrefixListId": "pl-s3",
                            "RouteTableIds": ["rtb-public"],
                        }
                    ]
                }
            if "describe-route-tables" in args:
                return {
                    "RouteTables": [
                        {
                            "Routes": [
                                {
                                    "GatewayId": "vpce-qa",
                                    "DestinationPrefixListId": "pl-s3",
                                }
                            ]
                        }
                    ]
                }
            raise AssertionError(f"unexpected aws call: {args}")

        original = iam_contract._aws_json
        iam_contract._aws_json = fake_aws_json
        try:
            failures = iam_contract._verify_s3_gateway_endpoint(
                vpc_id="vpc-abc",
                route_table_ids=["rtb-public"],
                endpoint_id="vpce-qa",
            )
        finally:
            iam_contract._aws_json = original
        self.assertEqual(failures, [])

    def test_verify_raw_archive_iam_contract_accepts_live_gateway_endpoint_shape(self) -> None:
        iam_contract = _load_module(
            "verify_raw_archive_iam_contract", "ops/qa/verify_raw_archive_iam_contract.py"
        )

        def fake_aws_json(args: list[str]) -> dict:
            if "describe-vpc-endpoints" in args:
                return {
                    "VpcEndpoints": [
                        {
                            "VpcId": "vpc-abc",
                            "State": "available",
                            "VpcEndpointType": "Gateway",
                            "ServiceName": "com.amazonaws.us-east-1.s3",
                            "PrefixListId": None,
                            "RouteTableIds": ["rtb-public"],
                        }
                    ]
                }
            if "describe-route-tables" in args:
                return {
                    "RouteTables": [
                        {
                            "Routes": [
                                {
                                    "GatewayId": "vpce-qa",
                                    "DestinationPrefixListId": "pl-s3",
                                }
                            ]
                        }
                    ]
                }
            raise AssertionError(f"unexpected aws call: {args}")

        original = iam_contract._aws_json
        iam_contract._aws_json = fake_aws_json
        try:
            failures = iam_contract._verify_s3_gateway_endpoint(
                vpc_id="vpc-abc",
                route_table_ids=["rtb-public"],
                endpoint_id="vpce-qa",
            )
        finally:
            iam_contract._aws_json = original
        self.assertEqual(failures, [])

    def test_verify_raw_archive_iam_contract_rejects_non_gateway_endpoint(self) -> None:
        iam_contract = _load_module(
            "verify_raw_archive_iam_contract", "ops/qa/verify_raw_archive_iam_contract.py"
        )

        def fake_aws_json(args: list[str]) -> dict:
            if "describe-vpc-endpoints" in args:
                return {
                    "VpcEndpoints": [
                        {
                            "VpcId": "vpc-abc",
                            "State": "available",
                            "VpcEndpointType": "Interface",
                            "ServiceName": "com.amazonaws.us-east-1.s3",
                            "RouteTableIds": ["rtb-public"],
                        }
                    ]
                }
            if "describe-route-tables" in args:
                return {"RouteTables": [{"Routes": []}]}
            raise AssertionError(f"unexpected aws call: {args}")

        original = iam_contract._aws_json
        iam_contract._aws_json = fake_aws_json
        try:
            failures = iam_contract._verify_s3_gateway_endpoint(
                vpc_id="vpc-abc",
                route_table_ids=["rtb-public"],
                endpoint_id="vpce-qa",
            )
        finally:
            iam_contract._aws_json = original
        self.assertIn("s3_endpoint_not_gateway", failures)

    def test_verify_raw_archive_iam_contract_rejects_unrelated_prefix_list_route(self) -> None:
        iam_contract = _load_module(
            "verify_raw_archive_iam_contract", "ops/qa/verify_raw_archive_iam_contract.py"
        )

        def fake_aws_json(args: list[str]) -> dict:
            if "describe-vpc-endpoints" in args:
                return {
                    "VpcEndpoints": [
                        {
                            "VpcId": "vpc-abc",
                            "State": "available",
                            "VpcEndpointType": "Gateway",
                            "ServiceName": "com.amazonaws.us-east-1.s3",
                            "PrefixListId": "pl-s3",
                            "RouteTableIds": ["rtb-public"],
                        }
                    ]
                }
            if "describe-route-tables" in args:
                return {
                    "RouteTables": [
                        {
                            "Routes": [
                                {
                                    "DestinationPrefixListId": "pl-unrelated",
                                    "GatewayId": "vpce-other",
                                }
                            ]
                        }
                    ]
                }
            raise AssertionError(f"unexpected aws call: {args}")

        original = iam_contract._aws_json
        iam_contract._aws_json = fake_aws_json
        try:
            failures = iam_contract._verify_s3_gateway_endpoint(
                vpc_id="vpc-abc",
                route_table_ids=["rtb-public"],
                endpoint_id="vpce-qa",
            )
        finally:
            iam_contract._aws_json = original
        self.assertIn("route_table_missing_s3_gateway_route:rtb-public", failures)

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
        commands = payload["commands"]
        quiesce_timer = (
            "if sudo systemctl list-unit-files tokenkey-qa-maintenance.timer "
            '--no-legend 2>/dev/null | grep -q "^tokenkey-qa-maintenance[.]timer"; '
            "then sudo systemctl disable --now tokenkey-qa-maintenance.timer; fi"
        )
        self.assertIn(quiesce_timer, commands)
        self.assertIn(
            "! sudo systemctl is-active --quiet tokenkey-qa-maintenance.timer",
            commands,
        )
        self.assertIn(
            "! sudo systemctl is-active --quiet tokenkey-qa-maintenance.service",
            commands,
        )
        restore = next(command for command in commands if "qa_sync_restore" in command)
        self.assertIn("trap qa_sync_restore EXIT", restore)
        self.assertIn("qa_timer_enabled", restore)
        self.assertIn("qa_timer_active", restore)
        drain = next(
            command
            for command in commands
            if "timeout draining tokenkey-qa-maintenance.service" in command
        )
        self.assertIn(
            "while sudo systemctl is-active --quiet tokenkey-qa-maintenance.service",
            drain,
        )
        self.assertLess(commands.index(restore), commands.index(quiesce_timer))
        self.assertGreater(
            commands.index("qa_sync_committed=1"),
            commands.index("sudo systemctl disable --now tokenkey-qa-maintenance.timer"),
        )
        resolver_install = next(
            command
            for command in commands
            if "/usr/local/lib/tokenkey/resolve-app-container.sh" in command
        )
        self.assertIn("base64 -d", resolver_install)
        scratch_prepare = (
            "sudo test -e /var/lib/tokenkey/app/qa_archive_tmp || "
            "sudo install -d -m 0700 -o 1000 -g 1000 "
            "/var/lib/tokenkey/app/qa_archive_tmp"
        )
        self.assertIn(scratch_prepare, commands)
        self.assertLess(
            commands.index(quiesce_timer),
            next(
                index
                for index, command in enumerate(commands)
                if "/usr/local/bin/tokenkey-qa-maintenance.sh" in command
                and "base64 -d" in command
            ),
        )
        self.assertLess(
            commands.index(scratch_prepare),
            commands.index("sudo /usr/local/bin/tokenkey-qa-maintenance.sh --selftest"),
        )
        for directory in ("qa_blobs", "qa_dlq", "qa_capture_ledger"):
            prepare = (
                f"sudo test -e /var/lib/tokenkey/app/{directory} || "
                f"sudo install -d -m 0700 -o 1000 -g 1000 /var/lib/tokenkey/app/{directory}"
            )
            self.assertIn(prepare, commands)
        self.assertNotIn("sudo systemctl daemon-reload", commands)
        conditional_reload = next(
            command for command in commands if "unit_install_changed" in command
        )
        self.assertIn("--install-units", conditional_reload)
        self.assertIn("systemctl daemon-reload", conditional_reload)
        self.assertIn('= "true"', conditional_reload)
        self.assertFalse(
            any("/var/lib/tokenkey/data/qa_archive_tmp" in command for command in commands)
        )
        for command in commands:
            parsed = subprocess.run(["bash", "-n"], input=command, text=True, capture_output=True)
            self.assertEqual(parsed.returncode, 0, (command, parsed.stderr))

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

    def test_qa_maintenance_sync_validates_install_unit_result_before_timer_state(self) -> None:
        script = ROOT / "ops/stage0/sync-qa-maintenance-timer-via-ssm.sh"
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            fake_bin = root / "bin"
            output = root / "output"
            fake_bin.mkdir()
            output.mkdir()
            (fake_bin / "aws").write_text(
                """#!/usr/bin/env bash
if [[ "$*" == *"ssm send-command"* ]]; then echo cmd-test; exit 0; fi
if [[ "$*" == *"--query Status"* ]]; then echo Success; exit 0; fi
if [[ "$*" == *"--query ResponseCode"* ]]; then echo 0; exit 0; fi
exit 0
""",
                encoding="utf-8",
            )
            (fake_bin / "aws").chmod(0o755)
            payload_env = {
                "PATH": f"{fake_bin}:/usr/bin:/bin",
                "STAGE0_SSM_OUTPUT_DIR": str(output),
                "STAGE0_SSM_TIMEOUT_SECONDS": "10",
            }
            payload_proc = subprocess.run(
                ["bash", str(script), "i-0123456789abcdef0"],
                env=payload_env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(payload_proc.returncode, 0, payload_proc.stderr)
            commands = json.loads((output / "ssm-params.json").read_text(encoding="utf-8"))["commands"]
            install_result = next(command for command in commands if "unit_install_result" in command)
            timer_command = next(
                command
                for command in commands
                if command == "sudo systemctl disable --now tokenkey-qa-maintenance.timer"
            )

            calls = root / "calls"
            runner = root / "tokenkey-qa-maintenance.sh"
            runner.write_text(
                "#!/usr/bin/env bash\nprintf '%s\\n' \"${INSTALL_RESULT}\"\n",
                encoding="utf-8",
            )
            runner.chmod(0o755)
            (fake_bin / "sudo").write_text("#!/usr/bin/env bash\nexec \"$@\"\n", encoding="utf-8")
            (fake_bin / "sudo").chmod(0o755)
            (fake_bin / "systemctl").write_text(
                "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"${CALLS}\"\n",
                encoding="utf-8",
            )
            (fake_bin / "systemctl").chmod(0o755)

            execution = install_result.replace(
                "/usr/local/bin/tokenkey-qa-maintenance.sh", str(runner)
            )
            for install_output, should_reload, should_reach_timer in (
                ('{"changed":false}', False, True),
                ('{"changed":true}', True, True),
                ('{"changed":false,"extra":true}', False, False),
                ('{"changed":"false"}', False, False),
                ("[]", False, False),
                ("{", False, False),
            ):
                calls.unlink(missing_ok=True)
                proc = subprocess.run(
                    ["bash", "-c", f"{execution}; {timer_command}"],
                    env={
                        "PATH": f"{fake_bin}:/usr/bin:/bin",
                        "CALLS": str(calls),
                        "INSTALL_RESULT": install_output,
                        "PYTHONOPTIMIZE": "1",
                    },
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(proc.returncode, 0 if should_reach_timer else 1, proc.stderr)
                observed_calls = calls.read_text(encoding="utf-8").splitlines() if calls.exists() else []
                self.assertEqual("daemon-reload" in observed_calls, should_reload)
                self.assertEqual(
                    "disable --now tokenkey-qa-maintenance.timer" in observed_calls,
                    should_reach_timer,
                )

    def test_qa_boundary_sync_activation_receipt_forces_disabled_on_success_and_rollback(self) -> None:
        script = ROOT / "ops/stage0/sync-qa-boundary-timer-via-ssm.sh"
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            fake_bin = root / "bin"
            output = root / "output"
            fake_bin.mkdir()
            fake_aws = fake_bin / "aws"
            fake_aws.write_text(
                """#!/usr/bin/env bash
case " $* " in
  *" ssm send-command "*) printf '%s\n' command-auto ;;
  *" --query Status "*) printf '%s\n' Success ;;
  *" --query ResponseCode "*) printf '%s\n' 0 ;;
  *) printf '\n' ;;
esac
""",
                encoding="utf-8",
            )
            fake_aws.chmod(0o755)
            proc = subprocess.run(
                ["bash", str(script), "i-0123456789abcdef0"],
                env={
                    **os.environ,
                    "PATH": f"{fake_bin}:/opt/homebrew/bin:/usr/bin:/bin",
                    "QA_BOUNDARY_TIMER_STATE": "enabled",
                    "STAGE0_SSM_OUTPUT_DIR": str(output),
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            payload = json.loads((output / "ssm-params.json").read_text(encoding="utf-8"))

        commands = payload["commands"]
        restore = next(command for command in commands if "qa_sync_restore" in command)
        owner = commands[commands.index("qa_sync_committed=1") - 1]

        lifecycle_lock = next(
            command
            for command in commands
            if "/run/lock/tokenkey-qa-lifecycle.lock" in command
        )
        self.assertIn("flock -x", lifecycle_lock)
        self.assertLess(commands.index(lifecycle_lock), commands.index(restore))

        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            fake_bin = root / "bin"
            fake_bin.mkdir()
            calls = root / "calls"
            enabled = root / "enabled"
            active = root / "active"
            receipt_responses = root / "receipt-responses"
            enabled.write_text("enabled\n", encoding="utf-8")
            active.write_text("active\n", encoding="utf-8")
            (fake_bin / "docker").write_text(
                """#!/usr/bin/env bash
response="$(head -n 1 "${RECEIPT_RESPONSES}")"
tail -n +2 "${RECEIPT_RESPONSES}" > "${RECEIPT_RESPONSES}.next"
mv "${RECEIPT_RESPONSES}.next" "${RECEIPT_RESPONSES}"
if [[ "${response}" == FAIL ]]; then exit 7; fi
printf '%s\n' "${response}"
""",
                encoding="utf-8",
            )
            (fake_bin / "sudo").write_text("#!/bin/sh\nexec \"$@\"\n", encoding="utf-8")
            (fake_bin / "systemctl").write_text(
                """#!/usr/bin/env bash
printf '%s\n' "$*" >> "${CALLS}"
action="$1"
shift
now=false
if [[ "${1:-}" == "--now" ]]; then now=true; shift; fi
case "${action}" in
  enable) printf 'enabled\n' > "${ENABLED}"; if [[ "${now}" == true ]]; then printf 'active\n' > "${ACTIVE}"; fi ;;
  disable) printf 'disabled\n' > "${ENABLED}"; if [[ "${now}" == true ]]; then printf 'inactive\n' > "${ACTIVE}"; fi ;;
  start) printf 'active\n' > "${ACTIVE}" ;;
  stop) printf 'inactive\n' > "${ACTIVE}" ;;
  is-enabled) cat "${ENABLED}" ;;
  is-active) cat "${ACTIVE}" ;;
esac
""",
                encoding="utf-8",
            )
            for executable in fake_bin.iterdir():
                executable.chmod(0o755)
            env = {
                "PATH": f"{fake_bin}:/usr/bin:/bin",
                "CALLS": str(calls),
                "ENABLED": str(enabled),
                "ACTIVE": str(active),
                "RECEIPT_RESPONSES": str(receipt_responses),
            }
            for name, command, responses in (
                ("success", f"{restore}; {owner}; qa_sync_committed=1; trap - EXIT", "1\n1\n"),
                ("rollback-transition", f"{restore}; false", "0\n0\n1\n"),
                ("rollback-query-uncertain", f"{restore}; false", "0\n0\nFAIL\n"),
            ):
                enabled.write_text("enabled\n", encoding="utf-8")
                active.write_text("active\n", encoding="utf-8")
                receipt_responses.write_text(responses, encoding="utf-8")
                calls.unlink(missing_ok=True)
                proc = subprocess.run(["bash", "-c", command], env=env, capture_output=True, text=True)
                if name == "success":
                    self.assertEqual(proc.returncode, 0, proc.stderr)
                else:
                    self.assertNotEqual(proc.returncode, 0)
                self.assertEqual(enabled.read_text(encoding="utf-8").strip(), "disabled")
                self.assertEqual(active.read_text(encoding="utf-8").strip(), "inactive")
                observed = calls.read_text(encoding="utf-8")
                self.assertNotIn("enable tokenkey-qa-boundary.timer", observed)
                self.assertNotIn("start tokenkey-qa-boundary.timer", observed)
        for command in commands:
            parsed = subprocess.run(["bash", "-n"], input=command, text=True, capture_output=True)
            self.assertEqual(parsed.returncode, 0, (command, parsed.stderr))

    def test_qa_bundle_infra_verifier_checks_live_capacity_image_and_dlq(self) -> None:
        script = ROOT / "ops/qa/verify_qa_bundle_infra.sh"
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            fake_bin = root / "bin"
            fake_bin.mkdir()
            calls = root / "aws-calls"
            fake_aws = fake_bin / "aws"
            fake_aws.write_text(
                """#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${AWS_CALLS}"
case "$*" in
  *"cloudformation describe-stacks"*)
    cat <<'JSON'
{"Stacks":[{"StackStatus":"UPDATE_COMPLETE","Parameters":[{"ParameterKey":"BundleWorkerImage","ParameterValue":"ghcr.io/youxuanxue/sub2api:1.8.156"},{"ParameterKey":"BundleBrowserAllowedOrigin","ParameterValue":"https://tokenkey.dev"},{"ParameterKey":"BundleRetentionDays","ParameterValue":"2"}],"Outputs":[{"OutputKey":"QaBundleBucketName","OutputValue":"qa-bucket"},{"OutputKey":"QaBundleQueueUrl","OutputValue":"https://sqs/queue"},{"OutputKey":"QaBundleDeadLetterQueueUrl","OutputValue":"https://sqs/dlq"},{"OutputKey":"QaBundleWorkerClusterName","OutputValue":"qa-cluster"},{"OutputKey":"QaBundleWorkerServiceName","OutputValue":"qa-service"}]}]}
JSON
    ;;
  *"s3api head-bucket"*) ;;
  *"s3api get-bucket-cors"*)
    printf '{"CORSRules":[{"AllowedHeaders":["*"],"AllowedMethods":["GET","HEAD"],"AllowedOrigins":["%s"],"ExposeHeaders":["ETag","Content-Encoding","Content-Length"],"MaxAgeSeconds":300}]}\n' "${ACTUAL_CORS_ORIGIN:-https://tokenkey.dev}"
    ;;
  *"s3api get-bucket-encryption"*)
    printf '%s\n' '{"ServerSideEncryptionConfiguration":{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}}'
    ;;
  *"s3api get-bucket-lifecycle-configuration"*)
    printf '%s\n' '{"Rules":[{"ID":"expire-qa-bundle-job-surfaces","Status":"Enabled","Filter":{"Prefix":"user-qa/qa-bundles/v1/jobs/"},"Expiration":{"Days":2}}]}'
    ;;
  *"sqs get-queue-attributes"*"https://sqs/dlq"*)
    printf '{"Attributes":{"QueueArn":"arn:dlq","ApproximateNumberOfMessages":"%s"}}\n' "${DLQ_DEPTH:-0}"
    ;;
  *"sqs get-queue-attributes"*"https://sqs/queue"*)
    printf '%s\n' '{"Attributes":{"QueueArn":"arn:queue","ApproximateNumberOfMessages":"0","ApproximateNumberOfMessagesNotVisible":"0"}}'
    ;;
  *"ecs describe-services"*)
    printf '%s\n' '{"failures":[],"services":[{"status":"ACTIVE","desiredCount":1,"runningCount":1,"taskDefinition":"arn:task:1"}]}'
    ;;
  *"ecs describe-task-definition"*)
    printf '%s\n' '{"taskDefinition":{"containerDefinitions":[{"name":"qa-bundle-worker","image":"ghcr.io/youxuanxue/sub2api:1.8.156"}]}}'
    ;;
  *) echo "unexpected aws call: $*" >&2; exit 90 ;;
esac
""",
                encoding="utf-8",
            )
            fake_aws.chmod(0o755)
            base_env = {
                "PATH": f"{fake_bin}:/usr/bin:/bin",
                "AWS_CALLS": str(calls),
                "GITHUB_OUTPUT": str(root / "github-output"),
                "QA_BUNDLE_VERIFY_MODE": "expected",
                "QA_BUNDLE_WORKER_IMAGE": "ghcr.io/youxuanxue/sub2api:1.8.156",
                "QA_BUNDLE_WORKER_DESIRED_COUNT": "1",
            }
            healthy = subprocess.run(
                ["bash", str(script)], env=base_env, capture_output=True, text=True, check=False
            )
            self.assertEqual(healthy.returncode, 0, healthy.stderr)
            receipt = json.loads(healthy.stdout)
            self.assertTrue(receipt["ok"])
            self.assertEqual(receipt["image"], base_env["QA_BUNDLE_WORKER_IMAGE"])
            self.assertEqual(receipt["desired_count"], 1)
            self.assertEqual(receipt["running_count"], 1)
            self.assertEqual(receipt["browser_origin"], "https://tokenkey.dev")
            self.assertEqual(
                (root / "github-output").read_text(encoding="utf-8").splitlines(),
                [
                    "bucket=qa-bucket",
                    "queue_url=https://sqs/queue",
                    "worker_image=ghcr.io/youxuanxue/sub2api:1.8.156",
                ],
            )
            observed = calls.read_text(encoding="utf-8")
            for expected in (
                "cloudformation describe-stacks",
                "s3api head-bucket",
                "s3api get-bucket-cors",
                "s3api get-bucket-encryption",
                "s3api get-bucket-lifecycle-configuration",
                "sqs get-queue-attributes",
                "ecs describe-services",
                "ecs describe-task-definition",
            ):
                self.assertIn(expected, observed)

            discovery_output = root / "github-output-discovery"
            discovery = subprocess.run(
                ["bash", str(script)],
                env={
                    key: value
                    for key, value in base_env.items()
                    if key != "QA_BUNDLE_WORKER_IMAGE"
                }
                | {
                    "QA_BUNDLE_VERIFY_MODE": "discovery",
                    "GITHUB_OUTPUT": str(discovery_output),
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(discovery.returncode, 0, discovery.stderr)
            self.assertIn(
                "worker_image=ghcr.io/youxuanxue/sub2api:1.8.156",
                discovery_output.read_text(encoding="utf-8").splitlines(),
            )

            missing_expected = subprocess.run(
                ["bash", str(script)],
                env={
                    key: value
                    for key, value in base_env.items()
                    if key != "QA_BUNDLE_WORKER_IMAGE"
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(missing_expected.returncode, 0)
            self.assertIn(
                "QA_BUNDLE_WORKER_IMAGE is required in expected mode",
                missing_expected.stderr,
            )

            cors_drift = subprocess.run(
                ["bash", str(script)],
                env={**base_env, "ACTUAL_CORS_ORIGIN": "https://api.tokenkey.dev"},
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(cors_drift.returncode, 0)
            self.assertIn("QA Bundle bucket CORS drift", cors_drift.stderr)

            unhealthy = subprocess.run(
                ["bash", str(script)],
                env={**base_env, "DLQ_DEPTH": "2"},
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(unhealthy.returncode, 0)
            self.assertIn("QA Bundle DLQ is not empty: 2", unhealthy.stderr)

    def test_qa_bundle_canary_ssm_wrapper_requires_canonical_receipt(self) -> None:
        script = ROOT / "ops/stage0/run-qa-bundle-canary-via-ssm.sh"
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            fake_bin = root / "bin"
            fake_bin.mkdir()
            fake_aws = fake_bin / "aws"
            fake_aws.write_text(
                """#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  *"ssm send-command"*) printf '%s\n' command-canary ;;
  *"--query Status"*) printf '%s\n' Success ;;
  *"--query StandardOutputContent"*)
    printf '{"schema_version":"qa-bundle-canary-v1","ok":true,"commit_count":%s,"record_count":0,"job_id":"%064d"}\n' "${CANARY_COMMIT_COUNT:-24}" 0
    ;;
  *"--query StandardErrorContent"*) ;;
  *"--query ResponseCode"*) printf '%s\n' 0 ;;
  *) echo "unexpected aws call: $*" >&2; exit 90 ;;
esac
""",
                encoding="utf-8",
            )
            fake_aws.chmod(0o755)
            base_env = {
                "PATH": f"{fake_bin}:/usr/bin:/bin",
                "STAGE0_SSM_TIMEOUT_SECONDS": "10",
            }

            healthy_output = root / "healthy"
            healthy = subprocess.run(
                ["bash", str(script), "i-0123456789abcdef0"],
                env={**base_env, "STAGE0_SSM_OUTPUT_DIR": str(healthy_output)},
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(healthy.returncode, 0, healthy.stderr)
            payload = json.loads((healthy_output / "ssm-params.json").read_text(encoding="utf-8"))
            self.assertEqual(
                payload["commands"],
                ["set -euo pipefail", "sudo /usr/local/bin/tokenkey-qa-maintenance.sh --qa-bundle-canary"],
            )

            invalid_output = root / "invalid"
            invalid = subprocess.run(
                ["bash", str(script), "i-0123456789abcdef0"],
                env={
                    **base_env,
                    "STAGE0_SSM_OUTPUT_DIR": str(invalid_output),
                    "CANARY_COMMIT_COUNT": "23",
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(invalid.returncode, 0)
            self.assertIn("invalid QA Bundle canary receipt", invalid.stderr)

    def test_raw_archive_cfn_keeps_app_and_recovery_permissions_separate(self) -> None:
        body = (ROOT / "deploy/aws/cloudformation/stage0-qa-raw-archive.yaml").read_text(
            encoding="utf-8"
        )
        app_policy = body[
            body.index("Sid: AllowAppInstanceRoleWriteRaw") :
            body.index("Sid: AllowOpsRecoveryRoleReadRaw")
        ]
        self.assertNotIn("s3:DeleteObject", app_policy)
        self.assertNotIn("Sid: AllowAppInstanceRoleListRawPrefix", app_policy)
        self.assertNotIn("s3:ListBucket", app_policy)
        self.assertIn("raw/v1/date=*/hour=*/commit.json", app_policy)
        self.assertNotIn("raw/partial/*", app_policy)
        self.assertIn("orphan-evidence-index.jsonl.zst", app_policy)
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
        self.assertNotIn("CAPABILITY_NAMED_IAM", body)
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
if [[ "$*" == *"cloudformation describe-stacks"* ]]; then echo arn:aws:iam::123456789012:role/generated-existing-role; exit 0; fi
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
                    "QA_BUNDLE_WORKER_PUBLIC_SUBNET_IDS": "subnet-1234",
                    "QA_BUNDLE_WORKER_IMAGE": "ghcr.io/youxuanxue/sub2api:1.8.156",
                    "QA_BUNDLE_BROWSER_ALLOWED_ORIGIN": "https://api.tokenkey.dev",
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
            self.assertNotIn("tokenkey-qa-stale-cleanup.timer", script)
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

    def test_us045_qa_archive_closeout_rejects_repair_apply_before_aws(self) -> None:
        module = _load_closeout_module()
        window_text = "2026-08-07T01:00:00Z"
        with mock.patch.object(module, "_aws_json") as aws_json:
            with self.assertRaisesRegex(module.QAArchiveCloseoutError, "unsupported command"):
                module.run(
                    "repair-apply",
                    window_text,
                    confirm="tokenkey-prod-qa-archive-repair-v1:" + window_text,
                )
        aws_json.assert_not_called()

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
