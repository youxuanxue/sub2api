#!/usr/bin/env python3
"""Contract tests for the shared Go cache write policy."""

from __future__ import annotations

from pathlib import Path
import unittest

import yaml


ACTION = Path(__file__).resolve().parents[2] / ".github" / "actions" / "go-rolling-cache" / "action.yml"
MAIN_WRITER_IF = "github.event_name == 'push' && github.ref == 'refs/heads/main'"
NON_MAIN_IF = "github.event_name != 'push' || github.ref != 'refs/heads/main'"


class GoRollingCachePolicyTest(unittest.TestCase):
    def test_only_main_push_steps_can_save_caches(self) -> None:
        steps = yaml.safe_load(ACTION.read_text(encoding="utf-8"))["runs"]["steps"]
        saving = [step for step in steps if step.get("uses") == "actions/cache@v6"]
        self.assertEqual(len(saving), 2)
        self.assertTrue(all(step.get("if") == MAIN_WRITER_IF for step in saving))

    def test_non_main_events_restore_but_never_save_both_cache_layers(self) -> None:
        steps = yaml.safe_load(ACTION.read_text(encoding="utf-8"))["runs"]["steps"]
        restore_only = [step for step in steps if step.get("uses") == "actions/cache/restore@v6"]
        self.assertEqual(len(restore_only), 2)
        self.assertTrue(all(step.get("if") == NON_MAIN_IF for step in restore_only))
        restored_paths = {step["with"]["path"] for step in restore_only}
        self.assertEqual(restored_paths, {"~/go/pkg/mod", "~/.cache/go-build"})


if __name__ == "__main__":
    unittest.main()
