#!/usr/bin/env python3

from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock


MODULE_PATH = Path(__file__).with_name("edge_capacity_report.py")
SPEC = importlib.util.spec_from_file_location("edge_capacity_report", MODULE_PATH)
assert SPEC and SPEC.loader
REPORT = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = REPORT
SPEC.loader.exec_module(REPORT)


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
        "observed_episode_seconds": 60.0,
        "observed_episode_start_utc": "2026-07-01T00:00:00Z",
        "observed_episode_end_utc": "2026-07-01T00:01:00Z",
        "peak_clean_episode_seconds": 1.5,
        "repeat_episode_count": 3,
        "repeat_distinct_days": 2,
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
        "account_name": f"{platform}-{account_id}",
        "platform": platform,
        "account_type": "oauth",
        "channel_type": channel_type,
        "configured_concurrency": 50,
        "model_mapping_keys": [],
        "final_error_intervals": 0,
        "hidden_error_intervals": 0,
        "recommended": cross_day,
        "recommendation_evidence": "cross-day-pristine",
        "coverage": {
            "usage_total": 100,
            "usage_matched": 100,
            "usage_match_pct": 100.0,
            "invalid_access_rows": 0,
            "invalid_usage_rows": 0,
        },
        "models": [{"model": f"{platform}-model", "requests": 100}],
        "sources": {
            "F": {"pristine": f_metric},
            "H": {"pristine": h_metric},
        },
    }


def _document(edge: str, accounts: list[dict[str, object]]) -> dict[str, object]:
    return {
        "schema_version": 1,
        "edge": edge,
        "meta": {
            "db_now_utc": "2026-07-20 10:00:00",
            "requested_days": 60,
            "error_retention_days": 30,
            "analysis_days": 30,
            "min_sustain_seconds": 60,
            "usage_min_utc": "2026-06-01 00:00:00",
            "usage_max_utc": "2026-07-20 10:00:00",
            "access_min_utc": "2026-06-20 00:00:00",
            "access_max_utc": "2026-07-20 10:00:00",
            "runtime_sampling_enabled": False,
        },
        "accounts": accounts,
        "pool_context": [],
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

        result = REPORT.summarize_runs(events, [], 60)

        self.assertEqual(result["peak"], 2)
        self.assertEqual(result["observed"], 2)
        self.assertEqual(result["repeated"], 2)
        self.assertEqual(result["cross_day"], 2)

    def test_one_long_episode_is_not_three_repetitions(self) -> None:
        events = _events((0, 180_000), (0, 180_000))

        result = REPORT.summarize_runs(events, [], 60)

        self.assertEqual(result["observed"], 2)
        self.assertEqual(result["repeated"], 0)
        self.assertEqual(result["cross_day"], 0)

    def test_long_request_qualifies_without_minute_events(self) -> None:
        result = REPORT.summarize_runs(_events((0, 61_000)), [], 60)

        self.assertEqual(result["observed"], 1)

    def test_error_point_splits_an_otherwise_qualifying_episode(self) -> None:
        result = REPORT.summarize_runs(
            _events((0, 100_000)),
            [(50_000, 50_001)],
            60,
        )

        self.assertEqual(result["observed"], 0)

    def test_equal_timestamp_churn_keeps_threshold_continuous(self) -> None:
        result = REPORT.summarize_runs(
            _events((0, 120_000), (60_000, 180_000)),
            [],
            60,
        )

        self.assertEqual(result["observed"], 2)
        self.assertEqual(result["observed_episode_seconds"], 60.0)


class CollectionContractTest(unittest.TestCase):
    def test_queries_share_one_bounded_snapshot_and_drop_ambiguous_f(self) -> None:
        snapshot = "2026-07-20T10:00:00.000000+00:00"

        error_sql = REPORT._error_sql(30, snapshot)
        event_sql = REPORT._event_sql(30, snapshot)

        self.assertIn(f"TIMESTAMPTZ '{snapshot}'", error_sql)
        self.assertIn(f"TIMESTAMPTZ '{snapshot}'", event_sql)
        self.assertIn("WHERE rn=1 AND candidate_count=1", error_sql)
        self.assertIn("SELECT * FROM selected WHERE candidate_count=1", event_sql)
        self.assertIn("GREATEST(", event_sql)
        self.assertIn("SELECT lower_ms FROM bounds", event_sql)
        self.assertNotIn("now()", error_sql)
        self.assertNotIn("now()", event_sql)

    def test_snapshot_requires_timezone(self) -> None:
        with self.assertRaises(REPORT.CapacityReportError):
            REPORT._normalize_snapshot("2026-07-20 10:00:00")

    def test_snapshot_accepts_postgres_short_utc_offset(self) -> None:
        self.assertEqual(
            REPORT._normalize_snapshot("2026-07-20 10:00:00.123456+00"),
            "2026-07-20T10:00:00.123456+00:00",
        )

    def test_ssm_pending_states_resume_original_command(self) -> None:
        for status in ("Pending", "InProgress", "Delayed"):
            with self.subTest(status=status):
                self.assertTrue(
                    REPORT._ssm_command_is_resumable(f"waiter status={status}")
                )
        self.assertFalse(REPORT._ssm_command_is_resumable("status=Failed"))

    def test_remote_payload_over_ssm_budget_is_rejected(self) -> None:
        oversized = {"payload": "x" * 22_000}
        args = SimpleNamespace(edge="us3", days=60, min_seconds=60)

        with mock.patch.object(REPORT, "analyze_edge", return_value=oversized):
            with self.assertRaises(REPORT.CapacityReportError):
                REPORT._command_analyze(args)


class AccountRecommendationTest(unittest.TestCase):
    def test_requires_cross_day_pristine_evidence(self) -> None:
        same_day_only = {"observed": 30, "repeated": 25, "cross_day": 0}

        recommendation, evidence = REPORT._account_recommendation(same_day_only, 30)

        self.assertIsNone(recommendation)
        self.assertEqual(evidence, "same-day-repeat-only")

    def test_cross_day_recommendation_is_capped(self) -> None:
        metric = {"observed": 40, "repeated": 35, "cross_day": 32}

        recommendation, evidence = REPORT._account_recommendation(metric, 30)

        self.assertEqual(recommendation, 30)
        self.assertEqual(evidence, "cross-day-pristine")


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


class RenderReportTest(unittest.TestCase):
    def test_report_is_chinese_and_does_not_align_independent_fh_maxima(self) -> None:
        documents = [
            _document("us3", [_account(1, "openai", 20, h_value=44)]),
            _document("us4", [_account(1, "openai", 20, h_value=30)]),
        ]
        documents[0]["accounts"][0]["account_name"] = "sensitive-account-name"

        report = REPORT.render_report(documents)

        self.assertIn("## 同类型建议", report)
        self.assertIn("## F/H 解释", report)
        self.assertIn("不保证发生在同一时段，不能直接相减", report)
        self.assertIn("至少 3 个独立账号", report)
        self.assertNotIn("sensitive-account-name", report)
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


if __name__ == "__main__":
    unittest.main()
