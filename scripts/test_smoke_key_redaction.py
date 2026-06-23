#!/usr/bin/env python3
"""Regression tests for Stage0 smoke key redaction."""
from __future__ import annotations

import pathlib
import unittest


REPO_ROOT = pathlib.Path(__file__).resolve().parents[1]


class SmokeKeyRedactionTest(unittest.TestCase):
    def test_stage0_smoke_scripts_do_not_log_key_hints(self) -> None:
        for relpath in (
            "ops/stage0/post_deploy_smoke.sh",
            "ops/stage0/edge_post_deploy_smoke.sh",
        ):
            body = (REPO_ROOT / relpath).read_text()
            with self.subTest(path=relpath):
                self.assertNotIn("key_hint", body)
                self.assertNotIn("head -c 6", body)
                self.assertNotRegex(body, r"tail -c 4(?![0-9])")
                self.assertIn("key=configured", body)


if __name__ == "__main__":
    unittest.main()
