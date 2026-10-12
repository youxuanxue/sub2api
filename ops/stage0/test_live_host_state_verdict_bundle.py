#!/usr/bin/env python3
from __future__ import annotations

import pathlib
import sys
import unittest


sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import live_host_state_verdict as verdict


class LiveHostStateVerdictBundleTest(unittest.TestCase):
    def test_expected_bundle_value_detects_stale_host_value(self) -> None:
        facts = verdict.parse_facts([
            'ENV {"key":"QA_BUNDLE_ENABLED","value":"true"}',
            'ENV {"key":"QA_BUNDLE_QUEUE_URL","value":"https://sqs.example/old"}',
        ])
        drifts = verdict.compute_drifts(
            facts,
            required_env=[],
            expected_env={"QA_BUNDLE_QUEUE_URL": "https://sqs.example/new"},
        )
        self.assertEqual(drifts, [
            "required env value differs on host: QA_BUNDLE_QUEUE_URL expected='https://sqs.example/new' observed='https://sqs.example/old'"
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
