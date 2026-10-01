#!/usr/bin/env python3
"""Behavior tests for probe-global-candidate.py (CallModel full facade)."""
from __future__ import annotations

import http.server
import json
import pathlib
import subprocess
import threading
import unittest


_SCRIPT = pathlib.Path(__file__).resolve().parent / "probe-global-candidate.py"


class CandidateHandler(http.server.BaseHTTPRequestHandler):
    pricing_public = True
    admin_redirect_status = 302
    malformed_settings = False
    home_alias_contract = True
    meta_noindex = False
    settings_code = 0
    signup_bonus_balance = 1
    payment_enabled = False
    api_base_url = "https://api.callmodel.io"
    product_paths_redirect = False
    alias_mode = False

    def do_GET(self) -> None:
        if self.path in {"/", "/home"}:
            if self.path == "/home" and not self.home_alias_contract:
                self._send(200, "text/html", b"<html><body>current homepage</body></html>")
                return
            robots = '<meta name="robots" content="noindex">' if self.meta_noindex else ""
            body = (
                '<html><head><link rel="canonical" href="https://callmodel.io/">'
                f"{robots}</head><body>"
                "<h1>China's leading AI models. One API.</h1>"
                '<a href="/register?redirect=%2Fquickstart%3Fmodel%3Ddeepseek-flash%26protocol%3Dopenai">'
                "Claim free trial</a>"
                "</body></html>"
            ).encode()
            self._send(200, "text/html", body)
        elif self.path == "/setup/status":
            self._json({"code": 0, "data": {"needs_setup": False}})
        elif self.path == "/api/v1/settings/public":
            self._json({"code": self.settings_code, "data": None if self.malformed_settings else {
                "registration_enabled": True,
                "pricing_catalog_public": self.pricing_public,
                "signup_bonus_enabled": True,
                "signup_bonus_balance_usd": self.signup_bonus_balance,
                "payment_enabled": self.payment_enabled,
                "api_base_url": self.api_base_url,
            }})
        elif self.path.startswith("/admin"):
            self.send_response(self.admin_redirect_status)
            self.send_header("Location", f"http://127.0.0.1:{self.server.server_port}{self.path}")
            self.end_headers()
        elif self.path in {"/login?next=%2Fconsole", "/register", "/models"}:
            if self.product_paths_redirect:
                self.send_response(302)
                self.send_header("Location", f"http://tokenkey.dev{self.path}")
                self.end_headers()
                return
            self._send(200, "text/html", b"<html><body>spa</body></html>")
        elif self.alias_mode and self.path == "/health":
            self._send(200, "application/json", b'{"status":"ok"}')
        elif self.alias_mode and self.path == "/login":
            self.send_response(301)
            self.send_header("Location", f"http://127.0.0.1:{self.server.server_port}/login")
            self.end_headers()
        else:
            self._send(404, "text/plain", b"missing")

    def _json(self, payload: dict) -> None:
        self._send(200, "application/json", json.dumps(payload).encode())

    def _send(self, status: int, content_type: str, body: bytes) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args) -> None:
        pass


