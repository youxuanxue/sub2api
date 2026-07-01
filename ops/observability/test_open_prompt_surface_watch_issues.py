#!/usr/bin/env python3
"""Unit tests for prompt-surface-watch issue body builders."""
from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from open_prompt_surface_watch_issues import (
    SIG_PROD_DRIFT,
    SIG_REGISTRY,
    prod_drift_body,
    prod_recover_body,
    registry_failure_body,
)


class OpenPromptSurfaceWatchIssuesTest(unittest.TestCase):
    def test_registry_failure_body_contains_signature(self) -> None:
        body = registry_failure_body("https://example/run/1")
        self.assertIn(SIG_REGISTRY, body)
        self.assertIn("registry-gate failed", body)
        self.assertIn("https://example/run/1", body)

    def test_prod_drift_body_lists_alerts(self) -> None:
        report = {
            "meta": {"container": "tokenkey-blue", "since": "24h"},
            "summary": {
                "count": 26,
                "has_actionable_drift": True,
                "alerts": ["noncanonical_geo_count=2"],
            },
        }
        body = prod_drift_body(report, "https://example/run/2", "- rows: 26\n")
        self.assertIn(SIG_PROD_DRIFT, body)
        self.assertIn("noncanonical_geo_count=2", body)
        self.assertIn("tokenkey-blue", body)
        self.assertIn("- rows: 26", body)

    def test_prod_recover_body_mentions_clear(self) -> None:
        body = prod_recover_body("https://example/run/3", {"summary": {"count": 10, "has_actionable_drift": False}})
        self.assertIn("no actionable prod fingerprint drift", body)
        self.assertIn("https://example/run/3", body)


if __name__ == "__main__":
    unittest.main()
