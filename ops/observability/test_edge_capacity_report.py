#!/usr/bin/env python3

from __future__ import annotations

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

try:
    from ops.observability import edge_capacity_probe as PROBE
    from ops.observability import edge_capacity_report as REPORT
except ModuleNotFoundError:
    import edge_capacity_probe as PROBE
    import edge_capacity_report as REPORT


def _events(*intervals: tuple[int, int]) -> list[tuple[int, int]]:
    output: list[tuple[int, int]] = []
    for start_ms, end_ms in intervals:
        output.extend(((start_ms, 1), (end_ms, -1)))
    return output


def _metric(value: int, *, peak: int | None = None) -> dict[str, object]:
    return {
        "peak": peak if peak is not None else value,
        "observed": value,
        "repeated": value,
        "cross_day": value,
    }


def _account(
    account_id: int,
    platform: str,
    cross_day: int,
    *,
    channel_type: int = 0,
    h_value: int | None = None,
) -> dict[str, object]:
    f_metric = _metric(cross_day, peak=max(30, cross_day))
    h_metric = _metric(h_value if h_value is not None else cross_day)
    return {
        "account_id": account_id,
        "platform": platform,
        "channel_type": channel_type,
        "configured_concurrency": 50,
        "coverage": {
            "usage_total": 100,
            "usage_matched": 100,
            "usage_match_pct": 100.0,
            "invalid_access_rows": 0,
            "invalid_usage_rows": 0,
        },
        "sources": {
            "F": {"pristine": f_metric},
            "H": {"pristine": h_metric},
        },
    }


def _document(edge: str, accounts: list[dict[str, object]]) -> dict[str, object]:
    return {
        "schema_version": PROBE.SCHEMA_VERSION,
        "edge": edge,
        "meta": {
            "snapshot_at": "2026-07-20T09:55:00.000000+00:00",
            "db_now_utc": "2026-07-20 10:00:00",
            "requested_days": 60,
            "error_retention_days": 30,
            "analysis_days": 30,
            "min_sustain_seconds": 60,
            "settlement_lag_seconds": PROBE.SETTLEMENT_LAG_SECONDS,
            "access_min_utc": "2026-06-20 00:00:00",
            "runtime_sampling_enabled": False,
        },
        "accounts": accounts,
    }


class SummarizeRunsTest(unittest.TestCase):
    def test_three_independent_runs_across_days_are_repeatable(self) -> None:
        day_ms = 86_400_000
        events = _events(
            (0, 60_000),
            (0, 60_000),
            (120_000, 180_000),
            (120_000, 180_000),
            (day_ms, day_ms + 60_000),
            (day_ms, day_ms + 60_000),
        )

        result = PROBE.summarize_runs(events, [], 60)

        self.assertEqual(result["peak"], 2)
        self.assertEqual(result["observed"], 2)
        self.assertEqual(result["repeated"], 2)
        self.assertEqual(result["cross_day"], 2)

    def test_one_long_episode_is_not_three_repetitions(self) -> None:
        events = _events((0, 180_000), (0, 180_000))

        result = PROBE.summarize_runs(events, [], 60)

        self.assertEqual(result["observed"], 2)
        self.assertEqual(result["repeated"], 0)
        self.assertEqual(result["cross_day"], 0)

    def test_long_request_qualifies_without_minute_events(self) -> None:
        result = PROBE.summarize_runs(_events((0, 61_000)), [], 60)

        self.assertEqual(result["observed"], 1)

    def test_error_point_splits_an_otherwise_qualifying_episode(self) -> None:
        result = PROBE.summarize_runs(
            _events((0, 100_000)),
            [(50_000, 50_001)],
            60,
        )

        self.assertEqual(result["observed"], 0)

    def test_equal_timestamp_churn_keeps_threshold_continuous(self) -> None:
        result = PROBE.summarize_runs(
            _events((0, 120_000), (60_000, 180_000)),
            [],
            60,
        )

        self.assertEqual(result["observed"], 2)

    def test_peak_excludes_unsafe_concurrency(self) -> None:
        result = PROBE.summarize_runs(
            _events((0, 120_000), (30_000, 90_000)),
            [(30_000, 90_000)],
            60,
        )

        self.assertEqual(result["peak"], 1)


