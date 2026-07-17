#!/usr/bin/env python3
"""Behavior gates for the Stage0 RDS cutover approval and rollback boundary."""

from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import unittest


REPO = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO / "ops/stage0/cutover_data_layer_via_ssm.sh"


def run_cutover(action: str, *, environment: str = "prod") -> subprocess.CompletedProcess:
    env = os.environ.copy()
    env.update(
        {
            "TK_ENVIRONMENT": environment,
            "TK_DATA_PG_HOST": "candidate.example.rds.amazonaws.com",
        }
    )
    return subprocess.run(
        ["bash", str(SCRIPT), action, "i-test-only"],
        cwd=REPO,
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )


class CutoverDataLayerSafetyTest(unittest.TestCase):
    def test_prod_apply_is_blocked_while_design_is_pending(self) -> None:
        proc = run_cutover("apply")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("production blocked", proc.stderr)
        self.assertIn("status=pending", proc.stderr)
        self.assertNotIn("reading RDS master password", proc.stdout)

    def test_stale_local_rollback_action_is_rejected(self) -> None:
        proc = run_cutover("rollback")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("rollback to stale local PG is intentionally unsupported", proc.stderr)

    def test_nonprod_rehearsal_reaches_runtime_preflight(self) -> None:
        env = os.environ.copy()
        env["TK_ENVIRONMENT"] = "rehearsal"
        env.pop("TK_DATA_PG_HOST", None)
        proc = subprocess.run(
            ["bash", str(SCRIPT), "apply", "i-test-only"],
            cwd=REPO,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("TK_DATA_PG_HOST", proc.stderr)
        self.assertNotIn("production blocked", proc.stderr)

    def test_failed_apply_keeps_overlay_without_safe_abort_marker(self) -> None:
        proc, aws_log = self._run_fake_failed_apply(standard_output="host failed")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("Forward-fix only", proc.stderr)
        self.assertNotIn("delete-parameter", aws_log)

    def test_failed_apply_deletes_overlay_only_with_safe_abort_marker(self) -> None:
        proc, aws_log = self._run_fake_failed_apply(
            standard_output="CUTOVER_ABORTED_BEFORE_RDS_START local restore succeeded"
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("delete-parameter", aws_log)

    def _run_fake_failed_apply(self, *, standard_output: str) -> tuple[subprocess.CompletedProcess, str]:
        with tempfile.TemporaryDirectory() as temp_dir:
            temp = pathlib.Path(temp_dir)
            aws_log = temp / "aws.log"
            fake_aws = temp / "aws"
            fake_aws.write_text(
                """#!/usr/bin/env bash
set -eu
printf '%s\\n' "$*" >> "$FAKE_AWS_LOG"
case "$*" in
  *'ssm get-parameter'*'rds-master-password'*) printf '%s\\n' '0123456789abcdef' ;;
  *'ssm put-parameter'*) : ;;
  *'ssm send-command'*) printf '%s\\n' 'cmd-test' ;;
  *'ssm get-command-invocation'*'Status'*) printf '%s\\n' 'Failed' ;;
  *'ssm get-command-invocation'*'StandardOutputContent'*) printf '%s\\n' "$FAKE_STANDARD_OUTPUT" ;;
  *'ssm get-command-invocation'*'StandardErrorContent'*) printf '%s\\n' 'simulated host failure' ;;
  *'ssm delete-parameter'*) : ;;
  *) printf 'unexpected fake aws call: %s\\n' "$*" >&2; exit 9 ;;
esac
"""
            )
            fake_aws.chmod(0o755)
            env = os.environ.copy()
            env.update(
                {
                    "PATH": f"{temp}:{env['PATH']}",
                    "TK_ENVIRONMENT": "rehearsal",
                    "TK_DATA_PG_HOST": "candidate.example.rds.amazonaws.com",
                    "STAGE0_SSM_OUTPUT_DIR": str(temp / "output"),
                    "FAKE_AWS_LOG": str(aws_log),
                    "FAKE_STANDARD_OUTPUT": standard_output,
                }
            )
            proc = subprocess.run(
                ["bash", str(SCRIPT), "apply", "i-test-only"],
                cwd=REPO,
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            if not aws_log.exists():
                self.fail(
                    "cutover exited before invoking fake aws:\n"
                    f"stdout={proc.stdout}\nstderr={proc.stderr}"
                )
            return proc, aws_log.read_text()


if __name__ == "__main__":
    unittest.main()
