#!/usr/bin/env python3
from __future__ import annotations

import unittest
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))

from ops.observability.pgdump_restore_canary_alert import build_decision


def valid_receipt(target: str = "edge:us3") -> dict:
    return {
        "schema_version": 2,
        "mode": "pgdump_restore_canary",
        "target": target,
        "completed_at": "2026-08-18T08:00:00Z",
        "source_s3_uri": "s3://bucket/edge/us3/pgdump/tokenkey-20260818T070000Z.sql.gz",
        "source_last_modified": "2026-08-18T07:02:00Z",
        "compressed_bytes": 100,
        "uncompressed_bytes": 1000,
        "required_free_bytes": 1073743924,
        "observed_free_bytes": 5000000000,
        "artifact_sha256": "a" * 64,
        "restore_image": "postgres:18-alpine",
        "live_counts": {
            "users": 5,
            "accounts": 8,
            "api_keys": 7,
            "groups": 3,
            "settings": 11,
            "usage_billing_dedup": 14,
        },
        "restored_counts": {
            "users": 3,
            "accounts": 5,
            "api_keys": 4,
            "groups": 2,
            "settings": 8,
            "usage_billing_dedup": 11,
        },
        "cleanup_verified": True,
        "source_mutated": False,
        "deletion_authorized": False,
    }


class PgdumpRestoreCanaryAlertTest(unittest.TestCase):
    def test_direct_script_entrypoint_loads_shared_receipt_contract(self) -> None:
        completed = subprocess.run(
            [sys.executable, str(ROOT / "ops/observability/pgdump_restore_canary_alert.py"), "--help"],
            cwd=ROOT,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertNotIn("ModuleNotFoundError", completed.stderr)

    def test_first_failure_fires(self) -> None:
        decision = build_decision("edge:us3", "failure", None, "", "https://run/1")
        self.assertTrue(decision["should_alert"])
        self.assertIn(":firing:job-failure", decision["key"])
        self.assertIn("edge:us3", decision["message"])

    def test_repeated_identical_failure_is_quiet(self) -> None:
        first = build_decision("edge:us3", "failure", None, "", "https://run/1")
        repeated = build_decision("edge:us3", "failure", None, first["key"], "https://run/2")
        self.assertFalse(repeated["should_alert"])
        self.assertEqual(repeated["key"], first["key"])

    def test_first_recovery_fires_and_steady_healthy_is_quiet(self) -> None:
        failure = build_decision("edge:us3", "failure", None, "", "https://run/1")
        recovery = build_decision("edge:us3", "success", valid_receipt(), failure["key"], "https://run/2")
        self.assertTrue(recovery["should_alert"])
        self.assertIn(":healthy", recovery["key"])
        self.assertIn("恢复", recovery["message"])
        steady = build_decision("edge:us3", "success", valid_receipt(), recovery["key"], "https://run/3")
        self.assertFalse(steady["should_alert"])

    def test_first_observed_success_does_not_send_recovery(self) -> None:
        decision = build_decision("edge:us3", "success", valid_receipt(), "", "https://run/1")
        self.assertFalse(decision["should_alert"])
        self.assertIn(":healthy", decision["key"])

    def test_malformed_receipt_is_firing(self) -> None:
        decision = build_decision("edge:us3", "success", {"mode": "wrong"}, "", "https://run/1")
        self.assertTrue(decision["should_alert"])
        self.assertIn(":firing:invalid-receipt", decision["key"])

    def test_recovery_requires_same_complete_receipt_contract_as_diagnostics(self) -> None:
        receipt = valid_receipt()
        receipt.pop("required_free_bytes")
        decision = build_decision(
            "edge:us3",
            "success",
            receipt,
            "pgdump:edge:us3:firing:job-failure",
            "https://run/2",
        )
        self.assertTrue(decision["should_alert"])
        self.assertIn(":firing:invalid-receipt", decision["key"])
        self.assertNotIn("已恢复", decision["message"])

    def test_recovery_refuses_receipt_missing_a_precious_table_count(self) -> None:
        receipt = valid_receipt()
        receipt["restored_counts"].pop("accounts")

        decision = build_decision(
            "edge:us3",
            "success",
            receipt,
            "pgdump:edge:us3:firing:job-failure",
            "https://run/2",
        )

        self.assertTrue(decision["should_alert"])
        self.assertIn(":firing:invalid-receipt", decision["key"])
        self.assertNotIn("已恢复", decision["message"])

    def test_missing_receipt_is_firing(self) -> None:
        decision = build_decision("edge:us3", "success", None, "", "https://run/1")
        self.assertTrue(decision["should_alert"])
        self.assertIn(":firing:missing-receipt", decision["key"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