class CollectionContractTest(unittest.TestCase):
    def test_queries_use_bounded_watermark_and_drop_ambiguous_f(self) -> None:
        snapshot = "2026-07-20T10:00:00.000000+00:00"

        error_sql = PROBE._error_sql(30, snapshot)
        event_sql = PROBE._event_sql(30, snapshot)
        meta_sql = PROBE._meta_sql(60, 30, 30, 60)

        self.assertIn(f"TIMESTAMPTZ '{snapshot}'", error_sql)
        self.assertIn(f"TIMESTAMPTZ '{snapshot}'", event_sql)
        self.assertIn("WHERE rn=1 AND candidate_count=1", error_sql)
        self.assertIn(
            "min(p.match_rank) OVER (PARTITION BY p.error_id)", error_sql
        )
        self.assertIn("WHERE p.match_rank=p.best_match_rank", error_sql)
        self.assertIn("e.key_kind='client' OR e.logged_account_id IS NULL", error_sql)
        self.assertIn("h.account_id=e.logged_account_id", error_sql)
        self.assertIn("SELECT * FROM selected WHERE candidate_count=1", event_sql)
        self.assertIn("GREATEST(", event_sql)
        self.assertIn("SELECT lower_ms FROM bounds", event_sql)
        self.assertNotIn("now()", error_sql)
        self.assertNotIn("now()", event_sql)
        self.assertIn(
            f"make_interval(secs=>{PROBE.SETTLEMENT_LAG_SECONDS})", meta_sql
        )

        shared_meta_sql = PROBE._meta_sql(60, 30, 30, 60, snapshot)
        self.assertIn(f"TIMESTAMPTZ '{snapshot}'", shared_meta_sql)
        self.assertIn("AS snapshot_is_settled", shared_meta_sql)
        self.assertIn("AS access_window_complete", shared_meta_sql)

    def test_snapshot_requires_timezone(self) -> None:
        with self.assertRaises(PROBE.CapacityReportError):
            PROBE._normalize_snapshot("2026-07-20 10:00:00")

    def test_snapshot_accepts_postgres_short_utc_offset(self) -> None:
        self.assertEqual(
            PROBE._normalize_snapshot("2026-07-20 10:00:00.123456+00"),
            "2026-07-20T10:00:00.123456+00:00",
        )

    def test_snapshot_pads_postgres_fraction_for_older_python(self) -> None:
        self.assertEqual(
            PROBE._normalize_snapshot("2026-07-20 13:12:33.29295+00"),
            "2026-07-20T13:12:33.292950+00:00",
        )

    def test_remote_payload_over_ssm_budget_is_rejected(self) -> None:
        oversized = {"payload": "x" * 22_000}
        args = SimpleNamespace(edge="us3", days=60, min_seconds=60)

        with mock.patch.object(PROBE, "analyze_edge", return_value=oversized):
            with self.assertRaises(PROBE.CapacityReportError):
                PROBE._command_analyze(args)

    def test_analyze_edge_builds_strict_pristine_document(self) -> None:
        snapshot = "2026-07-20T09:55:00.000000+00:00"
        meta_row = {
            "snapshot_at": snapshot,
            "snapshot_is_settled": "t",
            "access_window_complete": "t",
            "db_now_utc": "2026-07-20 10:00:00",
            "requested_days": "60",
            "error_retention_days": "30",
            "analysis_days": "30",
            "min_sustain_seconds": "60",
            "access_min_utc": "2026-06-20 00:00:00",
            "runtime_sampling_enabled": "f",
        }
        account_rows = [
            {
                "id": "7",
                "platform": "kiro",
                "channel_type": "0",
                "concurrency": "30",
            }
        ]
        event_rows = [
            {"row_kind": "event", "source": source, "account_id": "7", "ts_ms": str(ts), "delta": str(delta)}
            for source in ("F", "H")
            for ts, delta in ((0, 1), (60_000, -1))
        ]
        event_rows.append(
            {
                "row_kind": "coverage",
                "account_id": "7",
                "usage_total": "10",
                "usage_matched": "10",
                "invalid_access_rows": "0",
                "invalid_usage_rows": "0",
            }
        )

        with (
            mock.patch.object(
                PROBE,
                "_one_row",
                side_effect=[{"error_retention_days": "30"}, meta_row],
            ),
            mock.patch.object(
                PROBE, "_copy_rows", side_effect=[account_rows, [], event_rows]
            ),
        ):
            document = PROBE.analyze_edge("us3", 60, 60, snapshot)

        self.assertEqual(document["meta"]["snapshot_at"], snapshot)
        self.assertEqual(
            document["accounts"][0]["sources"]["F"]["pristine"],
            {"peak": 1, "observed": 1, "repeated": 0, "cross_day": 0},
        )
        self.assertEqual(set(document["accounts"][0]), PROBE._ACCOUNT_KEYS)

    def test_analyze_edge_rejects_incomplete_access_window(self) -> None:
        snapshot = "2026-07-20T09:55:00.000000+00:00"
        meta_row = {
            "snapshot_at": snapshot,
            "snapshot_is_settled": "t",
            "access_window_complete": "f",
        }
        with (
            mock.patch.object(
                PROBE,
                "_one_row",
                side_effect=[{"error_retention_days": "30"}, meta_row],
            ),
            mock.patch.object(PROBE, "_copy_rows") as copy_rows,
        ):
            with self.assertRaises(PROBE.CapacityReportError):
                PROBE.analyze_edge("us3", 60, 60, snapshot)

        copy_rows.assert_not_called()

    def test_remote_entrypoint_forwards_shared_snapshot(self) -> None:
        script = Path(REPORT.__file__).with_name("probe-edge-capacity.sh")
        snapshot = "2026-07-20T09:55:00.000000+00:00"
        with tempfile.TemporaryDirectory() as temporary:
            analyzer = Path(temporary) / "analyzer.py"
            analyzer.write_text(
                "import json, sys\nprint(json.dumps(sys.argv[1:]))\n",
                encoding="utf-8",
            )
            env = os.environ.copy()
            env.update(
                {
                    "ANALYZER": str(analyzer),
                    "EDGE_ID": "us3",
                    "DAYS": "60",
                    "MIN_SECONDS": "60",
                    "SNAPSHOT_AT": snapshot,
                }
            )
            proc = subprocess.run(
                ["bash", str(script)], capture_output=True, text=True, env=env
            )

        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(
            json.loads(proc.stdout),
            [
                "analyze",
                "--edge",
                "us3",
                "--days",
                "60",
                "--min-seconds",
                "60",
                "--snapshot-at",
                snapshot,
            ],
        )


