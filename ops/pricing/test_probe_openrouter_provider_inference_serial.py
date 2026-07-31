#!/usr/bin/env python3
"""Regression tests for the serial OpenRouter inference probe."""
from __future__ import annotations

import pathlib
import subprocess
import sys
import unittest


_SCRIPT = pathlib.Path(__file__).resolve().parent / "probe-openrouter-provider-inference-serial.py"


class SerialProbeStartupTest(unittest.TestCase):
    def test_help_loads_chain_probe_before_parsing(self) -> None:
        result = subprocess.run(
            [sys.executable, str(_SCRIPT), "--help"],
            check=True,
            capture_output=True,
            text=True,
        )
        self.assertIn("--via-ssm", result.stdout)


if __name__ == "__main__":
    unittest.main()
