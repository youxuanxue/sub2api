#!/usr/bin/env python3
"""Tests for ops/stage0/assert-first-output-timeouts.sh transport glue.

Stubs run-probe so the wrapper can be exercised without AWS. Verdict logic is
covered by live_host_state_verdict.py / first_output_timeout_ssot.
"""
from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

_STAGE0 = pathlib.Path(__file__).resolve().parent
_SCRIPT = _STAGE0 / "assert-first-output-timeouts.sh"
_SSOT = _STAGE0 / "first_output_timeout_ssot.py"


def _expected_env_lines() -> str:
    import importlib.util

    spec = importlib.util.spec_from_file_location("fot_ssot", _SSOT)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    lines = ['APPCONTAINER {"name":"tokenkey-blue"}', 'RUNIMAGE {"image":"ghcr.io/o/sub2api:1.0.0"}']
    for key, value in mod.EXPECTED_ENV.items():
        lines.append(f'ENV {{"key":"{key}","value":"{value}"}}')
    return "\n".join(lines) + "\n"


class AssertFirstOutputTimeoutsTransportTest(unittest.TestCase):
    def _run(
        self,
        *,
        target: str,
        probe_stdout: str,
        probe_rc: int = 0,
    ) -> subprocess.CompletedProcess[str]:
        tmp = pathlib.Path(tempfile.mkdtemp(prefix="assert-fot-"))
        fake_bin = tmp / "bin"
        fake_bin.mkdir()
        repo = tmp / "repo"
        obs = repo / "ops" / "observability"
        stage0 = repo / "ops" / "stage0"
        obs.mkdir(parents=True)
        stage0.mkdir(parents=True)

        # Minimal shim tree: assert script + real verdict/ssot + fake run-probe.
        (stage0 / "assert-first-output-timeouts.sh").write_text(
            _SCRIPT.read_text(encoding="utf-8"), encoding="utf-8"
        )
        for name in (
            "live_host_state_verdict.py",
            "first_output_timeout_ssot.py",
        ):
            (stage0 / name).write_text(
                (_STAGE0 / name).read_text(encoding="utf-8"), encoding="utf-8"
            )
        (obs / "probe-first-output-timeouts.sh").write_text(
            "#!/bin/true\n", encoding="utf-8"
        )
        (obs / "run-probe.sh").write_text(
            textwrap.dedent(
                f"""\
                #!/usr/bin/env bash
                set -euo pipefail
                # swallow args; emit fixture stdout
                cat <<'OUT'
                {probe_stdout.rstrip()}
                OUT
                exit {probe_rc}
                """
            ),
            encoding="utf-8",
        )
        (obs / "run-probe.sh").chmod(0o755)
        (stage0 / "assert-first-output-timeouts.sh").chmod(0o755)

        proc = subprocess.run(
            [
                "bash",
                str(stage0 / "assert-first-output-timeouts.sh"),
                "--target",
                target,
                "--comment",
                "unit-test",
            ],
            env={**os.environ, "PATH": f"{fake_bin}:{os.environ['PATH']}"},
            capture_output=True,
            text=True,
            check=False,
        )
        return proc

    def test_clean_prod_target_ok(self) -> None:
        proc = self._run(target="prod", probe_stdout=_expected_env_lines())
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn("OK: live host matches intended state", proc.stdout)
        self.assertNotIn("::warning::first-output drift", proc.stdout)

    def test_drift_emits_warning_but_exits_zero(self) -> None:
        bad = _expected_env_lines().replace(
            'GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS","value":"120"',
            'GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS","value":"15"',
        )
        proc = self._run(target="prod", probe_stdout=bad)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn("DRIFT:", proc.stdout)
        self.assertIn("::warning::first-output drift target=prod:", proc.stdout)
        self.assertIn("GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS", proc.stdout)

    def test_missing_all_keys_is_drift_not_transport_skip(self) -> None:
        # Probe may emit only container/image lines when grep matches nothing.
        empty = (
            'APPCONTAINER {"name":"tokenkey-blue"}\n'
            'RUNIMAGE {"image":"ghcr.io/o/sub2api:1.0.0"}\n'
        )
        proc = self._run(target="prod", probe_stdout=empty)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn("DRIFT:", proc.stdout)
        self.assertIn("::warning::first-output drift target=prod:", proc.stdout)
        self.assertNotIn("transport failed", proc.stdout)

    def test_transport_failure_warns_and_skips(self) -> None:
        proc = self._run(target="prod", probe_stdout="", probe_rc=2)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn("::warning::first-output assert transport failed", proc.stdout)

    def test_usage_without_target(self) -> None:
        proc = subprocess.run(
            ["bash", str(_SCRIPT)],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 2)

if __name__ == "__main__":
    unittest.main()
