#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import contextlib
import copy
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


def _pause_boundary(snapshot):
    snapshot["boundary_systemd"].update(timer_enabled=False, timer_active=False)


class ProdPhase2LiveHealthTest(unittest.TestCase):
    def _healthy_probe_text(self) -> str:
        # Mirrors the live pause owner: maintenance and boundary both paused.
        snapshot, _ = _fresh_snapshot()
        _pause_maintenance(snapshot)
        _pause_boundary(snapshot)
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

    def test_boundary_pause_owner_overrides_finalized_lifecycle(self):
        # Single truth: host_watch.paused_jobs. A paused boundary must be disabled even
        # when qa_records says finalized, and its missing receipt is not a failure.
        snapshot, now = _fresh_snapshot()
        _pause_maintenance(snapshot)
        for key in ("boundary_host_receipt", "boundary_database_heartbeat"):
            snapshot.pop(key)
        def evaluate(reason):
            return live_health.evaluate_snapshot(snapshot, now=now,
                maintenance_pause_reason="operator paused uploads",
                boundary_pause_reason=reason)["health"]
        self.assertEqual(evaluate("")["lifecycle_phase"], "finalized")
        self.assertIn("boundary_host_receipt_missing", evaluate("")["reasons"])
        # Fixture boundary is still enabled+active: paused owner flags the resume.
        paused = evaluate("historical archive hold")
        self.assertIn("boundary_timer_enabled_while_paused", paused["reasons"])
        self.assertIn("boundary_timer_active_while_paused", paused["reasons"])
        self.assertNotIn("boundary_host_receipt_missing", paused["reasons"])
        _pause_boundary(snapshot)
        paused = evaluate("historical archive hold")
        self.assertEqual(paused["status"], "healthy", paused)
        self.assertTrue(paused["boundary_paused"])
        self.assertEqual(paused["boundary_pause_reason"], "historical archive hold")
        snapshot.pop("boundary_systemd")
        self.assertIn("boundary_systemd_missing", evaluate("historical archive hold")["reasons"])

    def test_pause_reasons_come_from_live_host_watch_owner(self):
        owner = json.loads((ROOT / "deploy/tencent/candidate-usw-20261022.json").read_text())
        paused = owner["host_watch"]["paused_jobs"]
        self.assertEqual(live_health.load_maintenance_pause_reason(), paused["tokenkey-qa-maintenance"])
        self.assertEqual(live_health.load_boundary_pause_reason(), paused["tokenkey-qa-boundary"])

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

    def test_paused_uploads_defer_only_the_write_identity_check(self) -> None:
        # Tencent Hybrid has no IMDS role; uploads are paused, so the write identity
        # is deferred to resume. Every other pin fact stays fatal while paused.
        def evaluate(mutation, pause):
            snapshot, now = _fresh_snapshot()
            _pause_maintenance(snapshot)
            _pause_boundary(snapshot)
            storage = snapshot["storage"]
            storage["instance_role_available"] = False
            if mutation == "missing":
                del snapshot["storage"]
            elif mutation == "running":
                storage["runtime_stopped"] = False
            elif mutation == "credentials":
                storage["credentials_file_present"] = True
            elif mutation == "coordinates":
                storage["environment"]["QA_ARCHIVE_STORAGE_BUCKET"] = "other"
            return live_health.evaluate_snapshot(snapshot, now=now,
                maintenance_pause_reason=pause, boundary_pause_reason="historical archive hold")
        paused = evaluate("none", "operator paused uploads")
        self.assertTrue(paused["storage_contract"]["ok"])
        self.assertEqual(paused["storage_contract"]["deferred_until_resume"],
                         ["qa_archive_instance_role_unavailable"])
        self.assertEqual(paused["health"]["status"], "healthy")
        self.assertIn("qa_archive_instance_role_unavailable",
                      evaluate("none", "")["health"]["reasons"])
        for mutation, reason in (("missing", "qa_runtime_storage_unavailable"),
                                 ("running", "qa_runtime_pin_invalid"),
                                 ("credentials", "qa_archive_static_credentials_unexpected"),
                                 ("coordinates", "qa_archive_coordinate_drift:QA_ARCHIVE_STORAGE_BUCKET")):
            with self.subTest(mutation=mutation):
                self.assertIn(reason, evaluate(mutation, "operator paused uploads")["health"]["reasons"])

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
        self.assertTrue(result["iam_contract"]["applicable"])
        self.assertEqual(result["health"]["status"], "healthy")

    def test_paused_upload_defers_only_retired_instance_role_sids(self) -> None:
        snapshot, now = _fresh_snapshot()
        _pause_maintenance(snapshot)
        missing = {
            "ok": False,
            "status": "drift",
            "failures": [
                "missing_app_sid:AllowAppInstanceRoleVerifyRaw",
                "missing_app_sid:AllowAppInstanceRoleWriteRaw",
            ],
        }
        with (
            mock.patch.object(iam_contract, "_account_id", return_value="123456789012"),
            mock.patch.object(iam_contract, "resolve_app_role_arn", return_value="legacy-role"),
            mock.patch.object(iam_contract, "evaluate", return_value=missing),
        ):
            paused = live_health.evaluate_snapshot(
                snapshot, skip_iam=False, now=now,
                maintenance_pause_reason="S3 upload paused",
            )
            live = live_health.evaluate_snapshot(snapshot, skip_iam=False, now=now)
        self.assertFalse(paused["iam_contract"]["applicable"])
        self.assertEqual(paused["iam_contract"]["status"], "deferred")
        self.assertEqual(paused["health"]["status"], "healthy")
        self.assertTrue(live["iam_contract"]["applicable"])
        self.assertFalse(live["iam_contract"]["ok"])

    def test_paused_upload_still_reports_other_iam_drift(self) -> None:
        snapshot, now = _fresh_snapshot()
        _pause_maintenance(snapshot)
        drifted = {
            "ok": False,
            "status": "drift",
            "failures": [
                "missing_app_sid:AllowAppInstanceRoleWriteRaw",
                "AllowAppInstanceRoleWriteRaw:forbidden_action:s3:DeleteObject",
            ],
        }
        with (
            mock.patch.object(iam_contract, "_account_id", return_value="123456789012"),
            mock.patch.object(iam_contract, "resolve_app_role_arn", return_value="legacy-role"),
            mock.patch.object(iam_contract, "evaluate", return_value=drifted),
        ):
            result = live_health.evaluate_snapshot(
                snapshot, skip_iam=False, now=now,
                maintenance_pause_reason="S3 upload paused",
            )
        self.assertTrue(result["iam_contract"]["applicable"])
        self.assertFalse(result["iam_contract"]["ok"])

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
             mock.patch.object(live_health, "load_boundary_pause_reason", return_value=""), \
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


