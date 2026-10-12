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
            matrix = tmp / "deploy"
            (matrix / "hetzner").mkdir(parents=True)
            (matrix / "hetzner/edge-targets-hetzner.json").write_text(
                json.dumps({
                    "targets": {
                        "us9": {
                            "deployable": True,
                            "domain": "api-us9.example.test",
                            "location": "fsn1",
                            "server_type": "cax21",
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

    def test_prod_settings_public_uses_apex_when_gateway_is_api_subdomain(self) -> None:
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
                "EDGE_IDS": "none",
                "PROD_BASE_URL": "https://api.prod.example.test",
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
            urls = {row.get("url") for row in rows if "url" in row}
            self.assertIn("https://api.prod.example.test/health", urls)
            self.assertIn("https://prod.example.test/api/v1/settings/public", urls)
            self.assertNotIn("https://api.prod.example.test/api/v1/settings/public", urls)

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


class LiveFleetProbeTest(unittest.TestCase):
    def probe(self, *, selector="", matrix=None):
        with tempfile.TemporaryDirectory() as td:
            root = pathlib.Path(td)
            fakebin = root / "bin"
            fakebin.mkdir()
            calls = root / "urls"
            curl = fakebin / "curl"
            curl.write_text("#!/bin/sh\n"
                            "while [ \"$#\" -gt 0 ]; do\n"
                            " case \"$1\" in -o) out=$2; shift 2;; -w|--max-time) shift 2;; -sS) shift;; *) url=$1; shift;; esac\n"
                            "done\nprintf '%s\\n' \"$url\" >> \"$CALLS\"\n"
                            "printf '{}' > \"$out\"\nprintf '200 0.1'\n")
            curl.chmod(0o755)
            env = {**os.environ, "PATH": f"{fakebin}:{os.environ['PATH']}",
                   "CALLS": str(calls), "EDGE_IDS": selector, "MATRIX_DIR": str(_REPO / "deploy"),
                   "INCLUDE_SETTINGS_PUBLIC": "1", "PROD_BASE_URL": "https://api.prod.example.test"}
            if matrix is not None:
                deploy = root / "deploy"
                (deploy / "hetzner").mkdir(parents=True)
                (deploy / "aws/lightsail").mkdir(parents=True)
                (deploy / "hetzner/edge-targets-hetzner.json").write_text(json.dumps(matrix))
                # Standby can even be re-enabled accidentally: it must not enter this probe.
                (deploy / "aws/lightsail/edge-targets-lightsail.json").write_text(json.dumps({
                    "targets": {"old": {"deployable": True, "domain": "old.example.test"}}}))
                env["MATRIX_DIR"] = str(deploy)
            result = subprocess.run(["bash", str(_SCRIPT)], env=env, capture_output=True, text=True)
            rows = [json.loads(line) for line in result.stdout.splitlines()]
            urls = calls.read_text().splitlines() if calls.exists() else []
            return result, rows, urls

    def test_default_entry_checks_current_fleet_domains(self):
        import sys
        sys.path.insert(0, str(_REPO / "ops/stage0"))
        from edge_routing_matrix import live_deployable_edge_ids, load_hetzner_targets
        ids = live_deployable_edge_ids(_REPO)
        targets = load_hetzner_targets(_REPO)
        result, rows, urls = self.probe()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(urls, ["https://api.prod.example.test/health",
                               "https://prod.example.test/api/v1/settings/public"] +
                         [f"https://{targets[e]['domain']}/health" for e in ids])
        self.assertEqual(rows[-1]["total"], 2 + len(ids))
        self.assertEqual(rows[-1]["failures"], [])

    def test_materialized_defaults_and_explicit_domain_exclude_standby(self):
        matrix = {"target_defaults": {"location": "fsn1", "server_type": "cax21"},
                  "targets": {"new": {"deployable": True},
                              "custom": {"deployable": True, "domain": "custom.example.test"},
                              "planned": {"deployable": False}}}
        result, rows, urls = self.probe(matrix=matrix)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(urls[2:], ["https://custom.example.test/health", "https://api-new.tokenkey.dev/health"])
        self.assertEqual(rows[-1]["ok"], 4)

    def test_invalid_selected_target_does_not_prevent_other_probes(self):
        matrix = {"target_defaults": {"location": "fsn1", "server_type": "cax21"},
                  "targets": {"valid": {"deployable": True}}}
        result, rows, urls = self.probe(selector="missing,valid", matrix=matrix)
        self.assertEqual(result.returncode, 4)
        self.assertEqual(urls[-1], "https://api-valid.tokenkey.dev/health")
        self.assertEqual(rows[-1]["failures"], ["edge:missing:target_resolution"])
        self.assertEqual(rows[-1]["ok"], 3)
        self.assertEqual(rows[-1]["total"], 4)
        self.assertEqual(rows[2]["status"], "fail")

    def test_invalid_matrix_reports_failure_and_still_checks_prod(self):
        result, rows, urls = self.probe(matrix={"targets": []})
        self.assertEqual(result.returncode, 4)
        self.assertEqual(len(urls), 2)
        self.assertEqual(rows[-1]["failures"], ["fleet:target_resolution"])
        self.assertEqual(rows[-1]["ok"], 2)


if __name__ == "__main__":
    unittest.main()
