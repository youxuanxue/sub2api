#!/usr/bin/env python3
"""Validation tests for probe-release-control-plane.sh.

Uses fake curl + a temporary matrix. No network or AWS access.
"""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "probe-release-control-plane.sh"
_REPO = _SCRIPT.parents[2]


class ProbeReleaseControlPlaneTest(unittest.TestCase):
    def test_syntax_clean(self) -> None:
        proc = subprocess.run(
            ["bash", "-n", str(_SCRIPT)],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)

    def test_help_does_not_probe(self) -> None:
        proc = subprocess.run(
            ["bash", str(_SCRIPT), "--help"],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0)
        self.assertIn("probe-release-control-plane.sh", proc.stdout)
        self.assertNotIn('"summary": "control_plane"', proc.stdout)

    def test_prod_and_edge_health_success(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            matrix = tmp / "aws"
            (matrix / "stage0").mkdir(parents=True)
            (matrix / "lightsail").mkdir(parents=True)
            (matrix / "stage0/edge-targets.json").write_text('{"targets":{}}')
            (matrix / "lightsail/edge-targets-lightsail.json").write_text(
                json.dumps({
                    "targets": {
                        "us9": {
                            "deployable": True,
                            "domain": "api-us9.example.test",
                            "lightsail_region": "us-east-1",
                            "ssm_prefix": "/edge/us9",
                        }
                    }
                }),
            )
            fakebin = tmp / "bin"
            fakebin.mkdir()
            (fakebin / "curl").write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    out=""
                    while [ "$#" -gt 0 ]; do
                      case "$1" in
                        -o) out="$2"; shift 2 ;;
                        -w) shift 2 ;;
                        --max-time) shift 2 ;;
                        -sS) shift ;;
                        *) url="$1"; shift ;;
                      esac
                    done
                    [ -n "$out" ] && printf '{"status":"ok"}' > "$out"
                    printf '200 0.123'
                    """
                ),
            )
            (fakebin / "curl").chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{fakebin}:{os.environ.get('PATH', '')}",
                "MATRIX_DIR": str(matrix),
                "EDGE_IDS": "us9",
                "PROD_BASE_URL": "https://prod.example.test",
            }
            proc = subprocess.run(
                ["bash", str(_SCRIPT)],
                cwd=_REPO,
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
            rows = [json.loads(line) for line in proc.stdout.splitlines() if line.startswith("{")]
            self.assertEqual(rows[-1]["status"], "ok")
            urls = {row.get("url") for row in rows if "url" in row}
            self.assertIn("https://prod.example.test/health", urls)
            self.assertIn("https://prod.example.test/api/v1/settings/public", urls)
            self.assertIn("https://api-us9.example.test/health", urls)

    def test_failure_exits_nonzero_and_summarizes(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            fakebin = tmp / "bin"
            fakebin.mkdir()
            (fakebin / "curl").write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    out=""
                    while [ "$#" -gt 0 ]; do
                      case "$1" in
                        -o) out="$2"; shift 2 ;;
                        -w) shift 2 ;;
                        --max-time) shift 2 ;;
                        -sS) shift ;;
                        *) shift ;;
                      esac
                    done
                    [ -n "$out" ] && : > "$out"
                    printf '502 0.050'
                    """
                ),
            )
            (fakebin / "curl").chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{fakebin}:{os.environ.get('PATH', '')}",
                "EDGE_IDS": "none",
                "INCLUDE_SETTINGS_PUBLIC": "0",
            }
            proc = subprocess.run(
                ["bash", str(_SCRIPT)],
                cwd=_REPO,
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 4, msg=proc.stderr + proc.stdout)
            summary = [json.loads(line) for line in proc.stdout.splitlines() if line.startswith("{")][-1]
            self.assertEqual(summary["status"], "fail")
            self.assertEqual(summary["ok"], 0)
            self.assertEqual(summary["total"], 1)

    def test_curl_transport_error_still_outputs_structured_failure(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            fakebin = tmp / "bin"
            fakebin.mkdir()
            (fakebin / "curl").write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    out=""
                    while [ "$#" -gt 0 ]; do
                      case "$1" in
                        -o) out="$2"; shift 2 ;;
                        -w) shift 2 ;;
                        --max-time) shift 2 ;;
                        -sS) shift ;;
                        *) shift ;;
                      esac
                    done
                    [ -n "$out" ] && : > "$out"
                    echo 'curl: (7) Failed to connect to prod.example.test port 443' >&2
                    printf '000 0.000'
                    exit 7
                    """
                ),
            )
            (fakebin / "curl").chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{fakebin}:{os.environ.get('PATH', '')}",
                "EDGE_IDS": "none",
                "INCLUDE_SETTINGS_PUBLIC": "0",
                "PROD_BASE_URL": "https://prod.example.test",
            }
            proc = subprocess.run(
                ["bash", str(_SCRIPT)],
                cwd=_REPO,
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 4, msg=proc.stderr + proc.stdout)
            rows = [json.loads(line) for line in proc.stdout.splitlines() if line.startswith("{")]
            self.assertEqual(rows[0]["status"], "fail")
            self.assertEqual(rows[0]["curl_rc"], 7)
            self.assertEqual(rows[0]["http_code"], 0)
            self.assertIn("Failed to connect", rows[0]["curl_error"])
            self.assertEqual(rows[-1]["status"], "fail")


if __name__ == "__main__":
    unittest.main()
