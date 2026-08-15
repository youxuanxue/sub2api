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


if __name__ == "__main__":
    unittest.main()
