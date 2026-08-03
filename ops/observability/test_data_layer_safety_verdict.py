#!/usr/bin/env python3
"""Behavior tests for data-layer safety findings."""

from __future__ import annotations

import datetime as dt
import pathlib
import subprocess
import sys
import unittest


_DIR = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(_DIR))

import data_layer_safety_verdict as verdict  # noqa: E402


NOW = dt.datetime(2026, 8, 3, 12, 0, tzinfo=dt.timezone.utc)


def _signals() -> dict:
    return {
        "PARTITIONSTATS": {
            "server_clock": NOW.isoformat(),
            "ops_error_logs_current_covered": True,
            "ops_error_logs_future_covered": True,
            "ops_system_logs_current_covered": True,
            "ops_system_logs_future_covered": True,
            "usage_logs_current_covered": True,
            "usage_logs_future_covered": True,
            "partition_maintenance_last_success_at": (NOW - dt.timedelta(hours=1)).isoformat(),
            "partition_maintenance_last_error_at": None,
        },
        "BACKUPSTATS": {"latest_pgdump_at": (NOW - dt.timedelta(hours=1)).isoformat()},
        "SNAPSHOTSTATS": {"latest_snapshot_at": (NOW - dt.timedelta(hours=12)).isoformat()},
        "ARCHIVESTATS": {
            "ledgers": [
                {
                    "table": table,
                    "legacy_upper_exclusive": "2026-07-01T00:00:00Z",
                    "final_cutoff_exclusive": "2026-07-01T00:00:00Z",
                    "more_cold_rows_remaining": False,
                }
                for table in ("ops_error_logs", "ops_system_logs")
            ],
            "hold_started_at": (NOW - dt.timedelta(days=1)).isoformat(),
            "closeout_complete": True,
            "restore_verified_at": [
                (NOW - dt.timedelta(days=1)).isoformat(),
                (NOW - dt.timedelta(days=1)).isoformat(),
            ],
        },
    }


class DataLayerSafetyVerdictTest(unittest.TestCase):
    def test_all_gates_green(self) -> None:
        result = verdict.compute_verdict(_signals())
        self.assertEqual(result["verdict"], "green")
        self.assertEqual(result["findings"], [])

    def test_capacity_independent_failures_are_separate_findings(self) -> None:
        signals = _signals()
        signals["PARTITIONSTATS"]["usage_logs_future_covered"] = False
        signals["BACKUPSTATS"]["latest_pgdump_at"] = (NOW - dt.timedelta(hours=3)).isoformat()
        signals["ARCHIVESTATS"]["ledgers"][0]["more_cold_rows_remaining"] = True
        result = verdict.compute_verdict(signals)
        self.assertEqual(result["verdict"], "unsafe")
        self.assertEqual(
            {finding["kind"] for finding in result["findings"]},
            {"partition_coverage", "pgdump_freshness", "archive_lag"},
        )

    def test_missing_restore_and_stale_hold_fail_closed(self) -> None:
        signals = _signals()
        signals["ARCHIVESTATS"]["closeout_complete"] = False
        signals["ARCHIVESTATS"]["hold_started_at"] = (NOW - dt.timedelta(days=20)).isoformat()
        signals["ARCHIVESTATS"]["restore_verified_at"] = []
        result = verdict.compute_verdict(signals)
        kinds = {finding["kind"] for finding in result["findings"]}
        self.assertIn("cleanup_hold_stale", kinds)
        self.assertIn("archive_restore_proof", kinds)

    def test_archive_evidence_validation_error_is_independent(self) -> None:
        signals = _signals()
        signals["ARCHIVESTATS"]["evidence_errors"] = [
            "ops_error_logs:closeout_binding"
        ]
        result = verdict.compute_verdict(signals)
        self.assertEqual(result["verdict"], "unsafe")
        self.assertIn(
            "archive_evidence",
            {finding["kind"] for finding in result["findings"]},
        )

    def test_latest_partition_failure_is_immediate_and_duplicate_ledgers_fail(self) -> None:
        signals = _signals()
        signals["PARTITIONSTATS"]["partition_maintenance_last_error_at"] = (
            NOW - dt.timedelta(minutes=1)
        ).isoformat()
        signals["ARCHIVESTATS"]["ledgers"][1]["table"] = "ops_error_logs"
        result = verdict.compute_verdict(signals)
        kinds = {finding["kind"] for finding in result["findings"]}
        self.assertIn("partition_maintenance_error", kinds)
        self.assertIn("archive_lag", kinds)

    def test_future_dated_freshness_evidence_fails_closed(self) -> None:
        cases = (
            ("heartbeat", "PARTITIONSTATS", "partition_maintenance_last_success_at", "partition_maintenance_heartbeat"),
            ("pgdump", "BACKUPSTATS", "latest_pgdump_at", "pgdump_freshness"),
            ("snapshot", "SNAPSHOTSTATS", "latest_snapshot_at", "ebs_snapshot_freshness"),
        )
        for name, section, field, expected_kind in cases:
            with self.subTest(name=name):
                signals = _signals()
                signals[section][field] = (
                    NOW + verdict.MAX_FUTURE_SKEW + dt.timedelta(seconds=1)
                ).isoformat()
                kinds = {
                    finding["kind"]
                    for finding in verdict.compute_verdict(signals)["findings"]
                }
                self.assertIn(expected_kind, kinds)

        signals = _signals()
        signals["ARCHIVESTATS"]["restore_verified_at"][0] = (
            NOW + verdict.MAX_FUTURE_SKEW + dt.timedelta(seconds=1)
        ).isoformat()
        kinds = {
            finding["kind"]
            for finding in verdict.compute_verdict(signals)["findings"]
        }
        self.assertIn("archive_restore_proof", kinds)

    def test_probe_shell_parses(self) -> None:
        proc = subprocess.run(
            ["bash", "-n", str(_DIR / "probe-data-layer-safety.sh")],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)

    def test_probe_uses_legacy_partition_bound_not_table_existence(self) -> None:
        probe = (_DIR / "probe-data-layer-safety.sh").read_text(encoding="utf-8")
        self.assertIn("pg_get_expr(child.relpartbound, child.oid)", probe)
        self.assertIn("FROM named_partitions", probe)
        self.assertNotIn("OR to_regclass('usage_logs_legacy') IS NOT NULL", probe)
        self.assertNotIn("OR to_regclass('ops_error_logs_legacy') IS NOT NULL", probe)


if __name__ == "__main__":
    unittest.main(verbosity=2)
