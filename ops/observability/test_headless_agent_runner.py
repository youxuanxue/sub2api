#!/usr/bin/env python3
from __future__ import annotations

import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
RUNNER = ROOT / "scripts" / "agent" / "run-headless-claude.sh"
REDACTOR = ROOT / "scripts" / "agent" / "redact-stream.py"


class HeadlessAgentRunnerTest(unittest.TestCase):
    def test_large_prompt_is_streamed_over_stdin(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_raw:
            tmp = pathlib.Path(tmp_raw)
            bin_dir = tmp / "bin"
            bin_dir.mkdir()
            prompt = tmp / "prompt.txt"
            captured = tmp / "captured-prompt.txt"
            args = tmp / "claude-args.txt"
            output = tmp / "agent.jsonl"
            github_output = tmp / "github-output"
            prompt.write_bytes(b"large-prompt-line\n" * 13_000)
            shutil.copyfile(REDACTOR, tmp / "redact-agent-stream.py")

            claude = bin_dir / "claude"
            claude.write_text(
                """#!/usr/bin/env bash
set -euo pipefail
printf '%s\\n' "$@" > "$CLAUDE_ARGS_FILE"
cat > "$CLAUDE_STDIN_FILE"
printf '%s\\n' '{"type":"result","result":"ok"}'
exit "${CLAUDE_STUB_EXIT:-0}"
""",
                encoding="utf-8",
            )
            claude.chmod(0o755)

            env = {
                "PATH": f"{bin_dir}:{os.environ['PATH']}",
                "PROMPT_FILE": str(prompt),
                "ANTHROPIC_MODEL": "test-model",
                "MAX_BUDGET_USD": "1.00",
                "ALLOWED_TOOLS": "Read Grep Glob",
                "OUTPUT_FILE": str(output),
                "RUNNER_TEMP": str(tmp),
                "GITHUB_OUTPUT": str(github_output),
                "FAIL_ON_ERROR": "true",
                "CLAUDE_ARGS_FILE": str(args),
                "CLAUDE_STDIN_FILE": str(captured),
            }
            proc = subprocess.run(
                ["bash", str(RUNNER)],
                cwd=ROOT,
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )

            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertGreater(prompt.stat().st_size, 131_072)
            self.assertEqual(captured.read_bytes(), prompt.read_bytes())
            self.assertEqual(
                args.read_text(encoding="utf-8").splitlines(),
                [
                    "-p",
                    "--model",
                    "test-model",
                    "--max-budget-usd",
                    "1.00",
                    "--allowedTools",
                    "Read Grep Glob",
                    "--input-format",
                    "text",
                    "--output-format",
                    "stream-json",
                    "--verbose",
                    "--exclude-dynamic-system-prompt-sections",
                ],
            )
            self.assertEqual(output.read_text(encoding="utf-8"), '{"type":"result","result":"ok"}\n')
            self.assertEqual(github_output.read_text(encoding="utf-8"), "exit_code=0\n")

    def test_nonzero_exit_is_exported_when_failure_is_allowed(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_raw:
            tmp = pathlib.Path(tmp_raw)
            bin_dir = tmp / "bin"
            bin_dir.mkdir()
            prompt = tmp / "prompt.txt"
            prompt.write_text("test", encoding="utf-8")
            shutil.copyfile(REDACTOR, tmp / "redact-agent-stream.py")
            claude = bin_dir / "claude"
            claude.write_text(
                "#!/usr/bin/env bash\ncat >/dev/null\nexit 7\n",
                encoding="utf-8",
            )
            claude.chmod(0o755)
            github_output = tmp / "github-output"
            env = {
                "PATH": f"{bin_dir}:{os.environ['PATH']}",
                "PROMPT_FILE": str(prompt),
                "ANTHROPIC_MODEL": "test-model",
                "MAX_BUDGET_USD": "1.00",
                "ALLOWED_TOOLS": "Read",
                "OUTPUT_FILE": str(tmp / "agent.jsonl"),
                "RUNNER_TEMP": str(tmp),
                "GITHUB_OUTPUT": str(github_output),
                "FAIL_ON_ERROR": "false",
            }
            proc = subprocess.run(
                ["bash", str(RUNNER)], cwd=ROOT, env=env, capture_output=True, text=True, check=False
            )

            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertEqual(github_output.read_text(encoding="utf-8"), "exit_code=7\n")

    def test_redactor_failure_is_fail_closed_when_claude_succeeds(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_raw:
            tmp = pathlib.Path(tmp_raw)
            bin_dir = tmp / "bin"
            bin_dir.mkdir()
            prompt = tmp / "prompt.txt"
            prompt.write_text("test", encoding="utf-8")
            (tmp / "redact-agent-stream.py").write_text(
                "raise SystemExit(9)\n",
                encoding="utf-8",
            )
            claude = bin_dir / "claude"
            claude.write_text(
                "#!/usr/bin/env bash\ncat >/dev/null\nexit 0\n",
                encoding="utf-8",
            )
            claude.chmod(0o755)
            github_output = tmp / "github-output"
            env = {
                "PATH": f"{bin_dir}:{os.environ['PATH']}",
                "PROMPT_FILE": str(prompt),
                "ANTHROPIC_MODEL": "test-model",
                "MAX_BUDGET_USD": "1.00",
                "ALLOWED_TOOLS": "Read",
                "OUTPUT_FILE": str(tmp / "agent.jsonl"),
                "RUNNER_TEMP": str(tmp),
                "GITHUB_OUTPUT": str(github_output),
                "FAIL_ON_ERROR": "false",
            }
            proc = subprocess.run(
                ["bash", str(RUNNER)], cwd=ROOT, env=env, capture_output=True, text=True, check=False
            )

            self.assertEqual(proc.returncode, 9, proc.stderr)
            self.assertIn("headless agent redactor exited 9", proc.stdout)
            self.assertEqual(github_output.read_text(encoding="utf-8"), "exit_code=0\n")

    def test_output_failure_is_not_masked_when_claude_succeeds(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_raw:
            tmp = pathlib.Path(tmp_raw)
            bin_dir = tmp / "bin"
            bin_dir.mkdir()
            prompt = tmp / "prompt.txt"
            prompt.write_text("test", encoding="utf-8")
            shutil.copyfile(REDACTOR, tmp / "redact-agent-stream.py")
            claude = bin_dir / "claude"
            claude.write_text(
                "#!/usr/bin/env bash\ncat >/dev/null\nexit 0\n",
                encoding="utf-8",
            )
            claude.chmod(0o755)
            github_output = tmp / "github-output"
            env = {
                "PATH": f"{bin_dir}:{os.environ['PATH']}",
                "PROMPT_FILE": str(prompt),
                "ANTHROPIC_MODEL": "test-model",
                "MAX_BUDGET_USD": "1.00",
                "ALLOWED_TOOLS": "Read",
                "OUTPUT_FILE": str(tmp),
                "RUNNER_TEMP": str(tmp),
                "GITHUB_OUTPUT": str(github_output),
                "FAIL_ON_ERROR": "false",
            }
            proc = subprocess.run(
                ["bash", str(RUNNER)], cwd=ROOT, env=env, capture_output=True, text=True, check=False
            )

            self.assertNotEqual(proc.returncode, 0)
            self.assertIn("headless agent output writer exited", proc.stdout)
            self.assertEqual(github_output.read_text(encoding="utf-8"), "exit_code=0\n")


if __name__ == "__main__":
    unittest.main()
