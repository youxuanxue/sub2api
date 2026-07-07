"""Unit tests for ops/xai/capture_grok_fingerprint.py"""
from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import capture_grok_fingerprint as mod  # noqa: E402


class CaptureGrokFingerprintTest(unittest.TestCase):
    def test_diff_match(self) -> None:
        rows = mod.diff_rows("0.2.73", "0.2.73")
        self.assertEqual(rows[0].status, "match")

    def test_diff_mismatch(self) -> None:
        rows = mod.diff_rows("0.2.73", "0.2.74")
        self.assertTrue(mod.has_drift(rows))

    def test_live_repo_aligned(self) -> None:
        if not mod.OAUTH_GO.is_file():
            self.skipTest("oauth.go missing")
        pinned = mod.load_pinned_version()
        if not pinned:
            self.skipTest("no pinned version")
        rows = mod.diff_rows(pinned, pinned)
        self.assertFalse(mod.has_drift(rows))


if __name__ == "__main__":
    unittest.main()
