#!/usr/bin/env python3
"""Regression guard for upstream-merge tracking issue #1792."""
from __future__ import annotations

import pathlib
import subprocess
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent
CHECK_DRIFT = REPO_ROOT / "scripts/upstream/check-drift.sh"


class UpstreamSyncRegressionTest(unittest.TestCase):
    def test_tokenkey_not_behind_upstream_main(self) -> None:
        self.assertTrue(CHECK_DRIFT.is_file(), f"missing {CHECK_DRIFT}")
        proc = subprocess.run(
            ["bash", str(CHECK_DRIFT)],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            timeout=180,
            check=False,
        )
        combined = proc.stdout + proc.stderr
        self.assertEqual(proc.returncode, 0, combined)
        self.assertIn("TK behind: 0 commits", combined)


if __name__ == "__main__":
    unittest.main()
