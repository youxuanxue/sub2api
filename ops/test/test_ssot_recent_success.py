#!/usr/bin/env python3
"""Unit tests for SSOT recent-success skip key parsing."""
from __future__ import annotations

import unittest

from ssot_recent_success import billing_mode_to_modality, parse_recent_success_tsv


class TestSSOTRecentSuccess(unittest.TestCase):
    def test_billing_mode_to_modality(self) -> None:
        self.assertEqual(billing_mode_to_modality("token", "gpt-5"), "text")
        self.assertEqual(billing_mode_to_modality("image", "seedream"), "image")
        self.assertEqual(billing_mode_to_modality("video", "seedance"), "video")
        self.assertEqual(billing_mode_to_modality("token", "text-embedding-3-small"), "embeddings")

    def test_parse_recent_success_tsv(self) -> None:
        keys = parse_recent_success_tsv(
            "# comment\nclaude-sonnet-4-6\ttext\t12\nseedream-5\timage\t3\n",
            min_count=5,
        )
        self.assertIn(("claude-sonnet-4-6", "text"), keys)
        self.assertNotIn(("seedream-5", "image"), keys)


if __name__ == "__main__":
    unittest.main()
