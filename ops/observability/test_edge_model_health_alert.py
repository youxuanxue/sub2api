import datetime as dt
import contextlib
import io
import pathlib
import tempfile
import unittest

from ops.observability.edge_model_health_alert import evaluate, load_family_rules, load_previous_state


ROOT = pathlib.Path(__file__).resolve().parents[2]
RULES = load_family_rules(ROOT / "ops/observability/generated/model-family-rules.json")
NOW = dt.datetime(2026, 8, 18, 12, 20, tzinfo=dt.timezone.utc)


def fact(model, success, empty, other=0):
    return {
        "group_id": 7,
        "requested_model": model,
        "success": success,
        "final_empty_pool_429": empty,
        "other_error": other,
    }


def bucket(start, *facts, complete=True):
    return {
        "bucket_start": start,
        "complete": complete,
        "heartbeat_minutes": 5 if complete else 4,
        "producer_epochs": 1,
        "facts": list(facts),
    }


def edge(*buckets, edge_id="us5", telemetry_status="fresh"):
    return {
        "edge": edge_id,
        "reachable": True,
        "schema_version": 1,
        "watermark": "2026-08-18T12:16:00Z",
        "telemetry_status": telemetry_status,
        "buckets": list(buckets),
    }


def state_units(decision):
    return decision["state"]["model_units"]


