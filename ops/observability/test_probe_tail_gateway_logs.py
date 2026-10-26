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
                    2026-06-24T05:00:00Z INFO http request completed {"request_id":"r1","path":"/v1/messages","status_code":200,"latency_ms":17123,"image_ingress_ms":17016,"image_ingress_key_auth_ms":3,"image_ingress_selection_ms":13,"image_ingress_candidates_ms":11,"image_ingress_bind_ms":2,"image_request_body_read_ms":17000,"image_request_body_bytes":13,"image_request_content_length":-1,"prompt":"private","Authorization":"secret","ingress_body_read_ms":80,"ingress_body_bytes":524288,"ingress_before_routing_ms":82,"ingress_routing_ms":3,"request_body":"must-not-leak"}
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
            self.assertEqual(payload["requests"][0]["ingress_body_read_ms"], 80)
            self.assertEqual(payload["requests"][0]["ingress_body_bytes"], 524288)
            self.assertEqual(payload["requests"][0]["ingress_before_routing_ms"], 82)
            self.assertEqual(payload["requests"][0]["ingress_routing_ms"], 3)
            self.assertNotIn("request_body", payload["requests"][0])
            row = payload["requests"][0]
            self.assertEqual(
                {key: value for key, value in row.items() if key.startswith("image_")},
                {"image_ingress_ms": 17016, "image_ingress_key_auth_ms": 3,
                 "image_ingress_selection_ms": 13, "image_ingress_candidates_ms": 11, "image_ingress_bind_ms": 2,
                 "image_request_body_read_ms": 17000, "image_request_body_bytes": 13,
                 "image_request_content_length": -1},
            )
            self.assertNotIn("prompt", row)
            self.assertNotIn("Authorization", row)

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
                    2026-08-31T14:21:00Z INFO image.upstream_timing {"request_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","image_forward_index":2,"image_attempt_index":1,"image_request_written_ms":5,"image_tool_started_ms":1234,"image_generating_ms":2345,"image_result_ms":60001,"image_body_end_reason":"eof","image_upstream_body_bytes":8192,"image_connection_reused":false,"prompt":"private-image-prompt","Authorization":"secret-token"}
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
            timing = payload["related"][0]
            self.assertEqual(timing["image_forward_index"], 2)
            self.assertEqual(timing["image_attempt_index"], 1)
            self.assertEqual(timing["image_tool_started_ms"], 1234)
            self.assertEqual(timing["image_generating_ms"], 2345)
            self.assertEqual(timing["image_result_ms"], 60001)
            self.assertEqual(timing["image_body_end_reason"], "eof")
            self.assertIs(timing["image_connection_reused"], False)
            self.assertNotIn("private-image-prompt", proc.stdout)
            self.assertNotIn("secret-token", proc.stdout)
            self.assertEqual(payload["meta"]["matched_total"], 2)
            self.assertIn("db", payload)
            self.assertIn("error", payload["db"]["usage_logs"])

if __name__ == "__main__":
    unittest.main()
