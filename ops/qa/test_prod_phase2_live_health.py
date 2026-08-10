#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import unittest
import datetime as dt
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "ops" / "qa"))

import prod_phase2_live_health as live_health  # noqa: E402
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
    finished = now - dt.timedelta(seconds=30)
    started = finished - dt.timedelta(seconds=30)
    iso = lambda value: value.strftime("%Y-%m-%dT%H:%M:%SZ")
    snapshot["systemd"]["finished_at"] = iso(finished)
    snapshot["host_receipt"]["started_at"] = iso(started)
    snapshot["host_receipt"]["finished_at"] = iso(finished)
    snapshot["database_heartbeat"]["last_run_at"] = iso(started)
    snapshot["database_heartbeat"]["last_success_at"] = iso(finished)
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
                "PHASE2QARECORDS " + json.dumps({"partition_owner": "default_only"}, sort_keys=True),
            ]
        )

    def test_parse_probe_output_builds_snapshot(self) -> None:
        snapshot = live_health._parse_probe_output(self._healthy_probe_text())
        self.assertIn("systemd", snapshot)
        self.assertIn("host_receipt", snapshot)
        self.assertEqual(snapshot["qa_records"]["partition_owner"], "default_only")

    def test_evaluate_snapshot_marks_default_only_partition_warning(self) -> None:
        snapshot, now = _fresh_snapshot()
        snapshot["qa_records"] = {"partition_owner": "default_only"}
        payload = live_health.evaluate_snapshot(snapshot, skip_iam=True, now=now)
        self.assertIn("qa_records_partition_owner_default_only", payload["warnings"])
        self.assertEqual(payload["health"]["status"], "healthy")

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


class VerifyRawArchiveIAMContractTest(unittest.TestCase):
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
        self.assertIn("AllowAppInstanceRoleList:forbidden_action:s3:ListBucket", verdict["failures"])

    def test_accepts_suffix_scoped_app_role(self) -> None:
        role = "arn:aws:iam::123456789012:role/app"
        bucket = "tokenkey-prod-qa-raw-archive-123456789012"
        suffix_resources = [
            f"arn:aws:s3:::{bucket}/raw/v1/date=*/hour=*/{suffix}"
            for suffix in iam_contract.EXPECTED_SUFFIXES
        ]
        statements = [
            {
                "Sid": "AllowAppInstanceRoleWriteRaw",
                "Principal": {"AWS": role},
                "Action": ["s3:PutObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"],
                "Resource": suffix_resources,
            },
            {
                "Sid": "AllowAppInstanceRoleVerifyRaw",
                "Principal": {"AWS": role},
                "Action": "s3:GetObject",
                "Resource": suffix_resources,
            },
        ]
        verdict = iam_contract.evaluate(bucket=bucket, app_role_arn=role, statements=statements)
        self.assertTrue(verdict["ok"], verdict)


if __name__ == "__main__":
    raise SystemExit(unittest.main())
