#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import hashlib
import io
import json
import gzip
import base64
import pathlib
import sys
import tempfile
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "prod_qa_archive_closeout", ROOT / "ops/qa/prod_qa_archive_closeout.py"
)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("cannot load prod_qa_archive_closeout")
closeout = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(closeout)


class ProdQAGapDecisionOperatorTest(unittest.TestCase):
    @staticmethod
    def _canonical_hash(plan: dict[str, object]) -> str:
        canonical = dict(plan)
        canonical["plan_hash"] = ""
        encoded = json.dumps(
            canonical, ensure_ascii=True, sort_keys=True, separators=(",", ":")
        ).encode()
        return hashlib.sha256(encoded).hexdigest()

    @classmethod
    def _valid_plan(cls, plan_hash: str | None = None) -> dict[str, object]:
        plan: dict[str, object] = {
            "schema_version": "qa-archive-gap-decision-v1",
            "db_utc_anchor": "2026-08-12T05:00:00Z",
            "retention_cutoff": "2026-08-11T05:00:00Z",
            "forward_cutover": {
                "window_start": "2026-08-07T21:00:00Z",
                "window_end": "2026-08-07T22:00:00Z",
            },
            "latest_normal_window_start": "2026-08-12T04:00:00Z",
            "region": "us-east-1",
            "bucket": "tokenkey-prod-qa-raw-archive-123456789012",
            "recovery_role_arn": "arn:aws:iam::123456789012:role/tokenkey-prod-qa-raw-recovery",
            "recovery_run_id": "recovery-head-batch-20260812T050000Z",
            "windows": [
                {
                    "window_start": "2026-08-08T23:00:00Z",
                    "window_end": "2026-08-09T00:00:00Z",
                    "commit_key": "raw/v1/date=2026-08-08/hour=23/commit.json",
                    "commit_exists": False,
                    "source_record_count": 0,
                    "control": {
                        "exists": False,
                        "window_end": "0001-01-01T00:00:00Z",
                        "updated_at": "0001-01-01T00:00:00Z",
                        "segment_fingerprint": "",
                        "has_commit_ready_segment": False,
                    },
                }
            ],
            "plan_hash": "",
        }
        plan["plan_hash"] = plan_hash if plan_hash is not None else cls._canonical_hash(plan)
        return plan

    def test_gap_plan_binds_db_recovery_facts_and_writes_secure_atomic_plan(self) -> None:
        db_plan = {
            "schema_version": "qa-archive-gap-decision-v1",
            "db_utc_anchor": "2026-08-12T05:00:00Z",
            "retention_cutoff": "2026-08-11T05:00:00Z",
            "forward_cutover": {
                "window_start": "2026-08-07T21:00:00Z",
                "window_end": "2026-08-07T22:00:00Z",
            },
            "latest_normal_window_start": "2026-08-12T04:00:00Z",
            "region": "",
            "bucket": "",
            "recovery_role_arn": "",
            "recovery_run_id": "",
            "windows": [{"window_start": "2026-08-08T23:00:00Z"}],
            "plan_hash": "",
        }
        final_plan = self._valid_plan()
        with tempfile.TemporaryDirectory() as temp_dir:
            output = pathlib.Path(temp_dir) / "gap-plan.json"
            with (
                mock.patch.object(closeout, "_resolve_instance", return_value="i-0123456789abcdef0"),
                mock.patch.object(
                    closeout,
                    "_resolve_recovery_scope",
                    return_value=(
                        "tokenkey-prod-qa-raw-archive-123456789012",
                        "arn:aws:iam::123456789012:role/recovery",
                    ),
                ),
                mock.patch.object(closeout, "_run_gap_db_plan", return_value=db_plan) as db_owner,
                mock.patch.object(
                    closeout, "_run_gap_s3_plan", return_value=final_plan
                ) as s3_owner,
            ):
                result = closeout.build_gap_decision_plan(
                    output,
                    qa_archive_bin="/tmp/qa-archive-v1.8.148",
                    recovery_run_id="gap-20260812T060000Z",
                )

            self.assertEqual(result["plan_hash"], final_plan["plan_hash"])
            self.assertEqual(json.loads(output.read_text(encoding="utf-8")), final_plan)
            self.assertEqual(output.stat().st_mode & 0o777, 0o600)
            db_owner.assert_called_once_with("i-0123456789abcdef0")
            s3_owner.assert_called_once_with(
                "/tmp/qa-archive-v1.8.148",
                db_plan,
                "tokenkey-prod-qa-raw-archive-123456789012",
                "arn:aws:iam::123456789012:role/recovery",
                "gap-20260812T060000Z",
            )

    def test_gap_plan_hash_matches_go_operator_vector(self) -> None:
        self.assertEqual(
            self._valid_plan()["plan_hash"],
            "c81fc61fe234f8364f59e09d5121f1389a6507a4cc755d4aae7182f4f252ab21",
        )

    def test_gap_apply_rejects_wrong_confirmation_before_remote_execution(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            plan = pathlib.Path(temp_dir) / "plan.json"
            plan.write_text(
                json.dumps(
                    {
                        "schema_version": "qa-archive-gap-decision-v1",
                        "plan_hash": "b" * 64,
                        "windows": [{"window_start": "2026-08-08T23:00:00Z"}],
                    }
                ),
                encoding="utf-8",
            )
            with mock.patch.object(closeout, "_aws_json") as aws_json:
                with self.assertRaisesRegex(
                    closeout.QAArchiveCloseoutError, "confirmation"
                ):
                    closeout.apply_gap_decision_plan(
                        plan,
                        pathlib.Path(temp_dir) / "receipt.json",
                        confirmation="wrong",
                        approved_by="feng",
                    )
            aws_json.assert_not_called()

    def test_gap_apply_rejects_noncanonical_hash_before_remote_execution(self) -> None:
        plan_value = self._valid_plan("e" * 64)
        with tempfile.TemporaryDirectory() as temp_dir:
            root = pathlib.Path(temp_dir)
            plan = root / "plan.json"
            plan.write_text(json.dumps(plan_value), encoding="utf-8")
            with (
                mock.patch.object(closeout, "_resolve_instance") as resolve_instance,
                mock.patch.object(closeout, "_run_gap_apply") as remote_apply,
            ):
                with self.assertRaisesRegex(
                    closeout.QAArchiveCloseoutError, "canonical hash"
                ):
                    closeout.apply_gap_decision_plan(
                        plan,
                        root / "receipt.json",
                        confirmation=closeout.GAP_CONFIRMATION_PREFIX + "e" * 64,
                        approved_by="feng",
                    )
            resolve_instance.assert_not_called()
            remote_apply.assert_not_called()

    def test_gap_apply_persists_validated_remote_receipt(self) -> None:
        plan_value = self._valid_plan()
        plan_hash = str(plan_value["plan_hash"])
        remote_receipt = {
            "ok": True,
            "command": "gap-decision-apply",
            "plan_hash": plan_hash,
            "approved_by": "feng",
            "window_count": 1,
            "already_applied": False,
            "cleanup_eligible": False,
            "deletion_authorized": False,
        }
        with tempfile.TemporaryDirectory() as temp_dir:
            root = pathlib.Path(temp_dir)
            plan = root / "plan.json"
            receipt = root / "receipt.json"
            plan.write_text(json.dumps(plan_value), encoding="utf-8")
            with (
                mock.patch.object(closeout, "_resolve_instance", return_value="i-0123456789abcdef0"),
                mock.patch.object(
                    closeout,
                    "_run_gap_apply",
                    return_value={"command_id": "cmd-1", "remote_receipt": remote_receipt},
                ) as remote,
            ):
                result = closeout.apply_gap_decision_plan(
                    plan,
                    receipt,
                    confirmation=closeout.GAP_CONFIRMATION_PREFIX + plan_hash,
                    approved_by="feng",
                )

            self.assertEqual(result["remote_receipt"], remote_receipt)
            self.assertEqual(json.loads(receipt.read_text(encoding="utf-8")), result)
            self.assertEqual(receipt.stat().st_mode & 0o777, 0o600)
            remote.assert_called_once_with(
                "i-0123456789abcdef0", plan_value, plan_hash, "feng"
            )

    def test_gap_db_plan_decodes_bounded_gzip_receipt(self) -> None:
        db_plan = self._valid_plan()
        db_plan["region"] = ""
        db_plan["bucket"] = ""
        db_plan["recovery_role_arn"] = ""
        db_plan["recovery_run_id"] = ""
        db_plan["plan_hash"] = ""
        raw = json.dumps(db_plan, separators=(",", ":")).encode()
        receipt = {
            "ok": True,
            "command": "gap-decision-db-plan",
            "plan_gzip_base64": base64.b64encode(gzip.compress(raw)).decode(),
            "plan_uncompressed_bytes": len(raw),
            "window_count": 1,
            "cleanup_eligible": False,
            "deletion_authorized": False,
        }
        with mock.patch.object(
            closeout,
            "_send_remote",
            return_value={"command_id": "cmd-1", "stdout": json.dumps(receipt)},
        ):
            self.assertEqual(closeout._run_gap_db_plan("i-0123456789abcdef0"), db_plan)

    def test_gap_apply_refuses_oversized_ssm_transport_before_remote_call(self) -> None:
        plan_value = self._valid_plan()
        with mock.patch.object(
            closeout, "_encode_plan_transport", return_value="x" * 20001
        ), mock.patch.object(closeout, "_send_remote") as remote:
            with self.assertRaisesRegex(closeout.QAArchiveCloseoutError, "SSM transport"):
                closeout._run_gap_apply(
                    "i-0123456789abcdef0",
                    plan_value,
                    str(plan_value["plan_hash"]),
                    "feng",
                )
        remote.assert_not_called()

    def test_gap_apply_accepts_same_hash_replay_with_stored_approver(self) -> None:
        plan_value = self._valid_plan()
        plan_hash = str(plan_value["plan_hash"])
        remote_receipt = {
            "ok": True,
            "command": "gap-decision-apply",
            "plan_hash": plan_hash,
            "approved_by": "original-approver",
            "window_count": 1,
            "already_applied": True,
            "cleanup_eligible": False,
            "deletion_authorized": False,
        }
        with mock.patch.object(
            closeout,
            "_send_remote",
            return_value={"command_id": "cmd-replay", "stdout": json.dumps(remote_receipt)},
        ):
            result = closeout._run_gap_apply(
                "i-0123456789abcdef0", plan_value, plan_hash, "retrying-operator"
            )
        self.assertEqual(result["remote_receipt"], remote_receipt)

    def test_main_routes_gap_plan_and_apply_without_legacy_window_flags(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = pathlib.Path(temp_dir)
            with (
                mock.patch.object(
                    closeout,
                    "build_gap_decision_plan",
                    return_value=self._valid_plan("d" * 64),
                ) as build,
                mock.patch.object(sys, "argv", [
                    "prod_qa_archive_closeout.py", "gap-plan",
                    "--output", str(root / "plan.json"),
                    "--qa-archive-bin", "/tmp/qa-archive-v1.8.148",
                    "--recovery-run-id", "gap-20260812T060000Z",
                ]),
                mock.patch("sys.stdout", new_callable=io.StringIO),
            ):
                self.assertEqual(closeout.main(), 0)
            build.assert_called_once_with(
                root / "plan.json",
                qa_archive_bin="/tmp/qa-archive-v1.8.148",
                recovery_run_id="gap-20260812T060000Z",
            )

            with (
                mock.patch.object(
                    closeout,
                    "apply_gap_decision_plan",
                    return_value={"plan_hash": "d" * 64},
                ) as apply,
                mock.patch.object(sys, "argv", [
                    "prod_qa_archive_closeout.py", "gap-apply",
                    "--plan", str(root / "plan.json"),
                    "--receipt-output", str(root / "receipt.json"),
                    "--confirm", closeout.GAP_CONFIRMATION_PREFIX + "d" * 64,
                    "--approved-by", "feng",
                ]),
                mock.patch("sys.stdout", new_callable=io.StringIO),
            ):
                self.assertEqual(closeout.main(), 0)
            apply.assert_called_once_with(
                root / "plan.json", root / "receipt.json",
                confirmation=closeout.GAP_CONFIRMATION_PREFIX + "d" * 64,
                approved_by="feng",
            )


if __name__ == "__main__":
    raise SystemExit(unittest.main())