class AggregateTypeGroupsTest(unittest.TestCase):
    def test_third_highest_cross_edge_evidence_sets_recommendation(self) -> None:
        documents = [
            _document("us3", [_account(1, "kiro", 21), _account(2, "kiro", 17)]),
            _document("us4", [_account(1, "kiro", 21), _account(2, "kiro", 15)]),
            _document("us5", [_account(1, "kiro", 21), _account(2, "kiro", 14)]),
        ]

        groups = REPORT.aggregate_type_groups(documents)

        self.assertEqual(len(groups), 1)
        self.assertEqual(groups[0]["recommended"], 21)
        self.assertEqual(groups[0]["supporter_count"], 3)
        self.assertEqual(groups[0]["supporter_edge_count"], 3)

    def test_same_edge_top_three_falls_to_cross_edge_value(self) -> None:
        documents = [
            _document(
                "us3",
                [
                    _account(1, "openai", 30),
                    _account(2, "openai", 29),
                    _account(3, "openai", 28),
                ],
            ),
            _document("us4", [_account(1, "openai", 20)]),
        ]

        group = REPORT.aggregate_type_groups(documents)[0]

        self.assertEqual(group["recommended"], 20)
        self.assertEqual(group["supporter_edge_count"], 2)

    def test_two_accounts_must_both_support_value(self) -> None:
        documents = [
            _document("us3", [_account(1, "antigravity", 32)]),
            _document("us4", [_account(1, "antigravity", 25)]),
        ]

        group = REPORT.aggregate_type_groups(documents)[0]

        self.assertEqual(group["recommended"], 25)
        self.assertEqual(group["confidence"], "中")

    def test_multi_account_single_edge_has_no_type_recommendation(self) -> None:
        documents = [
            _document(
                "us3",
                [
                    _account(1, "kiro", 21),
                    _account(2, "kiro", 21),
                    _account(3, "kiro", 21),
                ],
            )
        ]

        group = REPORT.aggregate_type_groups(documents)[0]

        self.assertIsNone(group["recommended"])
        self.assertEqual(group["supporter_count"], 0)
        self.assertEqual(group["confidence"], "暂定")

    def test_duplicate_account_does_not_count_as_independent_evidence(self) -> None:
        duplicate = _account(1, "kiro", 30)
        documents = [
            _document("us3", [duplicate, duplicate]),
            _document("us4", [_account(1, "kiro", 30)]),
        ]

        with self.assertRaises(REPORT.CapacityReportError):
            REPORT.aggregate_type_groups(documents)

    def test_two_accounts_without_cross_day_evidence_are_tentative(self) -> None:
        documents = [
            _document("us3", [_account(1, "kiro", 0)]),
            _document("us4", [_account(1, "kiro", 0)]),
        ]

        group = REPORT.aggregate_type_groups(documents)[0]

        self.assertIsNone(group["recommended"])
        self.assertEqual(group["confidence"], "暂定")

    def test_current_cap_does_not_override_cross_account_evidence(self) -> None:
        low_cap = _account(2, "kiro", 0)
        low_cap["configured_concurrency"] = 5
        documents = [
            _document("us3", [_account(1, "kiro", 20), low_cap]),
            _document("us4", [_account(1, "kiro", 20)]),
            _document("us5", [_account(1, "kiro", 20)]),
        ]

        group = REPORT.aggregate_type_groups(documents)[0]

        self.assertEqual(group["recommended"], 20)


