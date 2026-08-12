#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import unittest
import datetime as dt
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "ops" / "qa"))

import prod_phase2_live_health as live_health  # noqa: E402
import qa_phase2_health as health  # noqa: E402
import verify_raw_archive_iam_contract as iam_contract  # noqa: E402


def _load_runtime_case():
    spec = importlib.util.spec_from_file_location(
        "qa_phase2_runtime_case",
        ROOT / "ops/qa/test_qa_maintenance_phase2_runtime.py",
    )
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module.QAPhase2OperatorAndHealthTest()


def _fresh_snapshot() -> tuple[dict, dt.datetime]:
    now = dt.datetime.now(dt.timezone.utc)
    snapshot, _ = _load_runtime_case()._healthy_snapshot()
    snapshot.setdefault("qa_records", {"partition_owner": "partitioned", "hourly_cutover_active": False})
    finished = now - dt.timedelta(seconds=30)
    started = finished - dt.timedelta(seconds=30)
    iso = lambda value: value.strftime("%Y-%m-%dT%H:%M:%SZ")
    snapshot["systemd"]["finished_at"] = iso(finished)
    snapshot["host_receipt"]["started_at"] = iso(started)
    snapshot["host_receipt"]["finished_at"] = iso(finished)
    snapshot["database_heartbeat"]["last_run_at"] = iso(started)
    snapshot["database_heartbeat"]["last_success_at"] = iso(finished)
    boundary_finished = now - dt.timedelta(minutes=30)
    boundary_started = boundary_finished - dt.timedelta(seconds=30)
    snapshot["boundary_systemd"]["finished_at"] = iso(boundary_finished)
    snapshot["boundary_host_receipt"]["started_at"] = iso(boundary_started)
    snapshot["boundary_host_receipt"]["finished_at"] = iso(boundary_finished)
    snapshot["boundary_database_heartbeat"]["last_run_at"] = iso(boundary_started)
    snapshot["boundary_database_heartbeat"]["last_success_at"] = iso(boundary_finished)
    current_hour = now.replace(minute=0, second=0, microsecond=0)
    snapshot["qa_records"]["future_coverage_start_utc"] = iso(current_hour)
    snapshot["qa_records"]["future_coverage_end_utc"] = iso(
        current_hour + dt.timedelta(hours=72)
    )
    return snapshot, now


