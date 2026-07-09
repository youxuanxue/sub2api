#!/usr/bin/env python3
"""Validation tests for probe-tail-gateway-logs.sh."""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "probe-tail-gateway-logs.sh"
_SYNTAX_SCRIPTS = (
    _SCRIPT,
    pathlib.Path(__file__).resolve().parent / "probe-traffic-logs.sh",
    pathlib.Path(__file__).resolve().parent / "probe-edge-health.sh",
    pathlib.Path(__file__).resolve().parent / "probe-gateway-ua-tls-compare.sh",
)


class ProbeTailGatewayLogsTest(unittest.TestCase):
    def test_syntax_clean(self) -> None:
        for script in _SYNTAX_SCRIPTS:
            with self.subTest(script=script.name):
                proc = subprocess.run(
                    ["bash", "-n", str(script)],
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
            (fakebin / "docker").write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    if [ "$1" = inspect ]; then
                      [ "$2" = tokenkey-green ] && exit 0
                      exit 1
                    fi
                    if [ "$1" = logs ]; then
                      cat <<'LOGS'
                    2026-06-24T05:00:00Z INFO http request completed {"request_id":"r1","path":"/v1/messages","status_code":200,"latency_ms":123}
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
            }
            proc = subprocess.run(
                ["bash", str(_SCRIPT)],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
            payload = json.loads(proc.stdout)
            self.assertEqual(payload["meta"]["container"], "tokenkey-green")
            self.assertIn("active-color=green", payload["meta"]["container_resolution"])
            self.assertIn("active-color container exists", payload["meta"]["container_resolution"])
            self.assertEqual(payload["requests"][0]["request_id"], "r1")

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
            payload = json.loads(proc.stdout)
            self.assertEqual(payload["meta"]["container"], "tokenkey")
            self.assertIn("fallback=tokenkey", payload["meta"]["container_resolution"])


if __name__ == "__main__":
    unittest.main()
