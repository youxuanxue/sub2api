#!/usr/bin/env python3
from __future__ import annotations

import unittest
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))

from ops.observability.pgdump_restore_canary_alert import apply_key_decision, build_decision


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
        self.assertIn("--deliver", completed.stdout)

    def test_deliver_cli_writes_key_file(self) -> None:
        import json
        import tempfile

        decision = build_decision("prod", "failure", None, "", "https://run/9")
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            decision_path = root / "decision.json"
            key_file = root / "key"
            decision_path.write_text(json.dumps(decision), encoding="utf-8")
            completed = subprocess.run(
                [
                    sys.executable,
                    str(ROOT / "ops/observability/pgdump_restore_canary_alert.py"),
                    "--deliver",
                    "--decision",
                    str(decision_path),
                    "--key-file",
                    str(key_file),
                    "--dry-run",
                ],
                cwd=ROOT,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertIn("dry-run", completed.stdout)
            self.assertFalse(key_file.exists())

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

    def test_deliver_persists_canary_key_without_edge_health_state(self) -> None:
        import tempfile

        decision = build_decision("edge:us3", "failure", None, "", "https://run/1")
        with tempfile.TemporaryDirectory() as raw:
            key_file = pathlib.Path(raw) / "key"
            result = apply_key_decision(
                decision,
                key_file=key_file,
                dry_run=True,
            )
            self.assertEqual(result, "dry-run")
            self.assertFalse(key_file.exists())
            result = apply_key_decision(
                {**decision, "should_alert": False},
                key_file=key_file,
            )
            self.assertEqual(result, "unchanged")
            self.assertEqual(key_file.read_text(encoding="utf-8"), decision["key"] + "\n")

    def test_deliver_rejects_edge_health_state_shape(self) -> None:
        import tempfile

        with tempfile.TemporaryDirectory() as raw:
            key_file = pathlib.Path(raw) / "key"
            with self.assertRaisesRegex(Exception, "key"):
                apply_key_decision(
                    {
                        "schema_version": 1,
                        "should_alert": False,
                        "state": {"schema_version": 1},
                        "message": "",
                    },
                    key_file=key_file,
                )


if __name__ == "__main__":
    unittest.main(verbosity=2)