class ProbeGlobalCandidateTest(unittest.TestCase):
    def setUp(self) -> None:
        CandidateHandler.pricing_public = True
        CandidateHandler.admin_redirect_status = 302
        CandidateHandler.malformed_settings = False
        CandidateHandler.home_alias_contract = True
        CandidateHandler.meta_noindex = False
        CandidateHandler.settings_code = 0
        CandidateHandler.signup_bonus_balance = 1
        CandidateHandler.payment_enabled = False
        CandidateHandler.api_base_url = "https://api.callmodel.io"
        CandidateHandler.product_paths_redirect = False
        CandidateHandler.alias_mode = False
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), CandidateHandler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)

    def _run(self, *extra: str) -> subprocess.CompletedProcess[str]:
        origin = f"http://127.0.0.1:{self.server.server_port}"
        return subprocess.run(
            [
                "python3",
                str(_SCRIPT),
                "--base-url",
                origin,
                "--product-url",
                origin,
                *extra,
            ],
            capture_output=True,
            text=True,
            check=False,
        )

    def test_candidate_facade_contract_passes(self) -> None:
        proc = self._run("--phase", "candidate")

        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        summary = json.loads(proc.stdout.splitlines()[-1])
        self.assertEqual(summary["status"], "ok")
        self.assertEqual(summary["failures"], [])
        self.assertEqual(summary["summary"], "callmodel_facade")
        self.assertEqual(summary["total"], 8)

    def test_product_path_kick_fails(self) -> None:
        CandidateHandler.product_paths_redirect = True

        proc = self._run("--phase", "candidate")

        self.assertEqual(proc.returncode, 4, msg=proc.stderr + proc.stdout)
        summary = json.loads(proc.stdout.splitlines()[-1])
        for check in ("register_stays", "login_stays", "models_stays"):
            self.assertIn(check, summary["failures"])

    def test_false_pricing_projection_fails(self) -> None:
        CandidateHandler.pricing_public = False

        proc = self._run()

        self.assertEqual(proc.returncode, 4)
        rows = [json.loads(line) for line in proc.stdout.splitlines()]
        self.assertEqual(rows[-1]["failures"], ["public_settings"])
        self.assertIn('"pricing_catalog_public": false', rows[3]["detail"])

    def test_home_alias_requires_the_export_crawler_contract(self) -> None:
        CandidateHandler.home_alias_contract = False

        proc = self._run()

        self.assertEqual(proc.returncode, 4, msg=proc.stderr + proc.stdout)
        rows = [json.loads(line) for line in proc.stdout.splitlines()]
        self.assertEqual(rows[-1]["failures"], ["home_alias"])

    def test_meta_noindex_fails_both_homepage_paths(self) -> None:
        CandidateHandler.meta_noindex = True

        proc = self._run()

        self.assertEqual(proc.returncode, 4, msg=proc.stderr + proc.stdout)
        rows = [json.loads(line) for line in proc.stdout.splitlines()]
        self.assertEqual(rows[-1]["failures"], ["homepage", "home_alias"])

    def test_nonzero_public_settings_api_code_fails(self) -> None:
        CandidateHandler.settings_code = 500

        proc = self._run()

        self.assertEqual(proc.returncode, 4, msg=proc.stderr + proc.stdout)
        rows = [json.loads(line) for line in proc.stdout.splitlines()]
        self.assertEqual(rows[-1]["failures"], ["public_settings"])
        self.assertIn("api_code=500", rows[3]["detail"])

    def test_positive_trial_and_enabled_payment_do_not_pin_temporary_config(self) -> None:
        CandidateHandler.signup_bonus_balance = 2.5
        CandidateHandler.payment_enabled = True

        proc = self._run()

        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)

    def test_non_positive_trial_balance_fails(self) -> None:
        CandidateHandler.signup_bonus_balance = 0

        proc = self._run()

        self.assertEqual(proc.returncode, 4, msg=proc.stderr + proc.stdout)
        rows = [json.loads(line) for line in proc.stdout.splitlines()]
        self.assertEqual(rows[-1]["failures"], ["public_settings"])
        self.assertIn('"signup_bonus_balance_usd": 0', rows[3]["detail"])

    def test_stale_api_base_url_fails_when_commercial_urls_required(self) -> None:
        CandidateHandler.api_base_url = "https://api.tokenkey.dev"

        proc = self._run()

        self.assertEqual(proc.returncode, 4, msg=proc.stderr + proc.stdout)
        summary = json.loads(proc.stdout.splitlines()[-1])
        self.assertEqual(summary["failures"], ["public_settings"])
        self.assertIn("api.tokenkey.dev", proc.stdout)

    def test_stale_api_base_url_allowed_when_commercial_urls_disabled(self) -> None:
        CandidateHandler.api_base_url = "https://api.tokenkey.dev"

        proc = self._run("--no-expect-commercial-urls")

        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)

    def test_live_phase_requires_permanent_admin_redirect(self) -> None:
        CandidateHandler.admin_redirect_status = 301

        proc = self._run("--phase", "live")

        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)

    def test_api_alias_human_redirect_and_health(self) -> None:
        CandidateHandler.alias_mode = True
        origin = f"http://127.0.0.1:{self.server.server_port}"

        proc = self._run("--api-alias-url", origin)

        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        summary = json.loads(proc.stdout.splitlines()[-1])
        self.assertIn("api_alias_health", [json.loads(line).get("check") for line in proc.stdout.splitlines()[:-1]])
        self.assertEqual(summary["status"], "ok")

    def test_non_object_settings_are_reported_as_failure(self) -> None:
        CandidateHandler.malformed_settings = True

        proc = self._run()

        self.assertEqual(proc.returncode, 4, msg=proc.stderr + proc.stdout)
        summary = json.loads(proc.stdout.splitlines()[-1])
        self.assertEqual(summary["failures"], ["public_settings"])


if __name__ == "__main__":
    unittest.main()
