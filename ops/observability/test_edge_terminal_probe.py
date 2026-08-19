import datetime as dt
import json
import unittest

from ops.observability.edge_terminal_probe import ProbeContractError, parse_probe_output


NOW = dt.datetime(2026, 8, 18, 12, 10, tzinfo=dt.timezone.utc)


def line(tag, payload):
    return f"{tag} {json.dumps(payload)}"


class EdgeTerminalProbeTest(unittest.TestCase):
    def test_parses_complete_exact_model_bucket(self):
        raw = "\n".join(
            [
                line("TERMINAL_META", {"schema_version": 1, "watermark": "2026-08-18T12:06:00Z"}),
                line("TERMINAL_WINDOW", {"bucket_start": "2026-08-18T12:00:00Z", "heartbeat_minutes": 5, "producer_epochs": 1, "all_complete": True}),
                line("TERMINAL_FACT", {"bucket_start": "2026-08-18T12:00:00Z", "group_id": 7, "requested_model": "claude-sonnet-4-6", "success": 90, "final_empty_pool_429": 10, "other_error": 2}),
            ]
        )
        parsed = parse_probe_output(raw, "us5", now=NOW)
        self.assertEqual("fresh", parsed["telemetry_status"])
        self.assertTrue(parsed["buckets"][0]["complete"])
        self.assertEqual(10, parsed["buckets"][0]["facts"][0]["final_empty_pool_429"])

    def test_missing_heartbeat_and_multiple_epochs_are_incomplete(self):
        for heartbeat_minutes, producer_epochs in ((4, 1), (5, 2)):
            raw = "\n".join(
                [
                    line("TERMINAL_META", {"schema_version": 1, "watermark": "2026-08-18T12:06:00Z"}),
                    line("TERMINAL_WINDOW", {"bucket_start": "2026-08-18T12:00:00Z", "heartbeat_minutes": heartbeat_minutes, "producer_epochs": producer_epochs, "all_complete": True}),
                ]
            )
            parsed = parse_probe_output(raw, "us5", now=NOW)
            self.assertFalse(parsed["buckets"][0]["complete"])

    def test_stale_watermark_is_explicit(self):
        raw = "\n".join(
            [
                line("TERMINAL_META", {"schema_version": 1, "watermark": "2026-08-18T11:40:00Z"}),
                line("TERMINAL_WINDOW", {"bucket_start": "2026-08-18T11:35:00Z", "heartbeat_minutes": 5, "producer_epochs": 1, "all_complete": True}),
            ]
        )
        self.assertEqual("stale", parse_probe_output(raw, "us5", now=NOW)["telemetry_status"])

    def test_rejects_malformed_and_orphan_fact_output(self):
        with self.assertRaises(ProbeContractError):
            parse_probe_output("TERMINAL_META not-json", "us5", now=NOW)

        raw = "\n".join(
            [
                line("TERMINAL_META", {"schema_version": 1, "watermark": "2026-08-18T12:06:00Z"}),
                line("TERMINAL_FACT", {"bucket_start": "2026-08-18T12:00:00Z", "group_id": 7, "requested_model": "gpt-5.4", "success": 1, "final_empty_pool_429": 0, "other_error": 0}),
            ]
        )
        with self.assertRaises(ProbeContractError):
            parse_probe_output(raw, "us5", now=NOW)


if __name__ == "__main__":
    unittest.main()
