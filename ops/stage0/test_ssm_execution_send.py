#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import pathlib
import re
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parents[2]
MOD_PATH = ROOT / "ops/stage0/ssm_execution.py"
GATE = re.compile(r"ssm\s+send-command\b", re.I)


def load_mod():
    spec = importlib.util.spec_from_file_location("ssm_execution_send_under_test", MOD_PATH)
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class SsmExecutionSendTest(unittest.TestCase):
    def setUp(self) -> None:
        self.mod = load_mod()

    def test_send_uses_injected_runner(self) -> None:
        seen: list[list[str]] = []

        def runner(argv: list[str]):
            seen.append(list(argv))
            return {"Command": {"CommandId": "cid-1"}}

        cid = self.mod.send_run_shell_script(
            region="us-east-1",
            instance_id="mi-abc",
            parameters={"commands": ["true"]},
            comment="t",
            runner=runner,
        )
        self.assertEqual(cid, "cid-1")
        self.assertEqual(seen[0][:3], ["aws", "ssm", "send-command"])
        self.assertIn("--output", seen[0])
        self.assertEqual(seen[0][seen[0].index("--output") + 1], "json")

    def test_public_error_strings_avoid_gate_regex(self) -> None:
        for msg in (
            "SSM SendCommand failed: boom",
            "SSM GetCommandInvocation failed: boom",
            "SSM SendCommand response has no CommandId",
        ):
            self.assertIsNone(GATE.search(msg), msg)

    def test_wait_get_cli_exits_zero_on_remote_failure(self) -> None:
        """Shell callers under set -e must still receive the invocation JSON."""
        inv = {
            "Status": "Failed",
            "ResponseCode": 1,
            "StandardOutputContent": "out",
            "StandardErrorContent": "err",
        }
        with mock.patch.object(self.mod, "wait_and_get", return_value=inv):
            with mock.patch("sys.stdout"):
                rc = self.mod._cli(
                    [
                        "wait-get",
                        "--region",
                        "us-east-1",
                        "--instance-id",
                        "mi-abc",
                        "--command-id",
                        "cid",
                        "--timeout-seconds",
                        "1",
                    ]
                )
        self.assertEqual(rc, 0)


if __name__ == "__main__":
    unittest.main()
