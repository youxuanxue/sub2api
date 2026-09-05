#!/usr/bin/env python3
"""Regression tests for the serial OpenRouter inference probe."""
from __future__ import annotations

import importlib.util
import pathlib
import subprocess
import sys
import unittest


_SCRIPT = pathlib.Path(__file__).resolve().parent / "probe-openrouter-provider-inference-serial.py"


def _load_serial_probe():
    name = "or_serial_probe"
    spec = importlib.util.spec_from_file_location(name, _SCRIPT)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    sys.modules[name] = mod  # required before dataclass processing
    spec.loader.exec_module(mod)
    return mod


class SerialProbeStartupTest(unittest.TestCase):
    def test_help_loads_chain_probe_before_parsing(self) -> None:
        result = subprocess.run(
            [sys.executable, str(_SCRIPT), "--help"],
            check=True,
            capture_output=True,
            text=True,
        )
        self.assertIn("--via-ssm", result.stdout)

    def test_route_kind_accepts_schema_24_modality_objects(self) -> None:
        mod = _load_serial_probe()
        self.assertEqual(mod.route_kind([{"type": "text"}]), "chat")
        self.assertEqual(mod.route_kind([{"type": "image"}]), "image")
        self.assertEqual(mod.route_kind([{"type": "video"}]), "video")
        self.assertEqual(mod.route_kind([{"type": "image"}, {"type": "text"}]), "chat")
        # legacy string list still works
        self.assertEqual(mod.route_kind(["image"]), "image")


if __name__ == "__main__":
    unittest.main()
