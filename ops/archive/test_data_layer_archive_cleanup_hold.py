#!/usr/bin/env python3
"""Behavior tests for the production archive cleanup hold."""

from __future__ import annotations

import copy
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


_DIR = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(_DIR))

import data_layer_archive_cleanup_hold as control  # noqa: E402
import data_layer_archive_cleanup_hold_remote as remote  # noqa: E402


_INSTANCE_ID = "i-0123456789abcdef0"
_STARTED_AT = "2026-07-21T14:30:00.000000Z"


def _settings(enabled: bool) -> dict[str, object]:
    return {
        "data_retention": {
            "cleanup_enabled": enabled,
            "cleanup_schedule": "0 2 * * *",
            "error_log_retention_days": 30,
            "minute_metrics_retention_days": 30,
            "hourly_metrics_retention_days": 30,
        },
        "aggregation": {"aggregation_enabled": True},
        "ignore_context_canceled": True,
    }


def _state(
    enabled: bool, *, last_run_at: str | None = "2026-07-21T02:00:00Z"
) -> dict[str, object]:
    settings = _settings(enabled)
    return {
        "server_clock": "2026-07-21T23:59:00.000000Z",
        "database_cleanup_enabled": enabled,
        "api_cleanup_enabled": enabled,
        "cleanup_lock_active": False,
        "last_cleanup_run_at": last_run_at,
        "last_cleanup_success_at": last_run_at,
        "last_cleanup_error_at": None,
        "last_cleanup_result": "error_logs=0 system_logs=0",
        "settings_sha256": remote._sha256(settings),
        "_admin_key": "secret-not-for-output",
        "_settings": settings,
    }


class CleanupHoldRemoteTest(unittest.TestCase):
    def test_us039_plan_is_read_only_and_sanitized(self) -> None:
        with mock.patch.object(remote, "_read_state", return_value=_state(True)):
            result = remote.plan()
        self.assertFalse(result["hold_active"])
        self.assertFalse(result["settings_mutated"])
        self.assertFalse(result["deletion_authorized"])
        self.assertNotIn("_admin_key", result)
        self.assertNotIn("_settings", result)

    def test_us039_apply_preserves_full_document_and_proves_reload(self) -> None:
        before = _state(True)
        after = _state(False)
        put_bodies: list[dict[str, object]] = []

        def admin_request(
            method: str, _key: str, body: dict[str, object] | None = None
        ) -> dict[str, object]:
            self.assertEqual(method, "PUT")
            assert body is not None
            put_bodies.append(copy.deepcopy(body))
            return body

        with mock.patch.object(
            remote, "_read_state", side_effect=[before, after]
        ), mock.patch.object(
            remote, "_admin_request", side_effect=admin_request
        ), mock.patch.object(remote, "_reload_proof", return_value=True):
            result = remote.apply_hold(remote.HOLD_CONFIRMATION)

        expected = _settings(True)
        expected["data_retention"]["cleanup_enabled"] = False
        self.assertEqual(put_bodies, [expected])
        self.assertTrue(result["hold_active"])
        self.assertTrue(result["reload_proven"])
        self.assertTrue(result["previous_cleanup_enabled"])
        self.assertFalse(result["deletion_authorized"])
        self.assertNotIn("secret-not-for-output", json.dumps(result))

    def test_us039_apply_refuses_without_runtime_reload_proof(self) -> None:
        with mock.patch.object(
            remote, "_read_state", side_effect=[_state(True), _state(False)]
        ), mock.patch.object(
            remote, "_admin_request", return_value=_settings(False)
        ), mock.patch.object(remote, "_reload_proof", return_value=False):
            with self.assertRaisesRegex(remote.HoldError, "runtime disable was not proven"):
                remote.apply_hold(remote.HOLD_CONFIRMATION)

    def test_us039_apply_refuses_to_replace_an_active_hold_receipt(self) -> None:
        with mock.patch.object(
            remote, "_read_state", return_value=_state(False)
        ), mock.patch.object(remote, "_admin_request") as admin_request:
            with self.assertRaisesRegex(remote.HoldError, "already active"):
                remote.apply_hold(remote.HOLD_CONFIRMATION)
        admin_request.assert_not_called()

    def test_us039_verify_rejects_cleanup_after_hold(self) -> None:
        state = _state(False, last_run_at="2026-07-21T14:30:01Z")
        with mock.patch.object(remote, "_read_state", return_value=state):
            with self.assertRaisesRegex(remote.HoldError, "cleanup ran after"):
                remote.verify_hold(_STARTED_AT)

    def test_us039_verify_rejects_an_inflight_cleanup_lock(self) -> None:
        state = _state(False)
        state["cleanup_lock_active"] = True
        with mock.patch.object(remote, "_read_state", return_value=state):
            with self.assertRaisesRegex(remote.HoldError, "not active"):
                remote.verify_hold(_STARTED_AT)

    def test_us039_verify_requires_current_runtime_disable_proof(self) -> None:
        with mock.patch.object(
            remote, "_read_state", return_value=_state(False)
        ), mock.patch.object(remote, "_runtime_disabled_since", return_value=False):
            with self.assertRaisesRegex(remote.HoldError, "runtime disable"):
                remote.verify_hold(_STARTED_AT)

    def test_us039_release_restores_only_the_previous_enabled_state(self) -> None:
        before = _state(False)
        with mock.patch.object(
            remote, "_read_state", return_value=before
        ), mock.patch.object(
            remote, "_runtime_disabled_since", return_value=True
        ), mock.patch.object(remote, "_admin_request") as admin_request:
            result = remote.release_hold(
                remote.RELEASE_CONFIRMATION,
                previous_cleanup_enabled=False,
                hold_started_at=_STARTED_AT,
            )
        admin_request.assert_not_called()
        self.assertFalse(result["restored_cleanup_enabled"])
        self.assertFalse(result["settings_mutated"])

        put_bodies: list[dict[str, object]] = []

        def admin_request(
            method: str, _key: str, body: dict[str, object] | None = None
        ) -> dict[str, object]:
            self.assertEqual(method, "PUT")
            assert body is not None
            put_bodies.append(copy.deepcopy(body))
            return body

        with mock.patch.object(
            remote, "_read_state", side_effect=[before, _state(True)]
        ), mock.patch.object(
            remote, "_runtime_disabled_since", return_value=True
        ), mock.patch.object(
            remote, "_admin_request", side_effect=admin_request
        ), mock.patch.object(remote, "_reload_proof", return_value=True):
            result = remote.release_hold(
                remote.RELEASE_CONFIRMATION,
                previous_cleanup_enabled=True,
                hold_started_at=_STARTED_AT,
            )
        expected = _settings(False)
        expected["data_retention"]["cleanup_enabled"] = True
        self.assertEqual(put_bodies, [expected])
        self.assertTrue(result["restored_cleanup_enabled"])
        self.assertTrue(result["reload_proven"])

    def test_us039_release_rejects_a_stale_hold_receipt(self) -> None:
        stale = _state(False, last_run_at="2026-07-21T14:30:01Z")
        with mock.patch.object(
            remote, "_read_state", return_value=stale
        ), mock.patch.object(remote, "_admin_request") as admin_request:
            with self.assertRaisesRegex(remote.HoldError, "cleanup ran after"):
                remote.release_hold(
                    remote.RELEASE_CONFIRMATION,
                    previous_cleanup_enabled=True,
                    hold_started_at=_STARTED_AT,
                )
        admin_request.assert_not_called()


