#!/usr/bin/env python3
"""Probe must exit 0 with zero ENV lines when no first-output keys exist."""
from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import unittest

_PROBE = (
    pathlib.Path(__file__).resolve().parent / "probe-first-output-timeouts.sh"
)


class ProbeFirstOutputTimeoutsTest(unittest.TestCase):
    def _run(self, printenv: str) -> subprocess.CompletedProcess[str]:
        tmp = pathlib.Path(tempfile.mkdtemp(prefix="probe-fot-"))
        bin_dir = tmp / "bin"
        bin_dir.mkdir()
        docker = bin_dir / "docker"
        # No textwrap.dedent: interpolated env must not break the heredoc
        # terminator / shebang indentation.
        docker.write_text(
            "#!/usr/bin/env bash\n"
            "set -euo pipefail\n"
            'case "$*" in\n'
            "  *inspect*)\n"
            "    echo 'ghcr.io/o/sub2api:test'\n"
            "    ;;\n"
            "  *)\n"
            "    cat <<'ENV'\n"
            f"{printenv.rstrip()}\n"
            "ENV\n"
            "    ;;\n"
            "esac\n",
            encoding="utf-8",
        )
        docker.chmod(0o755)
        resolver = tmp / "resolve-app-container.sh"
        resolver.write_text(
            "tk_resolve_app_container() { echo tokenkey-blue; }\n",
            encoding="utf-8",
        )
        return subprocess.run(
            ["bash", str(_PROBE)],
            env={
                **os.environ,
                "PATH": f"{bin_dir}:{os.environ['PATH']}",
                "TK_DOCKER": str(docker),
                "TK_RESOLVE_APP_CONTAINER_SH": str(resolver),
            },
            capture_output=True,
            text=True,
            check=False,
        )

    def test_empty_printenv_exits_zero_without_env_lines(self) -> None:
        proc = self._run(printenv="OTHER=1\n")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn('APPCONTAINER {"name":"tokenkey-blue"}', proc.stdout)
        self.assertNotIn('ENV {"key":"GATEWAY_', proc.stdout)

    def test_matching_keys_emitted(self) -> None:
        proc = self._run(
            printenv=(
                "GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS=30\n"
                "GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS=120\n"
                "UNRELATED=x\n"
            )
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn(
            'ENV {"key":"GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS","value":"30"}',
            proc.stdout,
        )
        self.assertIn(
            'ENV {"key":"GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS","value":"120"}',
            proc.stdout,
        )
        self.assertNotIn("UNRELATED", proc.stdout)


if __name__ == "__main__":
    unittest.main()
