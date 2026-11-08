#!/usr/bin/env python3
"""Behavioral coverage for independent releases and split cutover/completion."""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))
import bluegreen_completion as completion
import prod_release_plan as planner

REPOSITORY = "ghcr.io/youxuanxue/sub2api"


class RawArchiveReleasePlanTest(unittest.TestCase):
    def test_retired_qa_s3_rollout_plans_a_plain_gateway_release(self):
        self.assertEqual(
            planner.release_mode(ROOT / "ops/qa/deploy_rollout.yaml"), "gateway"
        )

    def test_pre_retirement_rollback_target_still_classifies(self):
        # deploy-stage0 reads the rollout from the target tag; tags <= 1.8.353
        # still carry raw_archive_v1 and must remain deployable for rollback.
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "rollout.yaml"
            p.write_text("prod: {component_release: {runtime_contract: raw_archive_v1}}\n")
            self.assertEqual(planner.release_mode(p), "raw_archive")

    def test_cli_publishes_gateway_mode_for_current_rollout(self):
        result = subprocess.run(
            [sys.executable, str(ROOT / "ops/stage0/prod_release_plan.py"),
             "--operation", "classify", "--rollout", str(ROOT / "ops/qa/deploy_rollout.yaml")],
            text=True, capture_output=True, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "mode=gateway\n")

    def test_unknown_contract_fails_closed(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "rollout.yaml"
            p.write_text(
                "prod: {component_release: {runtime_contract: old_worker_v1}}\n"
            )
            with self.assertRaisesRegex(ValueError, "unsupported"):
                planner.release_mode(p)

    def test_missing_and_malformed_contracts_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "rollout.yaml"
            for body in (
                "{}",
                "[]",
                "prod: []",
                "prod: {component_release: []}",
                "prod: {component_release: {runtime_contract: independent_v1}}",
                "prod: {component_release: {runtime_contract: [gateway_only_v1]}}",
            ):
                with self.subTest(body=body):
                    path.write_text(body)
                    with self.assertRaisesRegex(
                        ValueError, "unsupported runtime contract"
                    ):
                        planner.release_mode(path)

    def test_cli_rejects_legacy_before_publishing_workflow_output(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "rollout.yaml"
            path.write_text(
                "prod: {component_release: {runtime_contract: independent_v1}}"
            )
            result = subprocess.run(
                [
                    sys.executable,
                    str(ROOT / "ops/stage0/prod_release_plan.py"),
                    "--operation",
                    "classify",
                    "--rollout",
                    str(path),
                ],
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertEqual(result.returncode, 2)
            self.assertEqual(result.stdout, "")
            self.assertIn("raw archive compatibility", result.stderr)


class BlueGreenCompletionTest(unittest.TestCase):
    token = "a" * 32

    def test_us051_cutover_returns_while_original_deploy_is_draining(self):
        receipt = {
            "token": self.token,
            "tag": "1.8.215",
            "cutover_at": "2026-09-10T02:59:01Z",
        }
        with (
            patch.object(
                completion, "invocation", return_value={"Status": "InProgress"}
            ) as inspect,
            patch.object(
                completion.ssm_execution,
                "run_shell_b64",
                return_value=json.dumps(receipt),
            ),
        ):
            result = completion.wait(
                "instance", "command", self.token, "1.8.215", "cutover", 10
            )
        self.assertEqual(result, receipt["cutover_at"])
        self.assertEqual(inspect.call_count, 1)

    def test_us051_stale_receipt_cannot_claim_cutover(self):
        receipt = {
            "token": "b" * 32,
            "tag": "1.8.215",
            "cutover_at": "2026-09-10T02:59:01Z",
        }
        with self.assertRaises(ValueError):
            completion.validate_receipt(receipt, self.token, "1.8.215")

    def test_us051_completion_waits_and_propagates_drain_failure(self):
        with (
            patch.object(
                completion,
                "invocation",
                side_effect=[
                    {"Status": "InProgress"},
                    {"Status": "Failed", "ResponseCode": 1},
                ],
            ),
            patch.object(completion.time, "sleep"),
        ):
            with self.assertRaises(RuntimeError):
                completion.wait(
                    "instance", "command", self.token, "1.8.215", "complete", 10
                )

    def test_us051_completion_requires_successful_remote_exit(self):
        with patch.object(
            completion,
            "invocation",
            return_value={"Status": "Success", "ResponseCode": 0},
        ):
            self.assertIsNone(
                completion.wait(
                    "instance", "command", self.token, "1.8.215", "complete", 10
                )
            )
        with patch.object(
            completion,
            "invocation",
            return_value={"Status": "Success", "ResponseCode": 1},
        ):
            with self.assertRaises(RuntimeError):
                completion.wait(
                    "instance", "command", self.token, "1.8.215", "complete", 10
                )


if __name__ == "__main__":
    unittest.main()
