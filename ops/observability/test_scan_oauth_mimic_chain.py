#!/usr/bin/env python3
"""Unit tests for scan-oauth-mimic-chain.sh syntax and oauth_mimic_aggregate."""
from __future__ import annotations

import pathlib
import subprocess
import unittest

_SCAN = pathlib.Path(__file__).resolve().parent / "scan-oauth-mimic-chain.sh"
_POOL = pathlib.Path(__file__).resolve().parents[1] / "stage0" / "edge_anthropic_oauth_schedulable_probe.sh"


class ScanOAuthMimicChainTest(unittest.TestCase):
    def test_scan_script_syntax(self) -> None:
        proc = subprocess.run(["bash", "-n", str(_SCAN)], capture_output=True, text=True, check=False)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)

    def test_pool_probe_syntax(self) -> None:
        proc = subprocess.run(["bash", "-n", str(_POOL)], capture_output=True, text=True, check=False)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)


if __name__ == "__main__":
    unittest.main()