class CleanupHoldControlTest(unittest.TestCase):
    def test_us039_apply_writes_and_reverifies_receipt(self) -> None:
        applied = {
            "mode": "prod_archive_cleanup_hold",
            "environment": "prod",
            "instance_id": _INSTANCE_ID,
            "hold_active": True,
            "reload_proven": True,
            "hold_started_at": _STARTED_AT,
            "previous_cleanup_enabled": True,
            "deletion_authorized": False,
        }
        verified = {
            "mode": "prod_archive_cleanup_hold_verify",
            "environment": "prod",
            "instance_id": _INSTANCE_ID,
            "hold_active": True,
            "no_cleanup_after_hold": True,
            "runtime_disabled_proven": True,
            "server_clock": "2026-07-21T14:32:00.000000Z",
            "deletion_authorized": False,
        }
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(
            control, "_run_remote", side_effect=[applied, verified]
        ):
            receipt_path = pathlib.Path(temp) / "hold.json"
            result = control.apply(receipt_path, control.HOLD_CONFIRMATION)
            persisted = json.loads(receipt_path.read_text(encoding="utf-8"))
        self.assertEqual(result, persisted)
        self.assertEqual(result["verified_at"], verified["server_clock"])

    def test_us039_receipt_is_bound_to_the_prod_instance(self) -> None:
        receipt = {
            "mode": "prod_archive_cleanup_hold",
            "environment": "prod",
            "instance_id": _INSTANCE_ID,
            "hold_active": True,
            "reload_proven": True,
            "hold_started_at": _STARTED_AT,
            "deletion_authorized": False,
        }
        with tempfile.TemporaryDirectory() as temp:
            path = pathlib.Path(temp) / "hold.json"
            path.write_text(json.dumps(receipt), encoding="utf-8")
            with self.assertRaisesRegex(control.HoldControlError, "different"):
                control.verify_receipt_for_instance(path, "i-fffffffffffffffff")

    def test_us039_release_passes_the_receipt_hold_identity(self) -> None:
        receipt = {
            "mode": "prod_archive_cleanup_hold",
            "environment": "prod",
            "instance_id": _INSTANCE_ID,
            "hold_active": True,
            "reload_proven": True,
            "hold_started_at": _STARTED_AT,
            "previous_cleanup_enabled": True,
            "deletion_authorized": False,
        }
        released = {
            "mode": "prod_archive_cleanup_hold_release",
            "instance_id": _INSTANCE_ID,
            "deletion_authorized": False,
        }
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(
            control, "_run_remote", return_value=released
        ) as run_remote:
            path = pathlib.Path(temp) / "hold.json"
            path.write_text(json.dumps(receipt), encoding="utf-8")
            control.release(path, control.RELEASE_CONFIRMATION)
        self.assertEqual(
            run_remote.call_args.args,
            (
                "release",
                [
                    "--confirm",
                    control.RELEASE_CONFIRMATION,
                    "--previous-cleanup-enabled",
                    "true",
                    "--hold-started-at",
                    _STARTED_AT,
                ],
            ),
        )

    def test_us039_run_remote_fixes_prod_target_and_companion(self) -> None:
        payload = {
            "mode": "prod_archive_cleanup_hold_plan",
            "deletion_authorized": False,
        }
        completed = subprocess.CompletedProcess(
            args=[],
            returncode=0,
            stdout=json.dumps(payload),
            stderr=(
                "[run-probe] resolved region=us-east-1 "
                f"instance_id={_INSTANCE_ID}\n"
            ),
        )
        with mock.patch.object(control.subprocess, "run", return_value=completed) as run:
            result = control._run_remote("plan", [])
        command = run.call_args.args[0]
        self.assertEqual(command[command.index("--target") + 1], "prod")
        self.assertEqual(command[command.index("--with") + 1], str(control.REMOTE))
        self.assertEqual(result["instance_id"], _INSTANCE_ID)


if __name__ == "__main__":
    unittest.main(verbosity=2)