def _summarize_terminal_like_probe_sql(archive: dict, receipt: dict | None) -> dict:
    """Reference model of the probe's terminal summary SQL over a full inventory."""
    items = archive["terminal_failures_after_cutover"]
    by_code: dict[str, int] = {}
    for item in items:
        by_code[item["verification_error_code"]] = by_code.get(item["verification_error_code"], 0) + 1
    targets = set()
    receipt_comp = (receipt or {}).get("compensation")
    if isinstance(receipt_comp, dict) and receipt_comp.get("window_start"):
        targets.add(receipt_comp["window_start"])
    if isinstance(archive.get("compensation"), dict):
        targets.add(archive["compensation"].get("window_start"))
    windows = sorted(item["window_start"] for item in items)
    summary = {
        "count": len(items),
        "by_code": dict(sorted(by_code.items())),
        "first_window_start": windows[0] if windows else None,
        "last_window_start": windows[-1] if windows else None,
        "compensation_matches": [item for item in items if item["window_start"] in targets],
    }
    compact = {key: value for key, value in archive.items() if key != "terminal_failures_after_cutover"}
    compact["terminal_failures_summary"] = summary
    return compact


class TerminalInventorySummaryEquivalenceTest(unittest.TestCase):
    TERMINAL = "source_unavailable_after_retention"

    @staticmethod
    def _windows(count: int) -> list[str]:
        start = dt.datetime(2026, 8, 1, 1, tzinfo=dt.timezone.utc)
        return [(start + dt.timedelta(hours=i)).strftime("%Y-%m-%dT%H:%M:%SZ") for i in range(count)]

    def _scenarios(self):
        windows = self._windows(600)
        comp_window = windows[17]
        missing_window = "2026-07-01T00:00:00Z"
        failed_comp = {
            "state": "failed", "verification_error_code": self.TERMINAL,
            "restore_verified": False, "cleanup_eligible": False, "commit_etag": None,
        }
        heartbeat_comp = (
            " compensation_window={window} compensation_state=failed"
            f" compensation_error_code={self.TERMINAL}"
        )
        inventories = {
            "empty": [],
            "all_terminal": [{"window_start": w, "verification_error_code": self.TERMINAL} for w in windows],
            "mixed_codes": [
                {"window_start": w, "verification_error_code": (
                    "archive_failed" if i == 77 else "restore_failed" if i == 91 else self.TERMINAL
                )}
                for i, w in enumerate(windows)
            ],
            "comp_absent": [
                {"window_start": w, "verification_error_code": self.TERMINAL}
                for w in windows if w != comp_window
            ],
        }
        # receipt compensation window, control compensation window, heartbeat window
        correlations = {
            "no_compensation": (None, None, None),
            "control_only": (None, comp_window, None),
            "control_only_outside_inventory": (None, missing_window, None),
            "receipt_and_control": (comp_window, comp_window, comp_window),
            "receipt_outside_inventory": (missing_window, missing_window, missing_window),
            "receipt_control_disagree": (comp_window, windows[18], comp_window),
            "receipt_without_control": (comp_window, None, windows[19]),
        }
        for inv_name, inventory in inventories.items():
            for corr_name, (receipt_window, control_window, heartbeat_window) in correlations.items():
                snapshot, now = _fresh_snapshot()
                archive = snapshot["archive_control"]
                archive["terminal_failures_after_cutover"] = copy.deepcopy(inventory)
                archive["compensation"] = (
                    {"window_start": control_window, **failed_comp} if control_window else None
                )
                if receipt_window:
                    snapshot["host_receipt"]["compensation"] = {"window_start": receipt_window, **failed_comp}
                if heartbeat_window:
                    snapshot["database_heartbeat"]["last_result"] += heartbeat_comp.format(window=heartbeat_window)
                yield f"{inv_name}/{corr_name}", snapshot, now

    def test_summary_and_full_inventory_give_identical_verdicts(self) -> None:
        verdicts = set()
        for name, snapshot, now in self._scenarios():
            compact = copy.deepcopy(snapshot)
            compact["archive_control"] = _summarize_terminal_like_probe_sql(
                snapshot["archive_control"], snapshot["host_receipt"]
            )
            self.assertNotIn("terminal_failures_after_cutover", compact["archive_control"])
            for policy in ("accepted_terminal", "strict"):
                with self.subTest(scenario=name, policy=policy):
                    legacy = health.evaluate(snapshot, now=now, catchup_gap_policy=policy)
                    summary = health.evaluate(compact, now=now, catchup_gap_policy=policy)
                    self.assertEqual(summary, legacy)
                    verdicts.add((legacy["status"], tuple(legacy["catchup_reasons"])))
        # The scenarios must exercise healthy, degraded, and several failed shapes.
        statuses = {status for status, _ in verdicts}
        self.assertEqual(statuses, {"healthy", "degraded", "failed"}, verdicts)
        reasons = {reason for _, catchup in verdicts for reason in catchup}
        for expected in (
            "catchup_terminal_gaps_present", "terminal_failures_after_cutover", "archive_failed",
            "archive_verification_failure", "compensation_window_not_in_terminal_inventory",
        ):
            self.assertIn(expected, reasons)

    def test_malformed_summary_fails_closed_as_missing_inventory(self) -> None:
        for summary in (
            None, [], {"by_code": {}}, {"count": 2, "by_code": {self.TERMINAL: 1}},
            {"count": 1, "by_code": {self.TERMINAL: True}}, {"count": 0, "by_code": {self.TERMINAL: 0}},
            {"count": -1, "by_code": {}},
        ):
            with self.subTest(summary=summary):
                snapshot, now = _fresh_snapshot()
                archive = snapshot["archive_control"]
                del archive["terminal_failures_after_cutover"]
                if summary is not None:
                    archive["terminal_failures_summary"] = summary
                verdict = health.evaluate(snapshot, now=now)
                self.assertEqual(verdict["status"], "failed", verdict)
                self.assertIn("terminal_failure_inventory_missing", verdict["catchup_reasons"])

    def test_large_inventory_probe_output_stays_under_ssm_inline_limit(self) -> None:
        snapshot, _ = _fresh_snapshot()
        windows = self._windows(5000)
        snapshot["archive_control"]["terminal_failures_after_cutover"] = [
            {"window_start": w, "verification_error_code": self.TERMINAL} for w in windows
        ]
        snapshot["archive_control"]["compensation"] = {
            "window_start": windows[0], "state": "failed", "verification_error_code": self.TERMINAL,
            "restore_verified": False, "cleanup_eligible": False, "commit_etag": None,
        }
        legacy_line = "PHASE2ARCHIVE " + json.dumps(snapshot["archive_control"], sort_keys=True)
        compact = _summarize_terminal_like_probe_sql(snapshot["archive_control"], snapshot["host_receipt"])
        archive_line = "PHASE2ARCHIVE " + json.dumps(compact, sort_keys=True)
        self.assertGreater(len(legacy_line), 24_000)
        self.assertLess(len(archive_line), 2_000)
        probe_text = "\n".join(
            [
                "PHASE2SYSTEMD " + json.dumps(snapshot["systemd"], sort_keys=True),
                "PHASE2STORAGE " + json.dumps(snapshot["storage"], sort_keys=True),
                "PHASE2RECEIPT " + json.dumps(snapshot["host_receipt"], sort_keys=True),
                "PHASE2HEARTBEAT " + json.dumps(snapshot["database_heartbeat"], sort_keys=True),
                archive_line,
                "PHASE2BOUNDARYSYSTEMD " + json.dumps(snapshot["boundary_systemd"], sort_keys=True),
                "PHASE2BOUNDARYRECEIPT " + json.dumps(snapshot["boundary_host_receipt"], sort_keys=True),
                "PHASE2BOUNDARYHEARTBEAT " + json.dumps(snapshot["boundary_database_heartbeat"], sort_keys=True),
                "PHASE2QARECORDS " + json.dumps(snapshot["qa_records"], sort_keys=True),
            ]
        )
        # run-probe.sh refuses output SSM truncated at 24,000 bytes.
        self.assertLess(len(probe_text.encode()), 23_000)
        parsed = live_health._parse_probe_output(probe_text)
        self.assertEqual(parsed["archive_control"]["terminal_failures_summary"]["count"], 5000)

    def test_direct_ssm_delivery_inlines_container_resolver(self) -> None:
        body = live_health.probe_script_bytes().decode()
        self.assertLess(body.index("tk_resolve_postgres_container() {"), body.index("PHASE2STORAGE"))
        self.assertNotIn(" tokenkey-postgres psql", body)


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
