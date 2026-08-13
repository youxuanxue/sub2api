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

    def test_live_repo_owner_is_aligned_with_itself(self) -> None:
        self.assertTrue(mod.BILLING_GO.is_file())
        pinned = mod.load_pinned_version()
        self.assertRegex(pinned, r"^\d+\.\d+\.\d+")
        rows = mod.diff_rows(pinned, pinned)
        self.assertFalse(mod.has_drift(rows))
        self.assertEqual(rows[0].field, "cli_client_version")


if __name__ == "__main__":
    unittest.main()
