#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import contextlib
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
import datetime as dt
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "ops" / "qa"))

import prod_phase2_live_health as live_health  # noqa: E402
import prod_phase2_baseline as baseline  # noqa: E402
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
    snapshot["storage"] = {
        "status": "ok", "runtime_contract": "independent-v1", "runtime_stopped": True,
        "environment": {"QA_ARCHIVE_ENABLED": "true", **live_health.live_archive_coordinates()},
        "credentials_file_present": False, "instance_role_available": True,
    }
    snapshot.setdefault("qa_records", {"partition_owner": "partitioned", "hourly_cutover_active": False})
    finished = now - dt.timedelta(seconds=30)
    started = finished - dt.timedelta(seconds=30)
    iso = lambda value: value.strftime("%Y-%m-%dT%H:%M:%SZ")
    snapshot["systemd"]["last_trigger_at"] = iso(started)
    snapshot["systemd"]["finished_at"] = iso(finished)
    snapshot["host_receipt"]["started_at"] = iso(started)
    snapshot["host_receipt"]["finished_at"] = iso(finished)
    snapshot["database_heartbeat"]["last_run_at"] = iso(started)
    snapshot["database_heartbeat"]["last_success_at"] = iso(finished)
    boundary_finished = now - dt.timedelta(minutes=30)
    boundary_started = boundary_finished - dt.timedelta(seconds=30)
    snapshot["boundary_systemd"]["last_trigger_at"] = iso(boundary_started)
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


def _pause_maintenance(snapshot):
    snapshot["systemd"].update(timer_enabled=False, timer_active=False,
        timer_active_state="inactive", timer_unit_file_state="disabled",
        service_active_state="inactive")


