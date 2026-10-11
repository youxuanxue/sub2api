#!/usr/bin/env python3
"""Unit tests for ops/stage0/ssm_wrap_bash_commands.py."""

from __future__ import annotations

import base64
import importlib.util
import json
import pathlib
import subprocess
import tempfile
import unittest

_HERE = pathlib.Path(__file__).resolve().parent
_SCRIPT = _HERE / "ssm_wrap_bash_commands.py"


def _load_wrap_module():
    spec = importlib.util.spec_from_file_location("ssm_wrap_bash_commands", _SCRIPT)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {_SCRIPT}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class SsmWrapBashCommandsTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.mod = _load_wrap_module()

    def test_wraps_multi_command_payload(self) -> None:
        wrapped = self.mod.wrap_commands(["set -euo pipefail", "echo hi"])
        self.assertEqual(len(wrapped), 1)
        self.assertTrue(wrapped[0].startswith("echo "))
        self.assertTrue(wrapped[0].endswith(" | base64 -d | bash -s"))
        b64 = wrapped[0][len("echo ") : -len(" | base64 -d | bash -s")]
        self.assertEqual(base64.b64decode(b64).decode(), "set -euo pipefail\necho hi")

    def test_idempotent_when_already_wrapped(self) -> None:
        once = self.mod.wrap_commands(["set -euo pipefail"])
        twice = self.mod.wrap_commands(once)
        self.assertEqual(once, twice)

    def test_unwrap_round_trip(self) -> None:
        original = ["set -euo pipefail", "echo hi"]
        self.assertEqual(self.mod.unwrap_commands(self.mod.wrap_commands(original)), original)

    def test_unwrap_stdout_prints_real_script_so_lint_guards_still_work(self) -> None:
        """The preflight host-parse guard lints `--unwrap-stdout` output.

        Wrapping collapses the array into one trivially-parseable line, which
        would silently neuter that guard. This asserts the real host script
        comes back out, syntax error and all.
        """
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "ssm-params.json"
            # #512 bug shape: unquoted parens inside echo.
            bad = ["set -euo pipefail", "echo === sync (kind=$KIND) ==="]
            path.write_text(json.dumps({"commands": bad}))
            subprocess.run(["python3", str(_SCRIPT), str(path)], check=True)
            self.assertEqual(len(json.loads(path.read_text())["commands"]), 1)

            proc = subprocess.run(
                ["python3", str(_SCRIPT), "--unwrap-stdout", str(path)],
                capture_output=True, text=True, check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertEqual(proc.stdout.rstrip("\n").split("\n"), bad)
            # params file must be untouched by the read-only mode
            self.assertEqual(len(json.loads(path.read_text())["commands"]), 1)
            lint = subprocess.run(["bash", "-n"], input=proc.stdout, text=True, capture_output=True)
            self.assertNotEqual(lint.returncode, 0, "unwrapped lint must surface the syntax error")

    def test_unwrap_stdout_is_noop_on_unwrapped_array(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "ssm-params.json"
            plain = ["set -euo pipefail", "echo ok"]
            path.write_text(json.dumps({"commands": plain}))
            proc = subprocess.run(
                ["python3", str(_SCRIPT), "--unwrap-stdout", str(path)],
                capture_output=True, text=True, check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertEqual(proc.stdout.rstrip("\n").split("\n"), plain)

    def test_cli_rewrites_params_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "ssm-params.json"
            path.write_text(json.dumps({"commands": ["set -euo pipefail", "true"]}))
            proc = subprocess.run(
                ["python3", str(_SCRIPT), str(path)],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            payload = json.loads(path.read_text())
            self.assertEqual(len(payload["commands"]), 1)
            self.assertIn("base64 -d | bash -s", payload["commands"][0])


if __name__ == "__main__":
    unittest.main()
