"""Unit tests for validate_antigravity_spawn.py"""
from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import validate_antigravity_spawn as mod  # noqa: E402

SAMPLE = (
    "/Applications/Antigravity.app/Contents/Resources/bin/language_server "
    "--standalone --override_ide_name antigravity --subclient_type hub "
    "--override_ide_version 2.2.1 --override_user_agent_name antigravity"
)


class ValidateAntigravitySpawnTest(unittest.TestCase):
    def test_parse_spawn_args(self) -> None:
        args = mod.parse_spawn_args(SAMPLE)
        self.assertEqual(args["override_ide_version"], "2.2.1")
        self.assertEqual(args["subclient_type"], "hub")

    def test_diff_spawn_match(self) -> None:
        baseline = {"ua_version": "2.2.1", "body_user_agent": "antigravity", "ide_type": "ANTIGRAVITY"}
        rows = mod.diff_spawn(baseline, mod.parse_spawn_args(SAMPLE), "2.2.1")
        self.assertFalse(mod.has_drift(rows))


if __name__ == "__main__":
    unittest.main()
