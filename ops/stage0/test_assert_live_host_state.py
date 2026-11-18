#!/usr/bin/env python3
"""Tests for ops/stage0/assert-live-host-state.sh transport glue."""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

from first_output_timeout_ssot import EXPECTED_ENV

_SCRIPT = pathlib.Path(__file__).resolve().parent / "assert-live-host-state.sh"


class AssertLiveHostStateTransportTest(unittest.TestCase):
    def _fake_execute(self, tmp: pathlib.Path, *, transport_rc: int = 0) -> pathlib.Path:
        timeout_lines = "\n".join(
            f'ENV {json.dumps({"key": key, "value": value})}'
            for key, value in EXPECTED_ENV.items()
        )
        fake_exec = tmp / "execute_remote_params.py"
        body = (
            'APPCONTAINER {"name":"tokenkey-blue"}\n'
            'RUNIMAGE {"image":"ghcr.io/youxuanxue/sub2api:1.8.40"}\n'
            'ENV {"key":"SERVER_FRONTEND_URL","value":"https://api.tokenkey.dev"}\n'
            + timeout_lines
            + '\nRETENTION {"value":"1.5"}\n'
        )
        fake_exec.write_text(
            textwrap.dedent(
                f"""\
                #!/usr/bin/env python3
                import sys
                stdout_file = None
                args = sys.argv[1:]
                for idx, item in enumerate(args):
                    if item == "--stdout-file" and idx + 1 < len(args):
                        stdout_file = args[idx + 1]
                body = {body!r}
                if stdout_file:
                    open(stdout_file, "w", encoding="utf-8").write(body)
                sys.exit({transport_rc})
                """
            ),
            encoding="utf-8",
        )
        fake_exec.chmod(0o755)
        return fake_exec

    def _run_with_fake_transport(
        self, *, expected_tag: str | None = "1.8.40", transport_rc: int = 0
    ) -> subprocess.CompletedProcess[str]:
        with tempfile.TemporaryDirectory() as directory:
            tmp = pathlib.Path(directory)
            fake_exec = self._fake_execute(tmp, transport_rc=transport_rc)
            env = {
                **os.environ,
                "STAGE0_EXECUTE_REMOTE_PARAMS": str(fake_exec),
                "SSM_TIMEOUT_SECONDS": "5",
            }
            env.pop("AWS_REGION", None)
            env.pop("AWS_DEFAULT_REGION", None)
            cmd = ["bash", str(_SCRIPT), "i-test"]
            if expected_tag is not None:
                cmd.append(expected_tag)
            return subprocess.run(cmd, env=env, cwd=str(tmp), capture_output=True, text=True, check=False)

    def test_daily_audit_mode_without_unbound_array(self) -> None:
        proc = self._run_with_fake_transport()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIn("OK: live host matches intended state", proc.stdout)
        self.assertNotIn("unbound variable", proc.stderr)

    def test_no_expected_tag_runs_daily_audit_mode(self) -> None:
        proc = self._run_with_fake_transport(expected_tag=None)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIn("OK: live host matches intended state", proc.stdout)

    def test_transport_failure_is_advisory(self) -> None:
        proc = self._run_with_fake_transport(transport_rc=1)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIn("skipping verdict", proc.stdout)


class LiveCollectorTest(unittest.TestCase):
    def test_probe_script_parses_under_bash_n(self) -> None:
        probe = pathlib.Path(__file__).with_name("probe-live-host-state-collection.sh")
        proc = subprocess.run(["bash", "-n", str(probe)], capture_output=True, text=True)
        self.assertEqual(proc.returncode, 0, proc.stderr)


if __name__ == "__main__":
    unittest.main()