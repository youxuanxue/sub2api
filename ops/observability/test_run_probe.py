#!/usr/bin/env python3
"""Tests for ops/observability/run-probe.sh without live AWS calls.

Argument validation runs directly. Polling tests replace aws/date/sleep on PATH
to cover the SSM lifecycle deterministically; live delivery remains an operator
integration check.

stdlib-only.
"""
from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "run-probe.sh"


def _run(
    *args: str, env: dict[str, str] | None = None
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["bash", str(_SCRIPT), *args],
        capture_output=True,
        text=True,
        check=False,
        env=env,
    )


class RunProbeValidationTest(unittest.TestCase):
    def test_help_exits_zero(self) -> None:
        proc = _run("--help")
        self.assertEqual(proc.returncode, 0)
        self.assertIn("run-probe.sh", proc.stdout)
        self.assertIn("--target", proc.stdout)

    def test_missing_required_args(self) -> None:
        proc = _run()
        self.assertEqual(proc.returncode, 1)
        self.assertIn("--target and --script are required", proc.stderr)

    def test_unknown_arg_rejected(self) -> None:
        proc = _run("--bogus")
        self.assertEqual(proc.returncode, 1)
        self.assertIn("unknown arg", proc.stderr)

    def test_script_must_exist(self) -> None:
        proc = _run("--target", "prod", "--script", "/nonexistent/probe.sh")
        self.assertEqual(proc.returncode, 1)
        self.assertIn("script not found", proc.stderr)

    def test_invalid_target_shape(self) -> None:
        # Need an existing script to pass the prior check
        existing = pathlib.Path(__file__).resolve().parent / "probe-caps.sh"
        proc = _run("--target", "weird", "--script", str(existing))
        self.assertEqual(proc.returncode, 1)
        self.assertIn("--target must be", proc.stderr)

    def test_timeout_must_match_aws_range(self) -> None:
        existing = pathlib.Path(__file__).resolve().parent / "probe-caps.sh"
        for value in ("not-a-number", "29", "2592001"):
            with self.subTest(value=value):
                proc = _run(
                    "--target",
                    "prod",
                    "--script",
                    str(existing),
                    "--timeout-seconds",
                    value,
                )
                self.assertEqual(proc.returncode, 1)
                self.assertIn("integer from 30 to 2592000", proc.stderr)


class RunProbePollingTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temporary.name)
        self.bin_dir = self.root / "bin"
        self.bin_dir.mkdir()
        self.probe = self.root / "probe.sh"
        self._write_executable(self.probe, "#!/usr/bin/env bash\necho unused\n")

        self.aws_log = self.root / "aws.log"
        self.aws_state = self.root / "aws-state"
        self.date_state = self.root / "date-state"
        self._write_executable(
            self.bin_dir / "uname",
            "#!/usr/bin/env bash\necho Linux\n",
        )
        self._write_executable(
            self.bin_dir / "sleep",
            "#!/usr/bin/env bash\nexit 0\n",
        )
        self._write_executable(
            self.bin_dir / "date",
            textwrap.dedent(
                """\
                #!/usr/bin/env python3
                import os
                from pathlib import Path

                state = Path(os.environ["FAKE_DATE_STATE"])
                current = int(state.read_text() if state.exists() else "100")
                print(current)
                step = int(os.environ.get("FAKE_DATE_STEP", "1"))
                state.write_text(str(current + step))
                """
            ),
        )
        self._write_executable(
            self.bin_dir / "aws",
            textwrap.dedent(
                """\
                #!/usr/bin/env python3
                import json
                import os
                import sys
                from pathlib import Path

                args = sys.argv[1:]
                operation = " ".join(args[:2])
                command_id = ""
                if "--command-id" in args:
                    command_id = args[args.index("--command-id") + 1]
                log = Path(os.environ["FAKE_AWS_LOG"])
                with log.open("a", encoding="utf-8") as handle:
                    handle.write(operation + "\\t" + command_id + "\\n")

                if operation == "cloudformation describe-stacks":
                    print("i-test")
                    raise SystemExit(0)
                if operation == "cloudformation describe-stack-resources":
                    print("i-test")
                    raise SystemExit(0)
                if operation == "ssm send-command":
                    print("11111111-2222-3333-4444-555555555555")
                    raise SystemExit(0)
                if operation != "ssm get-command-invocation":
                    print(f"unexpected fake aws operation: {operation}", file=sys.stderr)
                    raise SystemExit(99)

                state = Path(os.environ["FAKE_AWS_STATE"])
                invocation = int(state.read_text() if state.exists() else "0") + 1
                state.write_text(str(invocation))
                scenario = os.environ["FAKE_AWS_SCENARIO"]
                if scenario == "eventual-success":
                    if invocation == 1:
                        print(
                            "An error occurred (InvocationDoesNotExist) when calling "
                            "the GetCommandInvocation operation",
                            file=sys.stderr,
                        )
                        raise SystemExit(255)
                    status = {2: "Pending", 3: "InProgress"}.get(
                        invocation, "Success"
                    )
                    stdout = "probe-result" if status == "Success" else ""
                    stderr = "probe-warning" if status == "Success" else ""
                elif scenario == "terminal-failure":
                    status = "Failed"
                    stdout = "partial-result"
                    stderr = "probe-failed"
                elif scenario == "timeout":
                    status = "Pending"
                    stdout = ""
                    stderr = ""
                else:
                    print(f"unexpected fake aws scenario: {scenario}", file=sys.stderr)
                    raise SystemExit(99)

                print(json.dumps({
                    "Status": status,
                    "StandardOutputContent": stdout,
                    "StandardErrorContent": stderr,
                }))
                """
            ),
        )

    def tearDown(self) -> None:
        self.temporary.cleanup()

    @staticmethod
    def _write_executable(path: pathlib.Path, content: str) -> None:
        path.write_text(content, encoding="utf-8")
        path.chmod(0o755)

    def _run_scenario(
        self,
        scenario: str,
        *,
        timeout_seconds: int | str = 30,
        date_step: int = 1,
    ) -> subprocess.CompletedProcess[str]:
        env = os.environ.copy()
        env.update(
            {
                "PATH": f"{self.bin_dir}{os.pathsep}{env['PATH']}",
                "FAKE_AWS_LOG": str(self.aws_log),
                "FAKE_AWS_STATE": str(self.aws_state),
                "FAKE_AWS_SCENARIO": scenario,
                "FAKE_DATE_STATE": str(self.date_state),
                "FAKE_DATE_STEP": str(date_step),
            }
        )
        return _run(
            "--target",
            "prod",
            "--script",
            str(self.probe),
            "--timeout-seconds",
            str(timeout_seconds),
            env=env,
        )

    def _aws_calls(self) -> list[tuple[str, str]]:
        calls: list[tuple[str, str]] = []
        for line in self.aws_log.read_text(encoding="utf-8").splitlines():
            operation, command_id = line.split("\t", 1)
            calls.append((operation, command_id))
        return calls

    def _assert_one_command(self, expected_gets: int) -> None:
        calls = self._aws_calls()
        operations = [operation for operation, _ in calls]
        self.assertEqual(operations.count("ssm send-command"), 1)
        self.assertEqual(
            operations.count("ssm get-command-invocation"), expected_gets
        )
        command_ids = [
            command_id
            for operation, command_id in calls
            if operation == "ssm get-command-invocation"
        ]
        self.assertEqual(
            command_ids,
            ["11111111-2222-3333-4444-555555555555"] * expected_gets,
        )
        self.assertNotIn("ssm wait", operations)

    def test_eventual_consistency_and_pending_states_reach_success(self) -> None:
        proc = self._run_scenario("eventual-success")

        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(proc.stdout, "probe-result\n")
        self.assertIn("[run-probe] status=Success", proc.stderr)
        self.assertIn("[remote-stderr] probe-warning", proc.stderr)
        self._assert_one_command(expected_gets=4)

    def test_terminal_failure_returns_remote_failure(self) -> None:
        proc = self._run_scenario("terminal-failure")

        self.assertEqual(proc.returncode, 3)
        self.assertEqual(proc.stdout, "partial-result\n")
        self.assertIn("[remote-stderr] probe-failed", proc.stderr)
        self.assertIn("[run-probe] ERROR: remote status=Failed", proc.stderr)
        self._assert_one_command(expected_gets=1)

    def test_poll_deadline_does_not_resubmit_command(self) -> None:
        proc = self._run_scenario("timeout", timeout_seconds=30, date_step=15)

        self.assertEqual(proc.returncode, 3)
        self.assertIn("[run-probe] status=Pending", proc.stderr)
        self.assertIn("[run-probe] ERROR: polling timed out", proc.stderr)
        self._assert_one_command(expected_gets=2)

    def test_leading_zero_timeout_is_normalized_before_arithmetic(self) -> None:
        proc = self._run_scenario("terminal-failure", timeout_seconds="030")

        self.assertEqual(proc.returncode, 3)
        self.assertIn("[run-probe] status=Failed", proc.stderr)
        self._assert_one_command(expected_gets=1)


if __name__ == "__main__":
    unittest.main()
