#!/usr/bin/env python3
"""Regression checks for QA Phase 1 closeout and Phase 2 baseline artifacts."""
from __future__ import annotations

import datetime as dt
import importlib.util
import json
import subprocess
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def _load_closeout_module():
    path = ROOT / "ops/qa/prod_qa_archive_closeout.py"
    spec = importlib.util.spec_from_file_location("prod_qa_archive_closeout", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load closeout controller")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


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

    def test_qa_maintenance_host_script_is_archive_only(self) -> None:
        body = (ROOT / "deploy/aws/stage0/tokenkey-qa-maintenance.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("--qa-maintenance-once", body)
        self.assertIn("tokenkey-prod-qa-maintenance-v1", body)
        self.assertIn("--install-units", body)
        self.assertNotIn("DELETE FROM qa_records", body)

    def test_release_images_include_qa_archive_binary(self) -> None:
        for rel in ("Dockerfile", "deploy/Dockerfile", "backend/Dockerfile"):
            body = (ROOT / rel).read_text(encoding="utf-8")
            self.assertIn("./cmd/qa-archive", body, rel)
            self.assertIn("/app/qa-archive", body, rel)

    def test_qa_archive_closeout_controller_is_fail_closed(self) -> None:
        module = _load_closeout_module()
        window = module._parse_window("2026-08-07T01:00:00Z")
        self.assertEqual(
            module._window_token(module.REPAIR_CONFIRMATION_PREFIX, window),
            "tokenkey-prod-qa-archive-repair-v1:2026-08-07T01:00:00Z",
        )
        proof = module._safety_proof(
            window, dt.datetime(2026, 8, 7, 2, 0, tzinfo=dt.timezone.utc)
        )
        self.assertNotIn("=", proof)
        guard = "\n".join(module._timer_guard_shell())
        remote = module._remote_command(
            "repair-apply",
            window,
            "",
            module._window_token(module.REPAIR_CONFIRMATION_PREFIX, window),
        )
        self.assertIn('"$proof"', remote)
        for needle in (
            "tokenkey-qa-maintenance.timer",
            "tokenkey-qa-stale-cleanup.timer",
            "disabled:inactive",
            "cleanup_enabled=false",
            "ops:cleanup:leader",
        ):
            self.assertIn(needle, guard)

    def test_qa_archive_closeout_rejects_incomplete_receipt(self) -> None:
        module = _load_closeout_module()
        window = module._parse_window("2026-08-07T01:00:00Z")
        receipt = {
            "ok": True,
            "command": "repair-apply",
            "window_start": "2026-08-07T01:00:00Z",
            "cleanup_eligible": False,
            "deletion_authorized": False,
            "cleanup_hold_active": True,
            "maintenance_timer_disabled": True,
            "maintenance_timer_inactive": True,
            "stale_cleanup_timer_disabled": True,
            "stale_cleanup_timer_inactive": True,
            "cleanup_runtime_disabled": True,
        }
        with self.assertRaises(module.QAArchiveCloseoutError):
            module._validate_receipt(json.dumps(receipt), "repair-apply", window)

    def test_qa_archive_closeout_restore_path_cannot_escape_root(self) -> None:
        module = _load_closeout_module()
        with self.assertRaises(module.QAArchiveCloseoutError):
            module._validate_restore_output("/app/data/qa_archive_restore/../escaped")

    def test_qa_maintenance_ops_scripts_compile(self) -> None:
        for rel in (
            "ops/qa/prod_qa_maintenance.py",
            "ops/qa/prod_qa_archive_backfill.py",
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
