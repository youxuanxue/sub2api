#!/usr/bin/env python3
from __future__ import annotations

import hashlib
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[2]
GATE = ROOT / "ops/qa/qa_archive_recovery_gate.py"
RETIRED_BREAK_GLASS = ROOT / "ops" / "prod" / "fetch-qa-dump.sh"


class QAArchiveRecoveryGateTest(unittest.TestCase):
    def run_gate(
        self,
        evidence: pathlib.Path,
        *extra_args: str,
    ) -> tuple[subprocess.CompletedProcess[str], dict[str, object]]:
        proc = subprocess.run(
            [sys.executable, str(GATE), "plan-retirement", "--evidence", str(evidence), *extra_args],
            capture_output=True,
            text=True,
            check=False,
        )
        payload = json.loads(proc.stdout) if proc.stdout else {}
        return proc, payload

    def test_us045_break_glass_script_is_retired(self) -> None:
        self.assertFalse(RETIRED_BREAK_GLASS.exists())

    def test_us045_missing_recovery_evidence_rejects_without_authorizing_transition(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            proc, payload = self.run_gate(pathlib.Path(temp_dir) / "missing.json")
        self.assertNotEqual(proc.returncode, 0)
        self.assertFalse(payload["planned_transition_authorized"])
        self.assertEqual(payload["script_action"], "preserve")
        self.assertEqual(payload["break_glass_state"], "retired")

    def test_us045_mismatched_recovery_evidence_rejects_without_authorizing_transition(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            evidence = pathlib.Path(temp_dir) / "evidence.json"
            evidence.write_text(json.dumps(self.valid_evidence(restore_role="arn:aws:iam::123456789012:role/other")), encoding="utf-8")
            proc, payload = self.run_gate(evidence)
        self.assertNotEqual(proc.returncode, 0)
        self.assertFalse(payload["planned_transition_authorized"])
        self.assertEqual(payload["script_action"], "preserve")
        self.assertEqual(payload["break_glass_state"], "retired")

    def test_us045_verified_synthetic_evidence_authorizes_only_planned_transition(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            evidence = pathlib.Path(temp_dir) / "evidence.json"
            evidence.write_text(json.dumps(self.valid_evidence()), encoding="utf-8")
            proc, payload = self.run_gate(evidence)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertTrue(payload["planned_transition_authorized"])
        self.assertFalse(payload["production_success_claimed"])
        self.assertEqual(payload["script_action"], "planned_removal_only")
        self.assertEqual(payload["break_glass_state"], "retired")

    def test_us045_relabeled_synthetic_evidence_cannot_claim_production_success(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            evidence = pathlib.Path(temp_dir) / "evidence.json"
            payload = self.valid_evidence()
            payload["scope"] = "production"
            evidence.write_text(json.dumps(payload), encoding="utf-8")
            proc, result = self.run_gate(evidence)
        self.assertNotEqual(proc.returncode, 0)
        self.assertFalse(result["production_success_claimed"])
        self.assertFalse(result["planned_transition_authorized"])
        self.assertEqual(result["script_action"], "preserve")

    def test_us045_copied_command_receipts_cannot_be_production_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            evidence = pathlib.Path(temp_dir) / "evidence.json"
            payload = self.valid_evidence()
            payload["scope"] = "production"
            payload["verify"] = dict(payload["inspect"])
            payload["restore"] = {**payload["inspect"], "privacy_confirmed": True}
            evidence.write_text(json.dumps(payload), encoding="utf-8")
            approval = pathlib.Path(temp_dir) / "approval.json"
            approval.write_text(json.dumps(self.production_approval(evidence)), encoding="utf-8")
            proc, result = self.run_gate(
                evidence,
                "--production-approval", str(approval),
                "--expected-window-start", "2026-08-07T21:00:00Z",
                "--expected-bucket", "tokenkey-prod-qa-raw-archive-123456789012",
                "--expected-recovery-role-arn", "arn:aws:iam::123456789012:role/recovery",
            )
        self.assertNotEqual(proc.returncode, 0)
        self.assertFalse(result["production_success_claimed"])
        self.assertEqual(result["script_action"], "preserve")

    def test_us045_production_evidence_requires_hash_bound_human_approval(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            evidence = pathlib.Path(temp_dir) / "evidence.json"
            payload = self.valid_evidence()
            payload["scope"] = "production"
            evidence.write_text(json.dumps(payload), encoding="utf-8")
            proc, result = self.run_gate(
                evidence,
                "--expected-window-start", "2026-08-07T21:00:00Z",
                "--expected-bucket", "tokenkey-prod-qa-raw-archive-123456789012",
                "--expected-recovery-role-arn", "arn:aws:iam::123456789012:role/recovery",
            )
        self.assertNotEqual(proc.returncode, 0)
        self.assertFalse(result["production_success_claimed"])
        self.assertFalse(result["planned_transition_authorized"])
        self.assertEqual(result["script_action"], "preserve")

    def test_us045_stale_production_receipts_cannot_authorize_retirement(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            evidence = pathlib.Path(temp_dir) / "evidence.json"
            payload = self.valid_evidence()
            payload["scope"] = "production"
            for command in ("inspect", "verify", "restore"):
                payload[command]["captured_at"] = "2020-01-01T00:00:00Z"
            evidence.write_text(json.dumps(payload), encoding="utf-8")
            approval = pathlib.Path(temp_dir) / "approval.json"
            approval.write_text(json.dumps(self.production_approval(evidence)), encoding="utf-8")
            proc, result = self.run_gate(
                evidence,
                "--production-approval", str(approval),
                "--expected-window-start", "2026-08-07T21:00:00Z",
                "--expected-bucket", "tokenkey-prod-qa-raw-archive-123456789012",
                "--expected-recovery-role-arn", "arn:aws:iam::123456789012:role/recovery",
            )
        self.assertNotEqual(proc.returncode, 0)
        self.assertFalse(result["planned_transition_authorized"])
        self.assertFalse(result["production_success_claimed"])
        self.assertEqual(result["script_action"], "preserve")

    @staticmethod
    def valid_evidence(restore_role: str = "arn:aws:iam::123456789012:role/recovery") -> dict[str, object]:
        base = {
            "ok": True, "verified": True, "database_accessed": False,
            "source": "ops-workstation-s3", "window_start": "2026-08-07T21:00:00Z",
            "bucket": "tokenkey-prod-qa-raw-archive-123456789012",
            "recovery_role_arn": "arn:aws:iam::123456789012:role/recovery",
            "recovery_run_id": "recovery-20260807T210000Z",
        }
        return {
            "schema_version": 1, "scope": "synthetic", "source": "ops-workstation-s3",
            "inspect": {**base, "command": "inspect", "receipt_id": "inspect-receipt"},
            "verify": {**base, "command": "verify", "receipt_id": "verify-receipt"},
            "restore": {
                **base, "command": "restore", "receipt_id": "restore-receipt",
                "recovery_role_arn": restore_role, "privacy_confirmed": True,
            },
        }

    @staticmethod
    def production_approval(evidence: pathlib.Path) -> dict[str, object]:
        return {
            "schema_version": 1,
            "approval_kind": "tokenkey-prod-qa-archive-retirement-v1",
            "approval_source": "human-high-risk-gate",
            "approved_by": "qa-operator",
            "approved_at": "2026-08-09T00:00:00Z",
            "expires_at": "2099-01-01T00:00:00Z",
            "evidence_sha256": hashlib.sha256(evidence.read_bytes()).hexdigest(),
            "expected_window_start": "2026-08-07T21:00:00Z",
            "expected_bucket": "tokenkey-prod-qa-raw-archive-123456789012",
            "expected_recovery_role_arn": "arn:aws:iam::123456789012:role/recovery",
        }


if __name__ == "__main__":
    unittest.main()
