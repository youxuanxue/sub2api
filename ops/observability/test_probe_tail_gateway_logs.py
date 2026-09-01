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
                    # The canonical resolver asks for State.Running, not mere existence.
                    if [ "$1" = inspect ]; then
                      name="${@: -1}"
                      [ "$name" = tokenkey-green ] || exit 1
                      echo true
                      exit 0
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
            self.assertIn("tokenkey-green is running", payload["meta"]["container_resolution"])
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
                    # Only the legacy container runs -> unique running candidate.
                    if [ "$1" = inspect ]; then
                      name="${@: -1}"
                      [ "$name" = tokenkey ] || exit 1
                      echo true
                      exit 0
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
            self.assertIn("unique running candidate tokenkey", payload["meta"]["container_resolution"])

    def test_request_ids_filter_keeps_matching_completed_rows(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            fakebin = tmp / "bin"
            fakebin.mkdir()
            (fakebin / "docker").write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    if [ "$1" = inspect ]; then
                      name="${@: -1}"
                      [ "$name" = tokenkey ] || exit 1
                      echo true
                      exit 0
                    fi
                    if [ "$1" = logs ]; then
                      cat <<'LOGS'
                    2026-08-31T14:21:00Z INFO http request completed {"request_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","path":"/v1/chat/completions","status_code":502,"latency_ms":12}
                    2026-08-31T14:21:01Z INFO http request completed {"request_id":"ffffffff-0000-0000-0000-000000000000","client_request_id":"other","path":"/v1/chat/completions","status_code":200}
                    2026-08-31T14:21:02Z INFO http request completed {"request_id":"11111111-2222-3333-4444-555555555555","client_request_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","path":"/v1/messages","status_code":200,"latency_ms":800}
                    LOGS
                      exit 0
                    fi
                    if [ "$1" = exec ]; then
                      exit 1
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
                "REQUEST_IDS": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
                "LIMIT": "20",
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
            ids = {
                (row.get("request_id"), row.get("client_request_id"))
                for row in payload["requests"]
            }
            self.assertEqual(
                ids,
                {
                    ("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", None),
                    (
                        "11111111-2222-3333-4444-555555555555",
                        "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
                    ),
                },
            )
            self.assertEqual(payload["meta"]["matched_total"], 2)
            self.assertIn("db", payload)
            self.assertIn("error", payload["db"]["usage_logs"])

    def test_connect_hosts_rejects_non_edge_hostname(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            fakebin = tmp / "bin"
            fakebin.mkdir()
            (fakebin / "docker").write_text("#!/usr/bin/env bash\nexit 2\n")
            (fakebin / "docker").chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{fakebin}:{os.environ.get('PATH', '')}",
                "ACTIVE_COLOR_FILE": str(tmp / "missing-active-color"),
                "CONNECT_HOSTS": "example.com",
                "LIMIT": "1",
            }
            proc = subprocess.run(
                ["bash", str(_SCRIPT)],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 2, msg=proc.stderr + proc.stdout)
            payload = json.loads(proc.stdout)
            self.assertIn("example.com", payload["rejected"])

    def test_connect_hosts_probes_allowlisted_edge(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            fakebin = tmp / "bin"
            fakebin.mkdir()
            (fakebin / "docker").write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    if [ "$1" = inspect ]; then
                      name="${@: -1}"
                      [ "$name" = tokenkey ] || exit 1
                      echo true
                      exit 0
                    fi
                    if [ "$1" = logs ]; then
                      echo 'INFO http request completed {"request_id":"r2","path":"/health/live","status_code":200}'
                      exit 0
                    fi
                    if [ "$1" = exec ]; then
                      if [ "$3" = getent ]; then
                        echo '1.2.3.4 STREAM api-us5.tokenkey.dev'
                        exit 0
                      fi
                      if [ "$3" = wget ]; then
                        echo 'HTTP/1.1 401 Unauthorized' >&2
                        exit 0
                      fi
                      if [ "$3" = psql ]; then
                        echo '{"id":71,"name":"kiro-us5","base_url":"https://api-us5.tokenkey.dev","usage_6h":0,"err502_6h":12}'
                        exit 0
                      fi
                      exit 1
                    fi
                    exit 2
                    """
                ),
            )
            (fakebin / "docker").chmod(0o755)
            (fakebin / "getent").write_text(
                "#!/usr/bin/env bash\necho '1.2.3.4 STREAM api-us5.tokenkey.dev'\n"
            )
            (fakebin / "getent").chmod(0o755)
            (fakebin / "curl").write_text(
                "#!/usr/bin/env bash\necho '401 0.080'\n"
            )
            (fakebin / "curl").chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{fakebin}:{os.environ.get('PATH', '')}",
                "ACTIVE_COLOR_FILE": str(tmp / "missing-active-color"),
                "CONNECT_HOSTS": "api-us5.tokenkey.dev",
                "LIMIT": "5",
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
            hop = payload["connect"]["hosts"][0]
            self.assertEqual(hop["host"], "api-us5.tokenkey.dev")
            self.assertEqual(hop["dns_host"]["ips"], ["1.2.3.4"])
            self.assertEqual(hop["gets"]["/health"]["host"]["http_code"], 401)
            self.assertEqual(payload["db"]["sibling_accounts"][0]["name"], "kiro-us5")


if __name__ == "__main__":
    unittest.main()
