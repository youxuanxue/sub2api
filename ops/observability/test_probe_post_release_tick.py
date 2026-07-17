#!/usr/bin/env python3
"""Validation tests for probe-post-release-tick.sh.

The script normally runs on the prod host via SSM and reads Docker logs. These
tests fake only the docker CLI and active-color file so the blue/green container
resolution contract is pinned without AWS or Docker.
"""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "probe-post-release-tick.sh"


class ProbePostReleaseTickTest(unittest.TestCase):
    def test_syntax_clean(self) -> None:
        proc = subprocess.run(
            ["bash", "-n", str(_SCRIPT)],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)

    def test_auto_container_resolves_active_color(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            active = tmp / "active-color"
            active.write_text("green\n")
            fakebin = tmp / "bin"
            fakebin.mkdir()
            calls = tmp / "docker-calls.log"
            (fakebin / "docker").write_text(
                textwrap.dedent(
                    f"""\
                    #!/usr/bin/env bash
                    echo "$*" >> {calls}
                    if [ "$1" = inspect ]; then
                      [ "$2" = tokenkey-green ] && exit 0
                      exit 1
                    fi
                    if [ "$1" = logs ]; then
                      cat <<'LOGS'
                    2026-06-24T05:00:00Z INFO http request completed {{"request_id":"r1","path":"/v1/messages","status_code":200}}
                    2026-06-24T05:00:01Z INFO anthropic_downstream_kiro_oauth_403_skip_penalty
                    LOGS
                      exit 0
                    fi
                    exit 2
                    """
                ),
            )
            (fakebin / "docker").chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{fakebin}:{os.environ.get('PATH', '')}",
                "ACTIVE_COLOR_FILE": str(active),
                "HOOK_PATTERNS": "anthropic_downstream_kiro_oauth_403_skip_penalty",
            }
            proc = subprocess.run(
                ["bash", str(_SCRIPT)],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
            self.assertIn('"container": "tokenkey-green"', proc.stdout)
            self.assertIn('"pattern": "anthropic_downstream_kiro_oauth_403_skip_penalty", "count": 1', proc.stdout)

            meta = None
            for line in proc.stdout.splitlines():
                if line.startswith("{") and '"container_resolution"' in line:
                    meta = json.loads(line)
                    break
            self.assertIsNotNone(meta)
            assert meta is not None
            self.assertIn("active-color=green", meta["container_resolution"])
            self.assertIn("active-color container exists", meta["container_resolution"])

    def test_auto_container_falls_back_to_legacy(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            fakebin = tmp / "bin"
            fakebin.mkdir()
            (fakebin / "docker").write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    if [ "$1" = inspect ]; then
                      [ "$2" = tokenkey ] && exit 0
                      exit 1
                    fi
                    if [ "$1" = logs ]; then
                      echo 'INFO http request completed {"request_id":"r2","path":"/health/live","status_code":200}'
                      exit 0
                    fi
                    exit 2
                    """
                ),
            )
            (fakebin / "docker").chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{fakebin}:{os.environ.get('PATH', '')}",
                "ACTIVE_COLOR_FILE": str(tmp / "missing-active-color"),
            }
            proc = subprocess.run(
                ["bash", str(_SCRIPT)],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
            self.assertIn('"container": "tokenkey"', proc.stdout)
            self.assertIn('"fallback=tokenkey"', proc.stdout)


if __name__ == "__main__":
    unittest.main()
