#!/usr/bin/env python3
"""Unit tests for wave_b_freeze_delta.py (no live DB / SSM)."""

from __future__ import annotations

import json
import subprocess
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "ops" / "stage0" / "wave_b_freeze_delta.py"

# Ensure importable when run as unittest module path.
sys.path.insert(0, str(ROOT / "ops" / "stage0"))
import wave_b_freeze_delta as delta  # noqa: E402


class WaveBFreezeDeltaTests(unittest.TestCase):
    def test_watermark_sql_mentions_dedup(self) -> None:
        sql = delta.watermark_sql()
        self.assertIn("usage_billing_dedup", sql)
        self.assertIn("dedup_max_id", sql)

    def test_truncate_requires_tables(self) -> None:
        with self.assertRaises(ValueError):
            delta.truncate_oltp_sql([])

    def test_truncate_uses_replica_role_not_cascade(self) -> None:
        sql = delta.truncate_oltp_sql(["accounts", "users"])
        self.assertNotIn("CASCADE", sql)
        self.assertIn("SET session_replication_role = replica;", sql)
        self.assertIn('TRUNCATE "accounts", "users" RESTART IDENTITY;', sql)
        self.assertIn("SET session_replication_role = DEFAULT;", sql)

    def test_pg_dump_exclude_covers_logs_and_dedup(self) -> None:
        args = delta.pg_dump_exclude_table_data_args()
        self.assertIn("--exclude-table-data=usage_logs*", args)
        self.assertIn("--exclude-table-data=usage_billing_dedup", args)
        joined = " ".join(args)
        out = subprocess.check_output(
            [sys.executable, str(SCRIPT), "print-pg-dump-exclude-args"],
            text=True,
        ).strip()
        self.assertEqual(out, joined)

    def test_dedup_copy_uses_watermark(self) -> None:
        sql = delta.dedup_delta_copy_sql(14615286)
        self.assertIn("id > 14615286", sql)
        self.assertIn("COPY", sql)

    def test_dedup_apply_on_conflict(self) -> None:
        sql = delta.dedup_delta_apply_sql()
        self.assertIn("ON CONFLICT (request_id, api_key_id) DO NOTHING", sql)
        self.assertIn("COPY _wave_b_dedup_delta FROM STDIN", sql)

    def test_plan_json_roundtrip_cli(self) -> None:
        wm = {
            "dedup_max_id": 100,
            "dedup_count": 90,
            "accounts": 215,
            "users": 63,
            "api_keys": 451,
            "settings": 318,
            "aws_dedup_count": 7500 + 90,
        }
        out = subprocess.check_output(
            [
                sys.executable,
                str(SCRIPT),
                "plan",
                "--watermark-json",
                json.dumps(wm),
                "--oltp-tables",
                "accounts,users,settings",
            ],
            text=True,
        )
        plan = json.loads(out)
        self.assertEqual(plan["mode"], "wave_b_compress_freeze")
        self.assertEqual(plan["oltp_table_count"], 3)
        trunc = plan["sql"]["truncate_oltp"]
        self.assertIn('TRUNCATE "accounts", "users", "settings"', trunc)
        self.assertNotIn("CASCADE", trunc)
        self.assertIn("session_replication_role = replica", trunc)
        self.assertIn("id > 100", plan["sql"]["dedup_delta_copy"])
        self.assertEqual(plan["estimate"]["dedup_delta_rows"], 7500)
        self.assertIn("--exclude-table-data=usage_billing_dedup", plan["pg_dump_exclude_table_data"])

    def test_estimate_cli(self) -> None:
        out = subprocess.check_output(
            [sys.executable, str(SCRIPT), "estimate", "--dedup-delta-rows", "7500"],
            text=True,
        )
        est = json.loads(out)
        self.assertEqual(est["dedup_delta_rows"], 7500)
        self.assertLess(est["user_visible_minutes_estimate"], 10)


if __name__ == "__main__":
    unittest.main()
