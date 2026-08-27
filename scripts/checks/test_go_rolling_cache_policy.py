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
        self.assertEqual(len(saving), 3)
        self.assertTrue(all(MAIN_WRITER_IF in step.get("if", "") for step in saving))

    def test_non_main_events_restore_but_never_save_all_cache_layers(self) -> None:
        steps = yaml.safe_load(ACTION.read_text(encoding="utf-8"))["runs"]["steps"]
        restore_only = [step for step in steps if step.get("uses") == "actions/cache/restore@v6"]
        self.assertEqual(len(restore_only), 3)
        self.assertTrue(all(NON_MAIN_IF in step.get("if", "") for step in restore_only))
        restored_paths = {step["with"]["path"] for step in restore_only}
        self.assertEqual(
            restored_paths,
            {"~/go/pkg/mod", "~/.cache/go-build", "~/.cache/golangci-lint"},
        )

    def test_golangci_cache_is_opt_in_and_rolls_from_main(self) -> None:
        action = yaml.safe_load(ACTION.read_text(encoding="utf-8"))
        self.assertEqual(action["inputs"]["golangci_cache"]["default"], "false")
        steps = action["runs"]["steps"]
        golangci_steps = [
            step
            for step in steps
            if step.get("with", {}).get("path") == "~/.cache/golangci-lint"
        ]
        self.assertEqual(len(golangci_steps), 2)
        for step in golangci_steps:
            with self.subTest(step=step["name"]):
                self.assertIn("inputs.golangci_cache == 'true'", step["if"])
                self.assertIn("github.run_id", step["with"]["key"])
                self.assertIn("backend/go.sum", step["with"]["key"])
                self.assertIn("backend/.golangci.yml", step["with"]["key"])

    def test_build_caches_refresh_weekly_unless_caller_opts_into_backend_fingerprint(self) -> None:
        action = yaml.safe_load(ACTION.read_text(encoding="utf-8"))
        self.assertEqual(action["inputs"]["refresh_on_backend_change"]["default"], "false")
        steps = action["runs"]["steps"]
        epoch_step = next(step for step in steps if step.get("id") == "cache_epoch")
        self.assertIn("date -u +%G-W%V", epoch_step["run"])

        build_steps = [
            step
            for step in steps
            if step.get("with", {}).get("path") == "~/.cache/go-build"
        ]
        self.assertEqual(len(build_steps), 2)
        expected_key = (
            "${{ runner.os }}-gobuild-${{ inputs.prefix }}-"
            "${{ hashFiles('backend/go.mod', 'backend/go.sum', '.new-api-ref') }}-"
            "${{ inputs.refresh_on_backend_change == 'true' && "
            "hashFiles('backend/**', '.new-api-ref') || steps.cache_epoch.outputs.value }}"
        )
        for step in build_steps:
            with self.subTest(step=step["name"]):
                cache_config = step["with"]
                key = cache_config["key"]
                self.assertEqual(key, expected_key)
                self.assertNotIn("github.run_id", key)

                restore_keys = cache_config["restore-keys"].splitlines()
                self.assertEqual(
                    restore_keys[0],
                    "${{ runner.os }}-gobuild-${{ inputs.prefix }}-"
                    "${{ hashFiles('backend/go.mod', 'backend/go.sum', '.new-api-ref') }}-",
                )
                self.assertNotIn("github.run_id", str(cache_config))


if __name__ == "__main__":
    unittest.main()
