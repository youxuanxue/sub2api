"""Unit tests for ops/geminicli/capture_gemini_fingerprint.py"""
from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import capture_gemini_fingerprint as mod  # noqa: E402


class CaptureGeminiFingerprintTest(unittest.TestCase):
    def test_diff_match(self) -> None:
        rows = mod.diff_rows("0.50.0", "0.50.0")
        self.assertEqual(rows[0].status, "match")

    def test_diff_mismatch(self) -> None:
        rows = mod.diff_rows("0.49.0", "0.50.0")
        self.assertTrue(mod.has_drift(rows))

    def test_live_repo_aligned(self) -> None:
        if not mod.CONSTANTS_GO.is_file():
            self.skipTest("constants.go missing")
        _, pinned = mod.load_pinned_ua()
        if not pinned:
            self.skipTest("no pinned version")
        # Same pin on both sides: asserts the diff helper, not a frozen release.
        rows = mod.diff_rows(pinned, pinned)
        self.assertFalse(mod.has_drift(rows))


if __name__ == "__main__":
    unittest.main()