class RenderReportTest(unittest.TestCase):
    def test_report_is_chinese_and_does_not_align_independent_fh_maxima(self) -> None:
        documents = [
            _document("us3", [_account(1, "openai", 20, h_value=44)]),
            _document("us4", [_account(1, "openai", 20, h_value=30)]),
            _document("us5", [_account(1, "kiro", 20)]),
        ]
        documents[2]["accounts"][0]["configured_concurrency"] = 30

        report = REPORT.render_report(documents)

        self.assertIn("## 同类型建议", report)
        self.assertIn("## F/H 解释", report)
        self.assertIn("不保证发生在同一时段，不能直接相减", report)
        self.assertIn("至少 3 个独立账号", report)
        self.assertNotIn("## Kiro 峰值与持续值", report)
        self.assertNotIn("## 模型证据", report)
        self.assertNotIn("同时约", report)

    def test_rejects_mixed_sustain_thresholds(self) -> None:
        first = _document("us3", [_account(1, "kiro", 20)])
        second = _document("us4", [_account(1, "kiro", 20)])
        second["meta"]["min_sustain_seconds"] = 600

        with self.assertRaises(REPORT.CapacityReportError):
            REPORT.render_report([first, second])

    def test_rejects_duplicate_edge_documents(self) -> None:
        document = _document("us3", [_account(1, "kiro", 20)])

        with self.assertRaises(REPORT.CapacityReportError):
            REPORT.render_report([document, document])

    def test_rejects_mixed_analysis_windows(self) -> None:
        first = _document("us3", [_account(1, "kiro", 20)])
        second = _document("us4", [_account(1, "kiro", 20)])
        second["meta"]["analysis_days"] = 14

        with self.assertRaises(REPORT.CapacityReportError):
            REPORT.render_report([first, second])

    def test_rejects_mixed_absolute_windows(self) -> None:
        first = _document("us3", [_account(1, "kiro", 20)])
        second = _document("us4", [_account(1, "kiro", 20)])
        second["meta"]["snapshot_at"] = "2026-07-20T09:56:00.000000+00:00"

        with self.assertRaises(REPORT.CapacityReportError):
            REPORT.render_report([first, second])

    def test_rejects_unknown_or_sensitive_raw_fields(self) -> None:
        for key in ("account_name", "email", "credentials", "model"):
            with self.subTest(key=key):
                document = _document("us3", [_account(1, "kiro", 20)])
                document["accounts"][0][key] = "must-not-persist"

                with self.assertRaises(REPORT.CapacityReportError):
                    REPORT.render_report([document])

    def test_rejects_previous_probe_schema(self) -> None:
        document = _document("us3", [_account(1, "kiro", 20)])
        document["schema_version"] = PROBE.SCHEMA_VERSION - 1

        with self.assertRaises(REPORT.CapacityReportError):
            REPORT.render_report([document])

    def test_c10_label_and_default_path_use_minutes(self) -> None:
        self.assertEqual(REPORT._metric_label(600), "C10")
        self.assertTrue(REPORT._default_report_path(600).endswith("-c10.md"))


