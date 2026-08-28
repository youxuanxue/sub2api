#!/usr/bin/env python3
"""Contract tests for the shared Go cache write policy."""

from __future__ import annotations

from pathlib import Path
import unittest

import yaml


ACTION = Path(__file__).resolve().parents[2] / ".github" / "actions" / "go-rolling-cache" / "action.yml"
MAIN_WRITER_IF = "github.event_name == 'push' && github.ref == 'refs/heads/main'"
NON_MAIN_IF = "github.event_name != 'push' || github.ref != 'refs/heads/main'"
RESTORE_ONLY_IF = "inputs.save_caches != 'true'"
BENCHMARK_WRITER_IF = "github.event_name == 'workflow_dispatch'"


class GoRollingCachePolicyTest(unittest.TestCase):
    def test_only_main_push_or_explicit_benchmark_steps_can_save_caches(self) -> None:
        action = yaml.safe_load(ACTION.read_text(encoding="utf-8"))
        self.assertEqual(action["inputs"]["save_caches"]["default"], "true")
        self.assertEqual(action["inputs"]["benchmark_build_cache_write"]["default"], "false")
        self.assertEqual(action["inputs"]["benchmark_prefix"]["default"], "")
        steps = action["runs"]["steps"]
        saving = [step for step in steps if step.get("uses") == "actions/cache@v6"]
        self.assertEqual(len(saving), 4)

        benchmark_writers = [
            step
            for step in saving
            if "inputs.benchmark_build_cache_write == 'true'" in step.get("if", "")
        ]
        self.assertEqual(len(benchmark_writers), 1)
        benchmark_writer = benchmark_writers[0]
        self.assertIn(BENCHMARK_WRITER_IF, benchmark_writer["if"])
        self.assertIn("github.ref != 'refs/heads/main'", benchmark_writer["if"])
        self.assertIn("inputs.benchmark_prefix != ''", benchmark_writer["if"])
        self.assertEqual(
            benchmark_writer["with"]["path"],
            "${{ inputs.build_cache_path }}",
        )
        self.assertIn("inputs.benchmark_prefix", benchmark_writer["with"]["key"])

        main_writers = [step for step in saving if step is not benchmark_writer]
        self.assertTrue(
            all(MAIN_WRITER_IF in step.get("if", "") for step in main_writers)
        )
        self.assertTrue(
            all(
                "inputs.save_caches == 'true'" in step.get("if", "")
                for step in main_writers
            )
        )

    def test_non_main_or_restore_only_callers_never_save_any_cache_layer(self) -> None:
        steps = yaml.safe_load(ACTION.read_text(encoding="utf-8"))["runs"]["steps"]
        restore_only = [step for step in steps if step.get("uses") == "actions/cache/restore@v6"]
        self.assertEqual(len(restore_only), 3)
        self.assertTrue(all(NON_MAIN_IF in step.get("if", "") for step in restore_only))
        self.assertTrue(all(RESTORE_ONLY_IF in step.get("if", "") for step in restore_only))
        build_restore = next(
            step
            for step in restore_only
            if step["with"]["path"] == "${{ inputs.build_cache_path }}"
        )
        self.assertIn(
            "inputs.benchmark_build_cache_write != 'true'",
            build_restore["if"],
        )
        restored_paths = {step["with"]["path"] for step in restore_only}
        self.assertEqual(
            restored_paths,
            {
                "~/go/pkg/mod",
                "${{ inputs.build_cache_path }}",
                "~/.cache/golangci-lint",
            },
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

    def test_build_caches_support_daily_refresh_without_changing_other_callers(self) -> None:
        action = yaml.safe_load(ACTION.read_text(encoding="utf-8"))
        self.assertEqual(action["inputs"]["fallback_prefix"]["default"], "")
        self.assertEqual(action["inputs"]["refresh_on_backend_change"]["default"], "false")
        self.assertEqual(action["inputs"]["refresh_daily"]["default"], "false")
        self.assertEqual(
            action["inputs"]["build_cache_path"]["default"],
            "~/.cache/go-build",
        )
        steps = action["runs"]["steps"]
        epoch_step = next(step for step in steps if step.get("id") == "cache_epoch")
        self.assertIn('week=$(date -u +%G-W%V)', epoch_step["run"])
        self.assertIn('day=$(date -u +%Y-%m-%d)', epoch_step["run"])

        build_steps = [
            step
            for step in steps
            if step.get("with", {}).get("path") == "${{ inputs.build_cache_path }}"
        ]
        self.assertEqual(len(build_steps), 3)
        expected_key = (
            "${{ runner.os }}-gobuild-${{ inputs.prefix }}-"
            "${{ hashFiles('backend/go.mod', 'backend/go.sum', '.new-api-ref') }}-"
            "${{ inputs.refresh_on_backend_change == 'true' && "
            "hashFiles('backend/**', '.new-api-ref') || "
            "inputs.refresh_daily == 'true' && steps.cache_epoch.outputs.day || "
            "steps.cache_epoch.outputs.week }}"
        )
        for step in build_steps:
            with self.subTest(step=step["name"]):
                cache_config = step["with"]
                key = cache_config["key"]
                if "benchmark writer" in step["name"]:
                    self.assertEqual(
                        key,
                        expected_key.replace("inputs.prefix", "inputs.benchmark_prefix"),
                    )
                else:
                    self.assertEqual(key, expected_key)
                self.assertNotIn("github.run_id", key)

                restore_keys = cache_config["restore-keys"].splitlines()
                expected_restore_prefix = (
                    "inputs.benchmark_prefix"
                    if "benchmark writer" in step["name"]
                    else "inputs.prefix"
                )
                self.assertEqual(
                    restore_keys[0],
                    "${{ runner.os }}-gobuild-${{ "
                    + expected_restore_prefix
                    + " }}-${{ hashFiles('backend/go.mod', 'backend/go.sum', '.new-api-ref') }}-",
                )
                self.assertNotIn("github.run_id", str(cache_config))

                fallback_keys = [
                    key for key in restore_keys if "inputs.fallback_prefix" in key
                ]
                self.assertEqual(len(fallback_keys), 2)
                self.assertTrue(
                    all("format(" in key for key in fallback_keys),
                    fallback_keys,
                )


if __name__ == "__main__":
    unittest.main()
