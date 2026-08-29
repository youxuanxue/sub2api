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
FORBIDDEN_KEY_FRAGMENTS = (
    "github.run_id",
    "refresh_daily",
    "refresh_on_backend_change",
    "cache_epoch",
    "%G-W%V",
    "%Y-%m-%d",
)
DEPENDENCY_HASH = "hashFiles('backend/go.mod', 'backend/go.sum', '.new-api-ref')"
SOURCE_HASH = "hashFiles('backend/**/*.go'"


def load_action() -> dict:
    return yaml.safe_load(ACTION.read_text(encoding="utf-8"))


class GoRollingCachePolicyTest(unittest.TestCase):
    def test_exports_only_primary_exact_build_cache_hits(self) -> None:
        action = load_action()
        self.assertEqual(
            action["outputs"]["build_cache_hit"]["value"],
            "${{ steps.build_cache_status.outputs.build_cache_hit }}",
        )
        self.assertEqual(
            action["outputs"]["build_cache_populated"]["value"],
            "${{ steps.build_cache_status.outputs.build_cache_populated }}",
        )
        status = next(
            step
            for step in action["runs"]["steps"]
            if step.get("id") == "build_cache_status"
        )
        self.assertIn("RESTORE_MATCHED", status["env"])
        self.assertIn("cache-matched-key", status["env"]["RESTORE_MATCHED"])
        self.assertIn("build_cache_hit=${hit}", status["run"])
        self.assertIn("build_cache_populated=${populated}", status["run"])
        self.assertNotIn("find ", status["run"])

    def test_uses_family_input_and_drops_date_epochs(self) -> None:
        action = load_action()
        self.assertIn("family", action["inputs"])
        self.assertTrue(action["inputs"]["family"]["required"])
        for removed in (
            "refresh_daily",
            "refresh_on_backend_change",
            "prefix",
        ):
            self.assertNotIn(removed, action["inputs"])
        text = ACTION.read_text(encoding="utf-8")
        for fragment in FORBIDDEN_KEY_FRAGMENTS:
            self.assertNotIn(fragment, text)

    def test_build_keys_are_content_addressed(self) -> None:
        action = load_action()
        version_step = next(
            step for step in action["runs"]["steps"] if step.get("id") == "go_version"
        )
        self.assertIn("backend/go.mod", version_step["run"])
        build_steps = [
            step
            for step in action["runs"]["steps"]
            if "gobuild" in str(step.get("with", {}).get("key", ""))
        ]
        self.assertGreaterEqual(len(build_steps), 2)
        for step in build_steps:
            key = step["with"]["key"]
            self.assertIn("gobuild-${{ inputs.family }}-v1-", key)
            self.assertIn("steps.go_version.outputs.version", key)
            self.assertIn(DEPENDENCY_HASH, key)
            self.assertIn(SOURCE_HASH, key)
            self.assertNotIn("github.run_id", key)
            self.assertNotIn("backend/**'", key)

    def test_only_main_push_or_explicit_benchmark_steps_can_save_caches(self) -> None:
        action = load_action()
        self.assertEqual(action["inputs"]["save_caches"]["default"], "true")
        self.assertEqual(action["inputs"]["benchmark_build_cache_write"]["default"], "false")
        saving = [
            step
            for step in action["runs"]["steps"]
            if step.get("uses") in {"actions/cache@v6", "actions/cache/save@v6"}
        ]
        self.assertTrue(saving)
        benchmark_writers = [
            step
            for step in saving
            if "inputs.benchmark_build_cache_write == 'true'" in step.get("if", "")
        ]
        self.assertEqual(len(benchmark_writers), 1)
        main_writers = [step for step in saving if step not in benchmark_writers]
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
        restore_only = [
            step
            for step in load_action()["runs"]["steps"]
            if step.get("uses") == "actions/cache/restore@v6"
        ]
        self.assertTrue(restore_only)
        self.assertTrue(all(NON_MAIN_IF in step.get("if", "") for step in restore_only))
        self.assertTrue(all(RESTORE_ONLY_IF in step.get("if", "") for step in restore_only))

    def test_analysis_family_owns_golangci_cache_without_run_id(self) -> None:
        text = ACTION.read_text(encoding="utf-8")
        self.assertIn("~/.cache/golangci-lint", text)
        self.assertIn("inputs.family == 'analysis'", text)
        self.assertNotIn("Linux-golangci-", text)
        self.assertNotIn("github.run_id", text)

    def test_non_analysis_build_path_is_a_single_entry(self) -> None:
        # actions/cache versions by the path list. A dummy second copy of
        # build_cache_path made required CI miss warm's single-path archives.
        text = ACTION.read_text(encoding="utf-8")
        self.assertNotIn(
            "&& '~/.cache/golangci-lint' || inputs.build_cache_path",
            text,
        )
        self.assertIn(
            "format('{0}\\n~/.cache/golangci-lint', inputs.build_cache_path)",
            text,
        )


if __name__ == "__main__":
    unittest.main()