class ProdPhase2LiveHealthTest(unittest.TestCase):
    def _healthy_probe_text(self) -> str:
        snapshot, _ = _fresh_snapshot()
        _pause_maintenance(snapshot)
        return "\n".join(
            [
                "PHASE2SYSTEMD " + json.dumps(snapshot["systemd"], sort_keys=True),
                "PHASE2STORAGE " + json.dumps(snapshot["storage"], sort_keys=True),
                "PHASE2RECEIPT " + json.dumps(snapshot["host_receipt"], sort_keys=True),
                "PHASE2HEARTBEAT " + json.dumps(snapshot["database_heartbeat"], sort_keys=True),
                "PHASE2ARCHIVE " + json.dumps(snapshot["archive_control"], sort_keys=True),
                "PHASE2BOUNDARYSYSTEMD " + json.dumps(snapshot["boundary_systemd"], sort_keys=True),
                "PHASE2BOUNDARYRECEIPT " + json.dumps(snapshot["boundary_host_receipt"], sort_keys=True),
                "PHASE2BOUNDARYHEARTBEAT " + json.dumps(snapshot["boundary_database_heartbeat"], sort_keys=True),
                "PHASE2QARECORDS " + json.dumps(snapshot["qa_records"], sort_keys=True),
            ]
        )

    def test_pause_ignores_old_archive_but_preserves_boundary_and_storage(self):
        snapshot, now = _fresh_snapshot()
        _pause_maintenance(snapshot)
        for key in ("host_receipt", "database_heartbeat", "archive_control"):
            snapshot.pop(key)
        def evaluate():
            return live_health.evaluate_snapshot(snapshot, now=now,
                maintenance_pause_reason="operator paused uploads")["health"]
        self.assertEqual(evaluate()["status"], "healthy")
        snapshot["boundary_systemd"]["timer_active"] = False
        self.assertIn("boundary_timer_not_active", evaluate()["reasons"])
        snapshot["boundary_systemd"]["timer_active"] = True
        snapshot["qa_records"]["current_hour_partition_missing"] = True
        self.assertIn("qa_records_current_partition_missing", evaluate()["reasons"])
        snapshot["qa_records"]["current_hour_partition_missing"] = False
        snapshot["storage"]["runtime_stopped"] = False
        self.assertIn("qa_runtime_pin_invalid", evaluate()["reasons"])

    def test_pause_rejects_resumed_or_unknown_systemd_states(self):
        for key, value in (("timer_active_state", "active"),
                           ("timer_unit_file_state", "enabled"),
                           ("service_active_state", "activating"),
                           ("service_active_state", "active"),
                           ("service_active_state", "deactivating"),
                           ("timer_active_state", "")):
            with self.subTest(key=key, value=value):
                snapshot, now = _fresh_snapshot()
                _pause_maintenance(snapshot)
                snapshot["systemd"][key] = value
                result = live_health.evaluate_snapshot(snapshot, now=now,
                    maintenance_pause_reason="operator paused uploads")
                self.assertIn("paused_maintenance_not_stopped", result["health"]["reasons"])
        snapshot, now = _fresh_snapshot()
        _pause_maintenance(snapshot)
        result = live_health.evaluate_snapshot(snapshot, now=now)
        self.assertIn("timer_not_active", result["health"]["reasons"])

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

    def test_cli_defaults_to_live_storage_check_without_control_plane_iam(self) -> None:
        proc = subprocess.run(
            [
                sys.executable,
                str(ROOT / "ops/qa/prod_phase2_live_health.py"),
                "--from-probe-stdin",
            ],
            input=self._healthy_probe_text(),
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        payload = json.loads(proc.stdout)
        self.assertEqual(payload["iam_contract"], {"ok": True, "status": "skipped", "failures": []})

    def test_baseline_does_not_touch_aws_without_legacy_flag(self) -> None:
        with mock.patch.object(baseline, "_account_id", side_effect=AssertionError("AWS must be opt-in")):
            account_id, bucket = baseline._archive_bucket_status(legacy_aws=False)
        self.assertIsNone(account_id)
        self.assertEqual(bucket, {"status": "skipped", "reason": "control-plane bucket audit not requested"})

    def test_live_storage_fails_on_missing_facts_aws_and_invalid_pin(self) -> None:
        for mutation in ("missing", "hetzner", "credentials", "role", "running", "label"):
            with self.subTest(mutation=mutation):
                snapshot, now = _fresh_snapshot()
                storage = snapshot["storage"]
                if mutation == "missing":
                    del snapshot["storage"]
                elif mutation == "hetzner":
                    storage["environment"]["QA_ARCHIVE_STORAGE_ENDPOINT"] = ""
                    storage["environment"]["QA_ARCHIVE_STORAGE_REGION"] = "fsn1"
                elif mutation == "credentials":
                    storage["credentials_file_present"] = True
                elif mutation == "role":
                    storage["instance_role_available"] = False
                elif mutation == "running":
                    storage["runtime_stopped"] = False
                else:
                    storage["runtime_contract"] = ""
                result = live_health.evaluate_snapshot(snapshot, now=now)
                self.assertFalse(result["storage_contract"]["ok"])
                self.assertEqual(result["health"]["status"], "failed")
                self.assertTrue(result["health"]["forward_reasons"])

    def test_legacy_iam_failure_is_separate_from_live_health(self) -> None:
        snapshot, now = _fresh_snapshot()
        with (
            mock.patch.object(iam_contract, "_account_id", return_value="123456789012"),
            mock.patch.object(iam_contract, "resolve_app_role_arn", return_value="legacy-role"),
            mock.patch.object(iam_contract, "evaluate", return_value={"ok": False, "failures": ["legacy drift"]}) as verify,
        ):
            result = live_health.evaluate_snapshot(snapshot, skip_iam=False, now=now)
        self.assertEqual(verify.call_count, 1)
        self.assertFalse(result["iam_contract"]["ok"])
        self.assertEqual(result["health"]["status"], "healthy")

    def test_baseline_checks_pin_and_fails_when_storage_is_unknown(self) -> None:
        import contextlib
        import io

        for body, code in ((self._healthy_probe_text(), 0), ("", 2)):
            with (
                self.subTest(code=code),
                mock.patch.object(sys, "argv", ["baseline", "--json"]),
                mock.patch.object(baseline, "resolve_prod_instance", return_value="mi-test"),
                mock.patch.object(baseline, "run_shell_b64", return_value=body),
                mock.patch.object(baseline, "_account_id", side_effect=AssertionError("legacy AWS disabled")),
                contextlib.redirect_stdout(io.StringIO()) as output,
            ):
                self.assertEqual(baseline.main(), code)
                self.assertEqual(json.loads(output.getvalue())["storage_contract"]["ok"], code == 0)

    @unittest.skipUnless(shutil.which("docker"), "Docker Compose CLI required")
    def test_runtime_compose_requires_each_storage_coordinate(self) -> None:
        env = {
            "PATH": os.environ["PATH"], "QA_MAINTENANCE_IMAGE": "example:1.0.0",
            "QA_MAINTENANCE_TAG": "1.0.0", "POSTGRES_PASSWORD": "synthetic-password",
            "QA_RUNTIME_NETWORK": "synthetic-data", **live_health.live_archive_coordinates(),
            "QA_ARCHIVE_STORAGE_CREDENTIALS_FILE": "/run/secrets/tokenkey-qa/raw.json",
        }
        command = ["docker", "compose", "--env-file", os.devnull, "-f",
                   str(ROOT / "deploy/aws/stage0/qa-runtime-compose.yml"), "config", "--format", "json"]
        result = subprocess.run(command, env=env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        resolved = json.loads(result.stdout)["services"]["runtime"]["environment"]
        for key, value in live_health.live_archive_coordinates().items():
            self.assertEqual(resolved[key], value)
        native = subprocess.run(command, env={k: v for k, v in env.items() if k != "QA_ARCHIVE_STORAGE_CREDENTIALS_FILE"}, text=True, capture_output=True)
        self.assertEqual(native.returncode, 0, native.stderr)
        self.assertEqual(json.loads(native.stdout)["services"]["runtime"]["environment"]["QA_ARCHIVE_STORAGE_CREDENTIALS_FILE"], "")
        for key in live_health.live_archive_coordinates():
            with self.subTest(missing=key):
                missing = {name: value for name, value in env.items() if name != key}
                failed = subprocess.run(command, env=missing, text=True, capture_output=True)
                self.assertNotEqual(failed.returncode, 0)
                self.assertIn(f"{key} is required", failed.stderr)

    def test_storage_probe_reads_stopped_pin_and_redacts_secrets(self) -> None:
        snapshot, _ = _fresh_snapshot()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixture = root / "pin.json"
            coordinates = snapshot["storage"]["environment"]
            facts = [{
                "Config": {
                    "Env": [f"{key}={value}" for key, value in coordinates.items()] + [
                        "QA_ARCHIVE_STORAGE_CREDENTIALS_FILE=/private/secret.json",
                        "DATABASE_PASSWORD=do-not-print-password",
                        "AWS_SECRET_ACCESS_KEY=do-not-print-key",
                    ],
                    "Labels": {"dev.tokenkey.qa-runtime": "independent-v1"},
                },
                "State": {"Running": False},
            }]
            fixture.write_text(json.dumps(facts))
            (root / "docker").write_text(
                '#!/bin/sh\nif [ "$1" = inspect ]; then\n'
                '  [ "$2" = tokenkey-qa-runtime ] || exit 2\n'
                '  cat "$TEST_PIN_FIXTURE"\nelse cat >/dev/null; fi\n'
            )
            (root / "systemctl").write_text(
                '#!/bin/sh\ncase "$*" in\n'
                '  "show tokenkey-qa-maintenance.timer -p UnitFileState --value") echo disabled ;;\n'
                '  "show tokenkey-qa-maintenance.timer -p ActiveState --value"|"show tokenkey-qa-maintenance.service -p ActiveState --value") echo inactive ;;\n'
                '  *) exit 1 ;;\nesac\n'
            )
            for binary in ("docker", "systemctl"):
                (root / binary).chmod(0o755)
            env = {**os.environ, "PATH": f"{root}:{os.environ['PATH']}",
                   "TEST_PIN_FIXTURE": str(fixture), "QA_MAINTENANCE_RECEIPT": str(root / "absent"),
                   "QA_BOUNDARY_RECEIPT": str(root / "absent-boundary")}
            for missing in (False, True):
                with self.subTest(missing=missing):
                    if missing:
                        fixture.unlink()
                    result = subprocess.run(["bash", str(live_health.PROBE)], env=env, text=True, capture_output=True)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    parsed = live_health._parse_probe_output(result.stdout)
                    self.assertEqual(parsed["systemd"]["timer_unit_file_state"], "disabled")
                    self.assertEqual(parsed["systemd"]["timer_active_state"], "inactive")
                    self.assertEqual(parsed["systemd"]["service_active_state"], "inactive")
                    storage = parsed["storage"]
                    self.assertEqual(storage, {"status": "unknown", "reason": "qa_runtime_storage_unavailable"}
                                     if missing else {**snapshot["storage"], "credentials_file_present": True, "instance_role_available": False})
                    for secret in ("/private/secret.json", "do-not-print-password", "do-not-print-key"):
                        self.assertNotIn(secret, result.stdout + result.stderr)

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
                "PHASE2STORAGE " + json.dumps(snapshot["storage"], sort_keys=True),
                "PHASE2RECEIPT " + json.dumps(snapshot["host_receipt"], sort_keys=True),
                "PHASE2HEARTBEAT " + json.dumps(snapshot["database_heartbeat"], sort_keys=True),
                "PHASE2ARCHIVE " + json.dumps(snapshot["archive_control"], sort_keys=True),
                "PHASE2BOUNDARYSYSTEMD " + json.dumps(snapshot["boundary_systemd"], sort_keys=True),
                "PHASE2BOUNDARYRECEIPT " + json.dumps(snapshot["boundary_host_receipt"], sort_keys=True),
                "PHASE2BOUNDARYHEARTBEAT " + json.dumps(snapshot["boundary_database_heartbeat"], sort_keys=True),
                "PHASE2QARECORDS " + json.dumps(snapshot.get("qa_records", {"partition_owner": "partitioned"}), sort_keys=True),
            ]
        )
        stdout = io.StringIO()
        with mock.patch.object(live_health, "load_maintenance_pause_reason", return_value=""), \
             mock.patch.object(sys, "argv", ["health", "--from-probe-stdin", "--skip-iam"]), \
             mock.patch.object(sys, "stdin", io.StringIO(probe_text)), contextlib.redirect_stdout(stdout):
            code = live_health.main()
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(stdout.getvalue())["health"]["status"], "degraded")

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
                "PHASE2STORAGE " + json.dumps(snapshot["storage"], sort_keys=True),
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
