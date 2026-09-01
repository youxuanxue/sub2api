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
    def run_probe(
        self,
        *,
        base_url: str,
        model: str = "",
        targets: str = "",
        account_id: str = "19",
        account: dict | None = None,
    ) -> dict:
        if account is None:
            account = {
                "name": "kiro-us3",
                "platform": "anthropic",
                "type": "apikey",
                "channel_type": 0,
                "mirror_platform": "kiro",
                "base_url": "https://api-us3.tokenkey.dev",
            }
        bootstrap = json.dumps({"admin_key": "test-admin-key", "account": account})
        with tempfile.TemporaryDirectory() as tmp:
            fake_sudo = Path(tmp) / "sudo"
            fake_sudo.write_text(
                "#!/bin/sh\n"
                f"printf '%s\\n' {shlex.quote(bootstrap)}\n",
                encoding="utf-8",
            )
            fake_sudo.chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{tmp}:{os.environ.get('PATH', '')}",
                "ACCOUNT_ID": account_id,
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
            "http://example.test/api/v1",
            "http://0.0.0.0/api/v1",
            "http://169.254.169.254/api/v1",
        ]
        for base_url in unsafe:
            with self.subTest(base_url=base_url):
                got = self.run_probe(base_url=base_url)
                self.assertEqual(got["verdict"], "setup_error")
                self.assertIn("BASE_URL", got["error"])

    def test_allows_private_ipv4_literal_for_container_network(self):
        script = SCRIPT.read_text(encoding="utf-8")
        self.assertIn('ipaddress.ip_network("172.16.0.0/12")', script)
        self.assertIn('parsed.scheme == "http" and (is_loopback or is_private_literal)', script)

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

    def test_agent_plan_scope_uses_properties_not_account_id(self):
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                payload = {"data": {"models": ["ark-code-latest"]}}
                body = json.dumps(payload).encode()
                self.send_response(200)
                self.send_header("content-type", "application/json")
                self.send_header("content-length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *_args):
                pass

        account = {
            "name": "volcengine-agent-plan-secondary",
            "platform": "newapi",
            "type": "apikey",
            "channel_type": 45,
            "mirror_platform": "",
            "base_url": "https://ark.cn-beijing.volces.com/api/plan/v3/",
        }
        with _Server(Handler) as server:
            got = self.run_probe(
                base_url=server.base_url,
                targets="ark-code-latest",
                account_id="89",
                account=account,
            )
        self.assertEqual(got["account_id"], 89)
        self.assertEqual(got["account_platform"], "newapi")
        self.assertEqual(
            got["account_scope"],
            "account_override:newapi:45:https://ark.cn-beijing.volces.com/api/plan/v3",
        )
        self.assertEqual(
            got["account_base_url"],
            "https://ark.cn-beijing.volces.com/api/plan/v3",
        )

    def test_tokensea_v1_suffix_keeps_relay_scope(self):
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                payload = {"data": {"models": ["gpt-5.4"]}}
                body = json.dumps(payload).encode()
                self.send_response(200)
                self.send_header("content-type", "application/json")
                self.send_header("content-length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *_args):
                pass

        # /v1 suffix is a stored-URL boundary sample, not an owner copy of live #92/#93.
        cases = [
            (
                {
                    "name": "tokensea",
                    "platform": "openai",
                    "type": "apikey",
                    "channel_type": 0,
                    "mirror_platform": "",
                    "base_url": "https://agent.tokensea.ai/v1",
                },
                "openai_tokensea_relay",
            ),
            (
                {
                    "name": "tokensea-cc",
                    "platform": "anthropic",
                    "type": "apikey",
                    "channel_type": 0,
                    "mirror_platform": "",
                    "base_url": "https://agent.tokensea.ai/v1/",
                },
                "anthropic_tokensea_relay",
            ),
        ]
        for account, scope in cases:
            with self.subTest(scope=scope):
                with _Server(Handler) as server:
                    got = self.run_probe(
                        base_url=server.base_url,
                        targets="gpt-5.4",
                        account_id="92",
                        account=account,
                    )
                self.assertEqual(got["account_scope"], scope)

    def test_volcengine_payg_scope_stays_channel_floor(self):
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                payload = {"data": {"models": ["doubao-seed-1-6"]}}
                body = json.dumps(payload).encode()
                self.send_response(200)
                self.send_header("content-type", "application/json")
                self.send_header("content-length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *_args):
                pass

        account = {
            "name": "volcengine-payg-secondary",
            "platform": "newapi",
            "type": "apikey",
            "channel_type": 45,
            "mirror_platform": "",
            "base_url": "https://ark.cn-beijing.volces.com/api/v3/",
        }
        with _Server(Handler) as server:
            got = self.run_probe(
                base_url=server.base_url,
                targets="doubao-seed-1-6",
                account_id="89",
                account=account,
            )
        self.assertEqual(got["account_scope"], "newapi_channel_type:45")

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
