#!/usr/bin/env python3
from __future__ import annotations

import datetime as dt
import importlib.util
import json
import pathlib
import tempfile
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("prod_qa_stale_cleanup", HERE / "prod_qa_stale_cleanup.py")
assert SPEC is not None and SPEC.loader is not None
control = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(control)

INSTANCE = "i-0123456789abcdef0"
CUTOFF = "2026-08-06T12:00:00.000000Z"
CONFIRM = "tokenkey-prod-qa-retention-apply-v1:" + CUTOFF
MARKER_SHA = "a" * 64


def plan(clock: str | None = None) -> dict:
    return {
        "mode": "prod_data_retention_activation_plan",
        "environment": "prod",
        "instance_id": INSTANCE,
        "activation_ready": True,
        "deletion_authorized": False,
        "ops": {"server_clock": clock or dt.datetime.now(dt.timezone.utc).isoformat()},
        "qa": {
            "mode": "prod_qa_age_retention_plan",
            "cutoff": CUTOFF,
            "active_image": "ghcr.io/youxuanxue/sub2api:1.8.140",
            "candidate_rows": 42,
            "candidate_blob_files": 3,
            "candidate_dlq_files": 1,
            "required_confirmation": CONFIRM,
            "deletion_authorized": False,
        },
    }


class ProdQAStaleCleanupTest(unittest.TestCase):
    def test_wrong_confirmation_is_rejected_before_ssm(self) -> None:
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(control, "_run_remote") as remote:
            path = pathlib.Path(temp) / "plan.json"
            path.write_text(json.dumps(plan()), encoding="utf-8")
            with self.assertRaisesRegex(control.StaleCleanupError, "confirmation"):
                control.apply_first(path, pathlib.Path(temp) / "receipt.json", "wrong")
        remote.assert_not_called()

    def test_existing_receipt_is_rejected_before_ssm(self) -> None:
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(control, "_run_remote") as remote:
            root = pathlib.Path(temp)
            path = root / "plan.json"
            receipt = root / "receipt.json"
            path.write_text(json.dumps(plan()), encoding="utf-8")
            receipt.write_text("{}\n", encoding="utf-8")
            with self.assertRaisesRegex(control.StaleCleanupError, "already exists"):
                control.apply_first(path, receipt, CONFIRM)
        remote.assert_not_called()

    def test_stale_plan_can_only_use_host_guarded_resume_mode(self) -> None:
        old_clock = (dt.datetime.now(dt.timezone.utc) - dt.timedelta(hours=1)).isoformat()
        remote_receipt = {
            "mode": "prod_qa_age_retention_first_apply",
            "cutoff": CUTOFF,
            "applied_at": "2026-08-07T12:01:00.000000Z",
            "authorization_expires_at": "2026-08-07T12:11:00.000000Z",
            "planned_rows": 42,
            "deleted_rows_this_attempt": 1,
            "planned_blob_files": 3,
            "planned_dlq_files": 1,
            "remaining_rows": 0,
            "remaining_blob_files": 0,
            "remaining_dlq_files": 0,
            "marker_sha256": MARKER_SHA,
            "deletion_authorized": True,
        }
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(
            control, "_run_remote", return_value=("ssm-1", remote_receipt)
        ) as remote:
            root = pathlib.Path(temp)
            path = root / "plan.json"
            path.write_text(json.dumps(plan(old_clock)), encoding="utf-8")
            with self.assertRaisesRegex(control.StaleCleanupError, "stale"):
                control.apply_first(path, root / "wrong-mode.json", CONFIRM)
            control.apply_first(path, root / "receipt.json", CONFIRM, resume=True)
        self.assertIn("--resume-first", remote.call_args.args[1])
        self.assertNotIn("--apply-first", remote.call_args.args[1])

    def test_future_plan_is_rejected_before_ssm(self) -> None:
        future_clock = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(minutes=1)).isoformat()
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(control, "_run_remote") as remote:
            root = pathlib.Path(temp)
            path = root / "plan.json"
            path.write_text(json.dumps(plan(future_clock)), encoding="utf-8")
            with self.assertRaisesRegex(control.StaleCleanupError, "future"):
                control.apply_first(path, root / "receipt.json", CONFIRM)
        remote.assert_not_called()

    def test_incomplete_remote_cleanup_never_persists_receipt(self) -> None:
        remote_receipt = {
            "mode": "prod_qa_age_retention_first_apply",
            "cutoff": CUTOFF,
            "applied_at": "2026-08-07T12:01:00.000000Z",
            "authorization_expires_at": "2026-08-07T12:11:00.000000Z",
            "planned_rows": 42,
            "deleted_rows_this_attempt": 41,
            "planned_blob_files": 3,
            "planned_dlq_files": 1,
            "remaining_rows": 1,
            "remaining_blob_files": 0,
            "remaining_dlq_files": 0,
            "marker_sha256": MARKER_SHA,
            "deletion_authorized": True,
        }
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(
            control, "_run_remote", return_value=("ssm-1", remote_receipt)
        ):
            root = pathlib.Path(temp)
            path = root / "plan.json"
            output = root / "receipt.json"
            path.write_text(json.dumps(plan()), encoding="utf-8")
            with self.assertRaisesRegex(control.StaleCleanupError, "failed validation"):
                control.apply_first(path, output, CONFIRM)
            self.assertFalse(output.exists())

    def test_apply_delivers_exact_bound_arguments_and_persists_receipt(self) -> None:
        receipt = {
            "mode": "prod_qa_age_retention_first_apply",
            "cutoff": CUTOFF,
            "applied_at": "2026-08-07T12:01:00.000000Z",
            "authorization_expires_at": "2026-08-07T12:11:00.000000Z",
            "planned_rows": 42,
            "deleted_rows_this_attempt": 42,
            "planned_blob_files": 3,
            "planned_dlq_files": 1,
            "remaining_rows": 0,
            "remaining_blob_files": 0,
            "remaining_dlq_files": 0,
            "marker_sha256": MARKER_SHA,
            "deletion_authorized": True,
        }
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(
            control, "_run_remote", return_value=("ssm-1", receipt)
        ) as remote:
            root = pathlib.Path(temp)
            path = root / "plan.json"
            output = root / "receipt.json"
            path.write_text(json.dumps(plan()), encoding="utf-8")
            result = control.apply_first(path, output, CONFIRM)
            persisted = json.loads(output.read_text(encoding="utf-8"))
        self.assertEqual(result, persisted)
        self.assertEqual(result["instance_id"], INSTANCE)
        args = remote.call_args.args
        self.assertEqual(args[0], INSTANCE)
        command = args[1]
        for expected in (CUTOFF, "--expected-rows 42", "--expected-blob-files 3", "--expected-dlq-files 1", "1.8.140", CONFIRM):
            self.assertIn(expected, command)


if __name__ == "__main__":
    unittest.main(verbosity=2)
