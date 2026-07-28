"""Tests for scripts/checks/public-group-aggregator-channel.py."""
from __future__ import annotations

import importlib.util
import pathlib
import subprocess
import sys
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts/checks/public-group-aggregator-channel.py"


class PublicGroupAggregatorChannelTests(unittest.TestCase):
    def test_repo_passes(self):
        proc = subprocess.run([sys.executable, str(SCRIPT)], capture_output=True, text=True, check=False)
        self.assertEqual(proc.returncode, 0, proc.stderr or proc.stdout)


if __name__ == "__main__":
    unittest.main()
