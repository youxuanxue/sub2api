#!/usr/bin/env python3
from __future__ import annotations

import json
import os
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


SCRIPT = Path(__file__).with_name("probe_account_upstream_models.sh")


class _Server:
    def __init__(self, handler):
        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self.httpd.server_port}/api/v1"

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *_args):
        self.httpd.shutdown()
        self.thread.join(timeout=2)
        self.httpd.server_close()


class ProbeAccountUpstreamModelsTest(unittest.TestCase):
    def run_probe(self, *, base_url: str, model: str = "", targets: str = "") -> dict:
        with tempfile.TemporaryDirectory() as tmp:
            fake_sudo = Path(tmp) / "sudo"
            fake_sudo.write_text(
                "#!/bin/sh\n"
                "printf '%s\\n' '{\"admin_key\":\"test-admin-key\",\"account\":{\"name\":\"kiro-us3\",\"platform\":\"anthropic\",\"type\":\"apikey\",\"channel_type\":0,\"mirror_platform\":\"kiro\",\"base_url\":\"https://api-us3.tokenkey.dev\"}}'\n",
                encoding="utf-8",
            )
            fake_sudo.chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{tmp}:{os.environ.get('PATH', '')}",
                "ACCOUNT_ID": "19",
                "BASE_URL": base_url,
                "MODEL": model,
                "TARGET_MODELS": targets,
                "REQUEST_TIMEOUT_SECONDS": "5",
            }
            proc = subprocess.run(
                ["bash", str(SCRIPT)],
                check=True,
                capture_output=True,
                text=True,
                env=env,
            )
        return json.loads(proc.stdout)

    def test_rejects_urls_that_could_receive_the_admin_key(self):
        unsafe = [
            "https://evil.example/api/v1",
            "http://api-us4.tokenkey.dev/api/v1",
            "https://api-us4.tokenkey.dev.evil.example/api/v1",
            "https://user@api-us4.tokenkey.dev/api/v1",
            "https://api-us4.tokenkey.dev:8443/api/v1",
            "https://api-us4.tokenkey.dev/api/v1?next=evil",
            "https://api-us4.tokenkey.dev/not-api/v1",
        ]
        for base_url in unsafe:
            with self.subTest(base_url=base_url):
                got = self.run_probe(base_url=base_url)
                self.assertEqual(got["verdict"], "setup_error")
                self.assertIn("BASE_URL", got["error"])

    def test_lists_models_from_an_allowed_loopback_target(self):
        seen = {}

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                seen["path"] = self.path
                seen["key"] = self.headers.get("x-api-key")
                payload = {"data": {"models": ["control", "claude-opus-5", "control"]}}
                body = json.dumps(payload).encode()
                self.send_response(200)
                self.send_header("content-type", "application/json")
                self.send_header("content-length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *_args):
                pass

        with _Server(Handler) as server:
            got = self.run_probe(base_url=server.base_url, targets="claude-opus-5")
        self.assertEqual(got["verdict"], "listed")
        self.assertEqual(got["models"], ["claude-opus-5", "control"])
        self.assertEqual(got["account_platform"], "anthropic")
        self.assertEqual(got["account_scope"], "kiro")
        self.assertEqual(seen["key"], "test-admin-key")
        self.assertEqual(seen["path"], "/api/v1/admin/accounts/19/models/sync-upstream")

    def test_direct_account_test_parses_successful_sse(self):
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = (
                    'data: {"type":"test_start","model":"claude-opus-5"}\n\n'
                    'data: {"type":"content","text":"OK"}\n\n'
                    'data: {"type":"test_complete","success":true}\n\n'
                ).encode()
                self.send_response(200)
                self.send_header("content-type", "text/event-stream")
                self.send_header("content-length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *_args):
                pass

        with _Server(Handler) as server:
            got = self.run_probe(base_url=server.base_url, model="claude-opus-5")
        self.assertEqual(got["verdict"], "servable")
        self.assertEqual(got["probe"], "account_test")
        self.assertEqual(got["account_platform"], "anthropic")
        self.assertEqual(got["account_scope"], "kiro")
        self.assertEqual(got["content_excerpt"], "OK")

    def test_does_not_forward_admin_key_across_redirects(self):
        redirected = {"requests": 0}

        class SinkHandler(BaseHTTPRequestHandler):
            def do_POST(self):
                redirected["requests"] += 1
                self.send_response(200)
                self.end_headers()

            def log_message(self, *_args):
                pass

        with _Server(SinkHandler) as sink:
            class RedirectHandler(BaseHTTPRequestHandler):
                def do_POST(self):
                    self.send_response(302)
                    self.send_header("location", sink.base_url + "/stolen")
                    self.end_headers()

                def log_message(self, *_args):
                    pass

            with _Server(RedirectHandler) as source:
                got = self.run_probe(base_url=source.base_url)
        self.assertEqual(got["verdict"], "upstream_error")
        self.assertEqual(got["http_status"], 302)
        self.assertEqual(redirected["requests"], 0)


if __name__ == "__main__":
    unittest.main()