class CollectionWorkflowTest(unittest.TestCase):
    @staticmethod
    def _args(**overrides: object) -> SimpleNamespace:
        values = {
            "edges": "auto",
            "days": 60,
            "min_seconds": 60,
            "timeout_seconds": 600,
            "raw_dir": None,
            "output": "docs/ops/edge-capacity-report-20260720-c1.md",
        }
        values.update(overrides)
        return SimpleNamespace(**values)

    def test_failed_edge_does_not_publish_raw_or_report(self) -> None:
        args = self._args(raw_dir=".cache/edge-capacity-report-raw")
        first = _document("us3", [_account(1, "kiro", 20)])

        with (
            mock.patch.object(REPORT, "_resolve_edges", return_value=["us3", "us4"]),
            mock.patch.object(
                REPORT,
                "_collect_edge",
                side_effect=[first, REPORT.CapacityReportError("us4 failed")],
            ),
            mock.patch.object(REPORT, "_write_text_atomic") as write,
        ):
            with self.assertRaises(REPORT.CapacityReportError):
                REPORT._command_collect(args)

        write.assert_not_called()

    def test_collect_uses_one_shared_snapshot(self) -> None:
        snapshots: list[str] = []

        def collect(
            _root: Path,
            edge: str,
            _days: int,
            _min_seconds: int,
            _timeout_seconds: int,
            snapshot_at: str,
        ) -> dict[str, object]:
            snapshots.append(snapshot_at)
            document = _document(edge, [_account(1, "kiro", 20)])
            document["meta"]["snapshot_at"] = snapshot_at
            return document

        with (
            mock.patch.object(REPORT, "_resolve_edges", return_value=["us3", "us4"]),
            mock.patch.object(REPORT, "_collect_edge", side_effect=collect),
            mock.patch.object(REPORT, "_write_text_atomic"),
        ):
            self.assertEqual(REPORT._command_collect(self._args()), 0)

        self.assertEqual(len(set(snapshots)), 1)

    def test_partial_edge_set_is_rejected(self) -> None:
        documents = [_document("us3", [_account(1, "kiro", 20)])]

        with self.assertRaises(REPORT.CapacityReportError):
            REPORT._require_complete_edge_set(documents, ["us3", "us4"])

    def test_collect_rejects_explicit_partial_edge_list(self) -> None:
        with (
            mock.patch.object(
                REPORT,
                "_resolve_edges",
                side_effect=[["us3"], ["us3", "us4"]],
            ),
            mock.patch.object(REPORT, "_collect_edge") as collect,
        ):
            with self.assertRaises(REPORT.CapacityReportError):
                REPORT._command_collect(self._args(edges="us3"))

        collect.assert_not_called()

    def test_collect_parser_rejects_timeout_outside_aws_range(self) -> None:
        parser = REPORT._build_parser()
        for value in ("29", "2592001"):
            with self.subTest(value=value), self.assertRaises(SystemExit):
                parser.parse_args(["collect", "--timeout-seconds", value])


if __name__ == "__main__":
    unittest.main()
