#!/usr/bin/env python3
"""Regression checks for QA Phase 1 closeout and Phase 2 baseline artifacts."""
from __future__ import annotations

import subprocess
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


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

    def test_raw_archive_cfn_grants_kms_to_bucket_principals(self) -> None:
        body = (ROOT / "deploy/aws/cloudformation/stage0-qa-raw-archive.yaml").read_text(
            encoding="utf-8"
        )
        self.assertIn("AllowAppInstanceRoleUseViaS3", body)
        self.assertIn("kms:GenerateDataKey*", body)
        self.assertIn("kms:ViaService", body)
        self.assertIn("AllowOpsRecoveryRoleReadViaS3", body)


if __name__ == "__main__":
    raise SystemExit(unittest.main())
