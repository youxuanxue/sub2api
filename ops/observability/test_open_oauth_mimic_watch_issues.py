#!/usr/bin/env python3
"""Unit tests for open_oauth_mimic_watch_issues.py body builders."""
from __future__ import annotations

import importlib.util
import pathlib
import unittest

_MODULE_PATH = pathlib.Path(__file__).resolve().parent / "open_oauth_mimic_watch_issues.py"
_SPEC = importlib.util.spec_from_file_location("open_oauth_mimic_watch_issues", _MODULE_PATH)
assert _SPEC and _SPEC.loader
_MOD = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(_MOD)


class OpenOAuthMimicWatchIssuesTest(unittest.TestCase):
    def test_drift_body_lists_alerts_and_followup(self) -> None:
        report = {
            "meta": {"since": "24h"},
            "summary": {
                "eligible_edge_count": 2,
                "alerts": ["us3:ingress_sdk_no_egress_fingerprint_logs"],
                "per_edge": [
                    {
                        "edge_id": "us3",
                        "oauth_openai_python_count": 40,
                        "egress_oauth_mimic_count": 0,
                        "billing_prefix_rate": None,
                        "verdict": "ingress_sdk_seen_no_egress_fingerprint_logs",
                    }
                ],
            },
        }
        body = _MOD.drift_body(report, "https://example/run/1", None)
        self.assertIn("ingress_sdk_no_egress_fingerprint_logs", body)
        self.assertIn("tokenkey-cc-fingerprint-alignment", body)
        self.assertIn("probe-oauth-mimicry-chain.sh", body)

    def test_recover_body_mentions_clear(self) -> None:
        body = _MOD.recover_body("https://example/run/2", {"summary": {"eligible_edge_count": 1, "has_actionable_drift": False}})
        self.assertIn("no actionable", body.lower())


if __name__ == "__main__":
    unittest.main()
