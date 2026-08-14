#!/usr/bin/env python3
from __future__ import annotations

import json
import os
import shlex
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


SCRIPT = Path(__file__).with_name("probe_grok_upstream_model.sh")


class _Server:
    def __init__(self, handler):
        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self.httpd.server_port}/v1"

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *_args):
        self.httpd.shutdown()
        self.thread.join(timeout=2)
        self.httpd.server_close()


class ProbeGrokUpstreamModelTest(unittest.TestCase):
    def run_probe(self, *, base_url: str, endpoint: str | None = None) -> dict:
        row = "test-token\thttps://api.x.ai/v1\tgrok-test\tgrok"
        with tempfile.TemporaryDirectory() as tmp:
            fake_sudo = Path(tmp) / "sudo"
            fake_sudo.write_text(
                "#!/bin/sh\n" f"printf '%s\\n' {shlex.quote(row)}\n",
                encoding="utf-8",
            )
            fake_sudo.chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{tmp}:{os.environ.get('PATH', '')}",
                "ACCOUNT_ID": "65",
                "MODEL": "grok-4.20-multi-agent-0309",
                "UPSTREAM_BASE": base_url,
            }
            if endpoint is not None:
                env["ENDPOINT"] = endpoint
            proc = subprocess.run(
                ["bash", str(SCRIPT)],
                check=True,
                capture_output=True,
                text=True,
                env=env,
            )
        return json.loads(proc.stdout)

    def test_responses_uses_responses_request_shape(self):
        seen = {}

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                seen["path"] = self.path
                seen["authorization"] = self.headers.get("authorization")
                length = int(self.headers.get("content-length", "0"))
                seen["body"] = json.loads(self.rfile.read(length))
                body = json.dumps({"id": "resp-test", "output": []}).encode()
                self.send_response(200)
                self.send_header("content-type", "application/json")
                self.send_header("content-length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *_args):
                pass

        with _Server(Handler) as server:
            result = self.run_probe(base_url=server.base_url, endpoint="responses")
        self.assertEqual(result["verdict"], "servable")
        self.assertEqual(result["probe"]["endpoint"], "responses")
        self.assertEqual(seen["path"], "/v1/responses")
        self.assertEqual(seen["authorization"], "Bearer test-token")
        self.assertNotIn("messages", seen["body"])
        self.assertEqual(seen["body"]["input"][0]["content"][0]["type"], "input_text")
        self.assertEqual(seen["body"]["max_output_tokens"], 16)

    def test_default_remains_chat_completions(self):
        seen = {}

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                seen["path"] = self.path
                length = int(self.headers.get("content-length", "0"))
                seen["body"] = json.loads(self.rfile.read(length))
                body = json.dumps({"choices": []}).encode()
                self.send_response(200)
                self.send_header("content-length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *_args):
                pass

        with _Server(Handler) as server:
            result = self.run_probe(base_url=server.base_url)
        self.assertEqual(result["probe"]["endpoint"], "chat/completions")
        self.assertEqual(seen["path"], "/v1/chat/completions")
        self.assertIn("messages", seen["body"])
        self.assertNotIn("input", seen["body"])

    def test_rejects_unknown_endpoint(self):
        result = self.run_probe(base_url="http://127.0.0.1:1/v1", endpoint="video")
        self.assertEqual(result["verdict"], "setup_error")
        self.assertIn("ENDPOINT", result["error"])


if __name__ == "__main__":
    unittest.main()
