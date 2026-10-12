#!/usr/bin/env python3
from __future__ import annotations

import pathlib
import sys
import unittest


sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import live_host_state_verdict as verdict


class LiveHostStateVerdictTest(unittest.TestCase):
    def test_expected_raw_archive_value_detects_stale_host_value(self) -> None:
        facts = verdict.parse_facts([
            'ENV {"key":"QA_ARCHIVE_ENABLED","value":"true"}',
            'ENV {"key":"QA_ARCHIVE_STORAGE_ENDPOINT","value":"https://old.example"}',
        ])
        drifts = verdict.compute_drifts(
            facts,
            required_env=[],
            expected_env={"QA_ARCHIVE_STORAGE_ENDPOINT": "https://new.example"},
        )
        self.assertEqual(drifts, [
            "required env value differs on host: QA_ARCHIVE_STORAGE_ENDPOINT expected='https://new.example' observed='https://old.example'"
        ])

    def test_default_expected_env_is_first_output_ssot(self) -> None:
        self.assertEqual(
            verdict.DEFAULT_EXPECTED_ENV["GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS"],
            "30",
        )
        self.assertEqual(
            verdict.DEFAULT_EXPECTED_ENV[
                "GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS"
            ],
            "60",
        )
        self.assertEqual(
            verdict.DEFAULT_EXPECTED_ENV["GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS"],
            "60",
        )
        self.assertEqual(
            verdict.DEFAULT_EXPECTED_ENV[
                "GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS"
            ],
            "300",
        )


if __name__ == "__main__":
    unittest.main()


class IndependentMaintenanceTest(unittest.TestCase):
    def test_gateway_health_does_not_claim_raw_maintenance_coordinates(self):
        facts = verdict.parse_facts([
            'ENV {"key":"SERVER_FRONTEND_URL","value":"https://api.example.test"}',
            *verdict._first_output_env_lines(),
        ])
        self.assertEqual(verdict.compute_drifts(facts), [])