class EdgeModelHealthAlertTest(unittest.TestCase):
    def test_provider_qualified_model_uses_generated_family_rules(self):
        decision = evaluate(
            [edge(
                bucket("2026-08-18T12:05:00Z", fact("anthropic.claude-sonnet-4-6", 40, 10)),
                bucket("2026-08-18T12:10:00Z", fact("claude-opus-4-6", 40, 10)),
            )],
            {},
            RULES,
            evaluated_at=NOW,
        )
        self.assertEqual({"kind": "family", "id": "claude"}, state_units(decision)[0]["unit"])
        self.assertEqual("degraded", state_units(decision)[0]["status"])

    def test_unknown_success_only_creates_no_candidate_state_or_notification(self):
        decision = evaluate(
            [edge(
                bucket("2026-08-18T12:05:00Z", fact("vendor-new-model", 20, 0)),
                bucket("2026-08-18T12:10:00Z", fact("vendor-new-model", 20, 0)),
            )],
            {},
            RULES,
            evaluated_at=NOW,
        )
        self.assertFalse(decision["should_alert"])
        self.assertEqual([], state_units(decision))

    def test_unknown_models_are_dynamic_exact_units_and_never_share_a_bucket(self):
        decision = evaluate(
            [edge(
                bucket("2026-08-18T12:05:00Z", fact("vendor-a", 20, 5), fact("vendor-b", 20, 5)),
                bucket("2026-08-18T12:10:00Z", fact("vendor-a", 20, 5), fact("vendor-b", 20, 5)),
            )],
            {},
            RULES,
            evaluated_at=NOW,
        )
        self.assertFalse(decision["should_alert"])
        self.assertEqual([], state_units(decision))

        triggered = evaluate(
            [edge(
                bucket("2026-08-18T12:05:00Z", fact("vendor-a", 40, 10), fact("vendor-b", 20, 0)),
                bucket("2026-08-18T12:10:00Z", fact("vendor-a", 40, 10), fact("vendor-b", 20, 0)),
            )],
            {},
            RULES,
            evaluated_at=NOW,
        )
        self.assertEqual({"kind": "model", "id": "vendor-a"}, state_units(triggered)[0]["unit"])
        self.assertNotIn("vendor-b", triggered["message"])

    def test_threshold_boundaries_are_high_precision(self):
        cases = [
            ("nine does not trigger", [fact("gpt-5.4", 36, 9), fact("gpt-5.4", 36, 9)], None),
            ("single fifty is unavailable", [fact("gpt-5.4", 0, 0), fact("gpt-5.4", 0, 50)], "unavailable"),
            ("two eighty percent buckets are unavailable", [fact("gpt-5.4", 2, 10), fact("gpt-5.4", 2, 10)], "unavailable"),
            ("two one-of-one buckets do not trigger", [fact("gpt-5.4", 0, 1), fact("gpt-5.4", 0, 1)], None),
        ]
        for name, facts, want in cases:
            with self.subTest(name=name):
                decision = evaluate(
                    [edge(
                        bucket("2026-08-18T12:05:00Z", facts[0]),
                        bucket("2026-08-18T12:10:00Z", facts[1]),
                    )],
                    {},
                    RULES,
                    evaluated_at=NOW,
                )
                got = state_units(decision)[0]["status"] if state_units(decision) else None
                self.assertEqual(want, got)

    def test_nonconsecutive_buckets_cannot_trigger(self):
        decision = evaluate(
            [edge(
                bucket("2026-08-18T12:00:00Z", fact("qwen3-coder", 40, 10)),
                bucket("2026-08-18T12:10:00Z", fact("qwen3-coder", 40, 10)),
            )],
            {},
            RULES,
            evaluated_at=NOW,
        )
        self.assertEqual([], state_units(decision))

    def test_active_unit_recovers_only_after_three_matching_buckets(self):
        active = {
            "schema_version": 1,
            "model_units": [{
                "edge": "us5",
                "unit": {"kind": "model", "id": "vendor-new-model"},
                "status": "unavailable",
                "started_bucket": "2026-08-18T11:30:00Z",
                "last_evaluated_bucket": "2026-08-18T11:55:00Z",
                "last_notified_severity": "unavailable",
            }],
            "hosts": [],
            "telemetry": [],
        }
        stopped = evaluate(
            [edge(
                bucket("2026-08-18T12:00:00Z"),
                bucket("2026-08-18T12:05:00Z"),
                bucket("2026-08-18T12:10:00Z"),
            )],
            active,
            RULES,
            evaluated_at=NOW,
        )
        self.assertEqual([], state_units(stopped))
        self.assertIn("影响已停止", stopped["message"])
        self.assertNotIn("容量恢复", stopped["message"])

        mixed = evaluate(
            [edge(
                bucket("2026-08-18T12:00:00Z"),
                bucket("2026-08-18T12:05:00Z", fact("vendor-new-model", 20, 0)),
                bucket("2026-08-18T12:10:00Z", fact("vendor-new-model", 20, 0)),
            )],
            active,
            RULES,
            evaluated_at=NOW,
        )
        self.assertEqual("unavailable", state_units(mixed)[0]["status"])
        self.assertFalse(mixed["should_alert"])

    def test_stale_or_incomplete_telemetry_freezes_active_model_state(self):
        active = {
            "schema_version": 1,
            "model_units": [{
                "edge": "us5",
                "unit": {"kind": "family", "id": "claude"},
                "status": "degraded",
                "started_bucket": "2026-08-18T11:30:00Z",
                "last_evaluated_bucket": "2026-08-18T11:55:00Z",
                "last_notified_severity": "degraded",
            }],
            "hosts": [],
            "telemetry": [],
        }
        decision = evaluate(
            [edge(bucket("2026-08-18T12:10:00Z", complete=False), telemetry_status="stale")],
            active,
            RULES,
            evaluated_at=NOW,
        )
        self.assertEqual("2026-08-18T11:55:00Z", state_units(decision)[0]["last_evaluated_bucket"])
        self.assertEqual("degraded", state_units(decision)[0]["status"])

    def test_family_top_model_change_at_twenty_five_percent_notifies(self):
        initial = evaluate(
            [edge(
                bucket("2026-08-18T12:00:00Z", fact("claude-a", 30, 10)),
                bucket("2026-08-18T12:05:00Z", fact("claude-a", 30, 10)),
            )],
            {},
            RULES,
            evaluated_at=dt.datetime(2026, 8, 18, 12, 10, tzinfo=dt.timezone.utc),
        )
        changed = evaluate(
            [edge(
                bucket("2026-08-18T12:05:00Z", fact("claude-a", 30, 10)),
                bucket("2026-08-18T12:10:00Z", fact("claude-a", 30, 30), fact("claude-b", 10, 10)),
            )],
            initial["state"],
            RULES,
            evaluated_at=NOW,
        )
        self.assertTrue(changed["should_alert"])
        self.assertEqual("impact_changed", changed["transitions"][0]["reason"])
        self.assertIn("claude-b", changed["message"])

    def test_family_top_model_display_escapes_controls_and_bounds_length(self):
        malicious = "claude-a\n" + "x" * 100
        decision = evaluate(
            [edge(
                bucket("2026-08-18T12:05:00Z", fact(malicious, 40, 10)),
                bucket("2026-08-18T12:10:00Z", fact(malicious, 40, 10)),
            )],
            {},
            RULES,
            evaluated_at=NOW,
        )
        self.assertNotIn(malicious, decision["message"])
        self.assertIn("claude-a?", decision["message"])
        self.assertIn("...", decision["message"])
        self.assertEqual(malicious, state_units(decision)[0]["last_notified_top_models"][0]["model"])

    def test_unavailable_never_downgrades_and_same_bucket_is_idempotent(self):
        unavailable = {
            "schema_version": 1,
            "model_units": [{
                "edge": "us5",
                "unit": {"kind": "family", "id": "gpt"},
                "status": "unavailable",
                "started_bucket": "2026-08-18T12:00:00Z",
                "last_evaluated_bucket": "2026-08-18T12:05:00Z",
                "last_notified_severity": "unavailable",
                "last_notified_top_models": [{"model": "gpt-5.4", "empty_pool_429": 10}],
            }],
            "hosts": [],
            "telemetry": [],
        }
        lowered = evaluate(
            [edge(
                bucket("2026-08-18T12:05:00Z", fact("gpt-5.4", 40, 10)),
                bucket("2026-08-18T12:10:00Z", fact("gpt-5.4", 40, 10)),
            )],
            unavailable,
            RULES,
            evaluated_at=NOW,
        )
        self.assertEqual("unavailable", state_units(lowered)[0]["status"])
        self.assertFalse(lowered["should_alert"])

        repeated = evaluate(
            [edge(bucket("2026-08-18T12:10:00Z", fact("gpt-5.4", 40, 10)))],
            lowered["state"],
            RULES,
            evaluated_at=NOW,
        )
        self.assertEqual(lowered["state"], repeated["state"])
        self.assertFalse(repeated["should_alert"])

    def test_host_unreachable_is_independent_and_model_transitions_are_combined(self):
        decision = evaluate(
            [
                edge(
                    bucket("2026-08-18T12:05:00Z", fact("gpt-5.4", 40, 10)),
                    bucket("2026-08-18T12:10:00Z", fact("gpt-5.4", 40, 10)),
                    edge_id="us4",
                ),
                {"edge": "us5", "reachable": False, "reason": "https_unreachable", "schema_version": 1},
            ],
            {},
            RULES,
            evaluated_at=NOW,
        )
        self.assertTrue(decision["should_alert"])
        self.assertEqual(2, len(decision["transitions"]))
        self.assertIn("us4", decision["message"])
        self.assertIn("us5", decision["message"])

    def test_telemetry_alert_needs_two_distinct_slots_and_recovers_once(self):
        unavailable = {
            "edge": "us5",
            "reachable": True,
            "reason": "ssm_unreachable",
            "schema_version": 1,
            "telemetry_status": "unavailable",
            "buckets": [],
        }
        first = evaluate([unavailable], {}, RULES, evaluated_at=NOW)
        self.assertFalse(first["should_alert"])
        self.assertEqual("pending", first["state"]["telemetry"][0]["status"])

        repeated = evaluate([unavailable], first["state"], RULES, evaluated_at=NOW)
        self.assertFalse(repeated["should_alert"])
        self.assertEqual(1, len(repeated["state"]["telemetry"][0]["failure_slots"]))

        second = evaluate(
            [unavailable],
            repeated["state"],
            RULES,
            evaluated_at=NOW + dt.timedelta(minutes=5),
        )
        self.assertTrue(second["should_alert"])
        self.assertIn("监控数据不可用", second["message"])

        recovered = evaluate(
            [edge(bucket("2026-08-18T12:20:00Z"))],
            second["state"],
            RULES,
            evaluated_at=NOW + dt.timedelta(minutes=10),
        )
        self.assertTrue(recovered["should_alert"])
        self.assertEqual([], recovered["state"]["telemetry"])
        self.assertIn("监控数据已恢复", recovered["message"])

    def test_nonconsecutive_telemetry_failures_do_not_combine(self):
        unavailable = {
            "edge": "us5",
            "reachable": True,
            "reason": "ssm_unreachable",
            "schema_version": 1,
            "telemetry_status": "unavailable",
            "buckets": [],
        }
        first = evaluate([unavailable], {}, RULES, evaluated_at=NOW)
        later = evaluate(
            [unavailable],
            first["state"],
            RULES,
            evaluated_at=NOW + dt.timedelta(minutes=15),
        )
        self.assertFalse(later["should_alert"])
        self.assertEqual(1, len(later["state"]["telemetry"][0]["failure_slots"]))

    def test_corrupt_previous_state_rebuilds_with_an_explicit_warning(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "state.json"
            for payload in ("not-json", '{"schema_version":99}'):
                path.write_text(payload, encoding="utf-8")
                stderr = io.StringIO()
                with contextlib.redirect_stderr(stderr):
                    self.assertEqual({}, load_previous_state(path))
                self.assertIn("rebuilding", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
