#!/usr/bin/env python3
from __future__ import annotations

import datetime as dt
import json
import pathlib
import sys
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))

from ops.observability.pgdump_restore_canary_verdict import evaluate_receipt

NOW = dt.datetime(2026, 8, 18, 8, 0, tzinfo=dt.timezone.utc)


def valid_receipt() -> dict:
    return {
        "schema_version": 2,
        "mode": "pgdump_restore_canary",
        "target": "edge:us3",
        "completed_at": "2026-08-11T09:00:00Z",
        "source_s3_uri": "s3://bucket/edge/us3/pgdump/tokenkey-20260811T070000Z.sql.gz",
        "source_last_modified": "2026-08-11T07:02:00Z",
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


class PgdumpRestoreCanaryVerdictTest(unittest.TestCase):
    def evaluate(self, raw: str) -> dict:
        return evaluate_receipt(raw, "edge:us3", "us3", now=NOW)

    def test_fresh_complete_receipt_is_healthy(self) -> None:
        finding = self.evaluate(json.dumps(valid_receipt()))
        self.assertEqual(finding["status"], "ok")
        self.assertEqual(finding["kind"], "pgdump_restore_canary")

    def test_healed_from_stale_dump_receipt_is_warning_not_missing(self) -> None:
        receipt = valid_receipt()
        receipt["healed_from_stale_dump"] = True
        finding = self.evaluate(json.dumps(receipt))
        self.assertEqual(finding["status"], "warning")
        self.assertEqual(finding["severity"], "warning")
        self.assertIn("self-healed", finding["summary"])
        self.assertIn("tokenkey-pgdump.timer", finding["summary"])

    def test_missing_receipt_is_actionable(self) -> None:
        finding = self.evaluate("")
        self.assertEqual(finding["status"], "issue_candidate")
        self.assertIn("missing", finding["summary"])

    def test_malformed_receipt_is_actionable(self) -> None:
        finding = self.evaluate("{broken")
        self.assertEqual(finding["status"], "issue_candidate")
        self.assertIn("malformed", finding["summary"])

    def test_stale_receipt_is_actionable(self) -> None:
        receipt = valid_receipt()
        receipt["completed_at"] = "2026-08-09T07:00:00Z"
        finding = self.evaluate(json.dumps(receipt))
        self.assertEqual(finding["status"], "issue_candidate")
        self.assertIn("stale", finding["summary"])

    def test_wrong_target_is_actionable(self) -> None:
        receipt = valid_receipt()
        receipt["target"] = "prod"
        finding = self.evaluate(json.dumps(receipt))
        self.assertEqual(finding["status"], "issue_candidate")
        self.assertIn("wrong target", finding["summary"])

    def test_incomplete_capacity_or_cleanup_evidence_is_actionable(self) -> None:
        for field in ("required_free_bytes", "cleanup_verified", "source_s3_uri"):
            with self.subTest(field=field):
                receipt = valid_receipt()
                receipt.pop(field)
                finding = self.evaluate(json.dumps(receipt))
                self.assertEqual(finding["status"], "issue_candidate")
                self.assertIn("incomplete", finding["summary"])

    def test_missing_precious_table_count_is_actionable(self) -> None:
        receipt = valid_receipt()
        receipt["restored_counts"].pop("accounts")

        finding = self.evaluate(json.dumps(receipt))

        self.assertEqual(finding["status"], "issue_candidate")
        self.assertIn("invalid evidence", finding["summary"])

    def test_invalid_precious_table_count_is_actionable(self) -> None:
        receipt = valid_receipt()
        receipt["live_counts"]["usage_billing_dedup"] = -1

        finding = self.evaluate(json.dumps(receipt))

        self.assertEqual(finding["status"], "issue_candidate")
        self.assertIn("invalid evidence", finding["summary"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
