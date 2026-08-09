#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[2]
GATE = ROOT / "ops/qa/qa_archive_recovery_gate.py"
BREAK_GLASS = ROOT / "ops/prod/fetch-qa-dump.sh"


class QAArchiveRecoveryGateTest(unittest.TestCase):
    def run_gate(self, evidence: pathlib.Path) -> tuple[subprocess.CompletedProcess[str], dict[str, object]]:
        proc = subprocess.run(
            [sys.executable, str(GATE), "plan-retirement", "--evidence", str(evidence)],
            capture_output=True,
            text=True,
            check=False,
        )
        payload = json.loads(proc.stdout) if proc.stdout else {}
        return proc, payload

    def test_us045_missing_recovery_evidence_preserves_break_glass_path(self) -> None:
        before = BREAK_GLASS.read_bytes()
        with tempfile.TemporaryDirectory() as temp_dir:
            proc, payload = self.run_gate(pathlib.Path(temp_dir) / "missing.json")
        self.assertNotEqual(proc.returncode, 0)
        self.assertFalse(payload["planned_transition_authorized"])
        self.assertEqual(payload["script_action"], "preserve")
        self.assertEqual(BREAK_GLASS.read_bytes(), before)

    def test_us045_mismatched_recovery_evidence_preserves_break_glass_path(self) -> None:
        before = BREAK_GLASS.read_bytes()
        with tempfile.TemporaryDirectory() as temp_dir:
            evidence = pathlib.Path(temp_dir) / "evidence.json"
            evidence.write_text(json.dumps(self.valid_evidence(restore_role="arn:aws:iam::123456789012:role/other")), encoding="utf-8")
            proc, payload = self.run_gate(evidence)
        self.assertNotEqual(proc.returncode, 0)
        self.assertFalse(payload["planned_transition_authorized"])
        self.assertEqual(payload["script_action"], "preserve")
        self.assertEqual(BREAK_GLASS.read_bytes(), before)

    def test_us045_verified_synthetic_evidence_authorizes_only_planned_transition(self) -> None:
        before = BREAK_GLASS.read_bytes()
        with tempfile.TemporaryDirectory() as temp_dir:
            evidence = pathlib.Path(temp_dir) / "evidence.json"
            evidence.write_text(json.dumps(self.valid_evidence()), encoding="utf-8")
            proc, payload = self.run_gate(evidence)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertTrue(payload["planned_transition_authorized"])
        self.assertFalse(payload["production_success_claimed"])
        self.assertEqual(payload["script_action"], "planned_removal_only")
        self.assertEqual(BREAK_GLASS.read_bytes(), before)

    @staticmethod
    def valid_evidence(restore_role: str = "arn:aws:iam::123456789012:role/recovery") -> dict[str, object]:
        base = {
            "ok": True, "verified": True, "database_accessed": False,
            "source": "ops-workstation-s3", "window_start": "2026-08-07T21:00:00Z",
            "bucket": "tokenkey-prod-qa-raw-archive-123456789012",
            "recovery_role_arn": "arn:aws:iam::123456789012:role/recovery",
        }
        return {
            "schema_version": 1, "scope": "synthetic", "source": "ops-workstation-s3",
            "inspect": dict(base), "verify": dict(base),
            "restore": {**base, "recovery_role_arn": restore_role, "privacy_confirmed": True},
        }


if __name__ == "__main__":
    unittest.main()