class ProdPhase2LiveHealthTest(unittest.TestCase):
    def _healthy_probe_text(self) -> str:
        snapshot, _ = _fresh_snapshot()
        return "\n".join(
            [
                "PHASE2SYSTEMD " + json.dumps(snapshot["systemd"], sort_keys=True),
                "PHASE2RECEIPT " + json.dumps(snapshot["host_receipt"], sort_keys=True),
                "PHASE2HEARTBEAT " + json.dumps(snapshot["database_heartbeat"], sort_keys=True),
                "PHASE2ARCHIVE " + json.dumps(snapshot["archive_control"], sort_keys=True),
                "PHASE2BOUNDARYSYSTEMD " + json.dumps(snapshot["boundary_systemd"], sort_keys=True),
                "PHASE2BOUNDARYRECEIPT " + json.dumps(snapshot["boundary_host_receipt"], sort_keys=True),
                "PHASE2BOUNDARYHEARTBEAT " + json.dumps(snapshot["boundary_database_heartbeat"], sort_keys=True),
                "PHASE2QARECORDS " + json.dumps(snapshot["qa_records"], sort_keys=True),
            ]
        )

    def test_parse_probe_output_builds_snapshot(self) -> None:
        snapshot = live_health._parse_probe_output(self._healthy_probe_text())
        self.assertIn("systemd", snapshot)
        self.assertIn("host_receipt", snapshot)
        self.assertEqual(snapshot["qa_records"]["partition_owner"], "partitioned")

    def test_parse_probe_output_treats_zero_qa_rows_as_partitioned(self) -> None:
        snapshot = live_health._parse_probe_output(
            'PHASE2QARECORDS {"default_rows":0,"non_default_rows":0}'
        )
        self.assertEqual(snapshot["qa_records"].get("partition_owner"), "partitioned")

    def test_evaluate_snapshot_marks_default_only_partition_failed(self) -> None:
        snapshot, now = _fresh_snapshot()
        snapshot["qa_records"].update(partition_owner="default_only", default_present=True)
        payload = live_health.evaluate_snapshot(snapshot, skip_iam=True, now=now)
        self.assertIn("qa_records_partition_owner_default_only", payload["warnings"])
        self.assertEqual(payload["health"]["status"], "failed")
        self.assertIn("qa_records_partition_owner_default_only", payload["health"]["reasons"])

    def test_evaluate_snapshot_accepts_default_only_before_scheduled_t0(self) -> None:
        snapshot, now = _fresh_snapshot()
        t0 = now.replace(minute=0, second=0, microsecond=0) + dt.timedelta(hours=1)
        iso = lambda value: value.strftime("%Y-%m-%dT%H:%M:%SZ")
        snapshot["boundary_systemd"].update(
            timer_enabled=False,
            timer_active=False,
            service_result="unknown",
            finished_at=None,
        )
        snapshot["boundary_host_receipt"] = None
        snapshot["boundary_database_heartbeat"] = None
        snapshot["qa_records"].update(
            partition_owner="default_only",
            default_rows=10,
            non_default_rows=0,
            hourly_cutover_finalize_receipt_present=False,
            hourly_cutover_finalized=False,
            activate_t0_utc=iso(t0),
            activate_applied_at=iso(now - dt.timedelta(minutes=30)),
            finalize_t0_utc=None,
            finalize_plan_hash=None,
            finalize_applied_at=None,
            default_present=True,
            default_rows_after_t0=0,
            future_coverage_start_utc=iso(t0),
            future_coverage_end_utc=iso(t0 + dt.timedelta(hours=72)),
            current_hour_partition_missing=True,
        )

        payload = live_health.evaluate_snapshot(snapshot, skip_iam=True, now=now)

        self.assertEqual(payload["health"]["status"], "healthy", payload)
        self.assertEqual(payload["warnings"], [], payload)

    def test_cli_from_probe_stdin(self) -> None:
        proc = subprocess.run(
            [
                sys.executable,
                str(ROOT / "ops/qa/prod_phase2_live_health.py"),
                "--from-probe-stdin",
                "--skip-iam",
            ],
            input=self._healthy_probe_text(),
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        payload = json.loads(proc.stdout)
        self.assertEqual(payload["health"]["status"], "healthy")

    def test_cli_degraded_exit_zero(self) -> None:
        snapshot, now = _fresh_snapshot()
        snapshot["archive_control"]["terminal_failures_after_cutover"] = [
            {
                "window_start": "2026-08-07T22:00:00Z",
                "verification_error_code": "source_unavailable_after_retention",
            }
        ]
        probe_text = "\n".join(
            [
                "PHASE2SYSTEMD " + json.dumps(snapshot["systemd"], sort_keys=True),
                "PHASE2RECEIPT " + json.dumps(snapshot["host_receipt"], sort_keys=True),
                "PHASE2HEARTBEAT " + json.dumps(snapshot["database_heartbeat"], sort_keys=True),
                "PHASE2ARCHIVE " + json.dumps(snapshot["archive_control"], sort_keys=True),
                "PHASE2BOUNDARYSYSTEMD " + json.dumps(snapshot["boundary_systemd"], sort_keys=True),
                "PHASE2BOUNDARYRECEIPT " + json.dumps(snapshot["boundary_host_receipt"], sort_keys=True),
                "PHASE2BOUNDARYHEARTBEAT " + json.dumps(snapshot["boundary_database_heartbeat"], sort_keys=True),
                "PHASE2QARECORDS " + json.dumps(snapshot.get("qa_records", {"partition_owner": "partitioned"}), sort_keys=True),
            ]
        )
        proc = subprocess.run(
            [
                sys.executable,
                str(ROOT / "ops/qa/prod_phase2_live_health.py"),
                "--from-probe-stdin",
                "--skip-iam",
            ],
            input=probe_text,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        payload = json.loads(proc.stdout)
        self.assertEqual(payload["health"]["status"], "degraded")

    def test_cli_nonwhitelisted_degraded_exit_nonzero(self) -> None:
        snapshot, _ = _fresh_snapshot()
        snapshot["archive_control"]["terminal_failures_after_cutover"] = [
            {
                "window_start": "2026-08-07T22:00:00Z",
                "verification_error_code": "source_unavailable_after_retention",
            }
        ]
        snapshot["qa_records"].update(partition_owner="default_only", default_present=True)
        probe_text = "\n".join(
            [
                "PHASE2SYSTEMD " + json.dumps(snapshot["systemd"], sort_keys=True),
                "PHASE2RECEIPT " + json.dumps(snapshot["host_receipt"], sort_keys=True),
                "PHASE2HEARTBEAT " + json.dumps(snapshot["database_heartbeat"], sort_keys=True),
                "PHASE2ARCHIVE " + json.dumps(snapshot["archive_control"], sort_keys=True),
                "PHASE2BOUNDARYSYSTEMD " + json.dumps(snapshot["boundary_systemd"], sort_keys=True),
                "PHASE2BOUNDARYRECEIPT " + json.dumps(snapshot["boundary_host_receipt"], sort_keys=True),
                "PHASE2BOUNDARYHEARTBEAT " + json.dumps(snapshot["boundary_database_heartbeat"], sort_keys=True),
                "PHASE2QARECORDS " + json.dumps(snapshot["qa_records"], sort_keys=True),
            ]
        )
        proc = subprocess.run(
            [
                sys.executable,
                str(ROOT / "ops/qa/prod_phase2_live_health.py"),
                "--from-probe-stdin",
                "--skip-iam",
            ],
            input=probe_text,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertNotEqual(proc.returncode, 0, proc.stdout)

    def test_timestamp_parses_systemd_finished_at(self) -> None:
        parsed = health._timestamp("Mon 2026-08-10 13:15:01 UTC")
        self.assertIsNotNone(parsed)
        assert parsed is not None
        self.assertEqual(
            parsed,
            dt.datetime(2026, 8, 10, 13, 15, 1, tzinfo=dt.timezone.utc),
        )

    def test_terminal_compensation_contradiction_fails_closed(self) -> None:
        snapshot, now = _fresh_snapshot()
        terminal_window = "2026-08-07T22:00:00Z"
        snapshot["host_receipt"]["compensation"] = {
            "window_start": terminal_window,
            "state": "failed",
            "verification_error_code": "source_unavailable_after_retention",
            "restore_verified": False,
            "cleanup_eligible": False,
        }
        snapshot["database_heartbeat"]["last_result"] += (
            " compensation_window=2026-08-07T23:00:00Z"
            " compensation_state=failed"
            " compensation_error_code=source_unavailable_after_retention"
        )
        snapshot["archive_control"]["compensation"] = None
        snapshot["archive_control"]["terminal_failures_after_cutover"] = [
            {
                "window_start": terminal_window,
                "verification_error_code": "source_unavailable_after_retention",
            }
        ]
        verdict = health.evaluate(snapshot, now=now, catchup_gap_policy="accepted_terminal")
        self.assertEqual(verdict["status"], "failed", verdict)
        self.assertIn("compensation_control_missing", verdict["catchup_reasons"], verdict)
        self.assertIn("compensation_window_heartbeat_mismatch", verdict["catchup_reasons"], verdict)

    def test_terminal_inventory_without_receipt_stays_degraded(self) -> None:
        snapshot, now = _fresh_snapshot()
        terminal_window = "2026-08-07T22:00:00Z"
        snapshot["archive_control"]["compensation"] = {
            "window_start": terminal_window,
            "state": "failed",
            "verification_error_code": "source_unavailable_after_retention",
            "restore_verified": False,
            "cleanup_eligible": False,
        }
        snapshot["archive_control"]["terminal_failures_after_cutover"] = [
            {
                "window_start": terminal_window,
                "verification_error_code": "source_unavailable_after_retention",
            }
        ]
        verdict = health.evaluate(snapshot, now=now, catchup_gap_policy="accepted_terminal")
        self.assertEqual(verdict["status"], "degraded", verdict)
        self.assertIn("catchup_terminal_gaps_present", verdict["catchup_reasons"], verdict)
        self.assertNotIn("compensation_control_without_receipt", verdict["catchup_reasons"], verdict)

    def test_terminal_compensation_receipt_stays_degraded(self) -> None:
        snapshot, now = _fresh_snapshot()
        terminal_window = "2026-08-07T22:00:00Z"
        snapshot["host_receipt"]["compensation"] = {
            "window_start": terminal_window,
            "state": "failed",
            "verification_error_code": "source_unavailable_after_retention",
            "restore_verified": False,
            "cleanup_eligible": False,
        }
        snapshot["database_heartbeat"]["last_result"] += (
            f" compensation_window={terminal_window}"
            " compensation_state=failed"
            " compensation_error_code=source_unavailable_after_retention"
        )
        snapshot["archive_control"]["compensation"] = {
            "window_start": terminal_window,
            "state": "failed",
            "verification_error_code": "source_unavailable_after_retention",
            "restore_verified": False,
            "cleanup_eligible": False,
        }
        snapshot["archive_control"]["terminal_failures_after_cutover"] = [
            {
                "window_start": terminal_window,
                "verification_error_code": "source_unavailable_after_retention",
            },
            {
                "window_start": "2026-08-07T23:00:00Z",
                "verification_error_code": "source_unavailable_after_retention",
            },
        ]
        verdict = health.evaluate(snapshot, now=now, catchup_gap_policy="accepted_terminal")
        self.assertEqual(verdict["status"], "degraded", verdict)
        self.assertNotIn("compensation_not_committed_restore_verified", verdict["reasons"], verdict)
        self.assertIn("catchup_terminal_gaps_present", verdict["catchup_reasons"], verdict)


class VerifyRawArchiveIAMContractTest(unittest.TestCase):
    def test_resolve_app_role_arn_reads_stack_parameter(self) -> None:
        with mock.patch.object(
            iam_contract,
            "_stack_parameter",
            return_value="arn:aws:iam::123456789012:role/tokenkey-prod-app",
        ) as mocked:
            self.assertEqual(
                iam_contract.resolve_app_role_arn(),
                "arn:aws:iam::123456789012:role/tokenkey-prod-app",
            )
            mocked.assert_called_once_with("tokenkey-prod-qa-raw-archive", "AppInstanceRoleArn")

    def test_rejects_list_bucket_on_app_role(self) -> None:
        role = "arn:aws:iam::123456789012:role/app"
        statements = [
            {
                "Sid": "AllowAppInstanceRoleWriteRaw",
                "Principal": {"AWS": role},
                "Action": ["s3:PutObject"],
                "Resource": [
                    "arn:aws:s3:::tokenkey-prod-qa-raw-archive-123456789012/raw/v1/date=*/hour=*/commit.json"
                ],
            },
            {
                "Sid": "AllowAppInstanceRoleVerifyRaw",
                "Principal": {"AWS": role},
                "Action": "s3:GetObject",
                "Resource": [
                    "arn:aws:s3:::tokenkey-prod-qa-raw-archive-123456789012/raw/v1/date=*/hour=*/commit.json"
                ],
            },
            {
                "Sid": "AllowAppInstanceRoleList",
                "Principal": {"AWS": role},
                "Action": "s3:ListBucket",
                "Resource": "arn:aws:s3:::tokenkey-prod-qa-raw-archive-123456789012",
            },
        ]
        verdict = iam_contract.evaluate(
            bucket="tokenkey-prod-qa-raw-archive-123456789012",
            app_role_arn=role,
            statements=statements,
        )
        self.assertFalse(verdict["ok"])
        self.assertIn("unexpected_app_sid:AllowAppInstanceRoleList", verdict["failures"])

    def test_accepts_suffix_scoped_app_role(self) -> None:
        role = "arn:aws:iam::123456789012:role/app"
        bucket = "tokenkey-prod-qa-raw-archive-123456789012"
        resources = iam_contract._expected_app_resources(bucket)
        statements = [
            {
                "Sid": "AllowAppInstanceRoleWriteRaw",
                "Effect": "Allow",
                "Principal": {"AWS": role},
                "Action": sorted(iam_contract.EXPECTED_WRITE_ACTIONS),
                "Resource": resources,
            },
            {
                "Sid": "AllowAppInstanceRoleVerifyRaw",
                "Effect": "Allow",
                "Principal": {"AWS": role},
                "Action": sorted(iam_contract.EXPECTED_VERIFY_ACTIONS),
                "Resource": resources,
            },
        ]
        verdict = iam_contract.evaluate(bucket=bucket, app_role_arn=role, statements=statements)
        self.assertTrue(verdict["ok"], verdict)

    def test_rejects_delete_object_on_app_role(self) -> None:
        role = "arn:aws:iam::123456789012:role/app"
        bucket = "tokenkey-prod-qa-raw-archive-123456789012"
        resources = iam_contract._expected_app_resources(bucket)
        statements = [
            {
                "Sid": "AllowAppInstanceRoleWriteRaw",
                "Effect": "Allow",
                "Principal": {"AWS": role},
                "Action": ["s3:PutObject", "s3:DeleteObject"],
                "Resource": resources,
            },
            {
                "Sid": "AllowAppInstanceRoleVerifyRaw",
                "Effect": "Allow",
                "Principal": {"AWS": role},
                "Action": "s3:GetObject",
                "Resource": resources,
            },
        ]
        verdict = iam_contract.evaluate(bucket=bucket, app_role_arn=role, statements=statements)
        self.assertFalse(verdict["ok"])
        self.assertTrue(any("unexpected_action" in item or "missing_action" in item for item in verdict["failures"]))


if __name__ == "__main__":
    raise SystemExit(unittest.main())
