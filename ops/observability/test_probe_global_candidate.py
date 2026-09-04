#!/usr/bin/env python3
"""Behavior tests for probe-global-candidate.py."""
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
    redirect_status = 302
    malformed_settings = False
    signup_bonus_balance = 1
    payment_enabled = False

    def do_GET(self) -> None:
        if self.path == "/":
            self._send(200, "text/html", b"""<html><head><link rel="canonical" href="https://global.tokenkey.dev/"></head><body><h1>China's leading AI models. One API.</h1></body></html>""")
        elif self.path == "/home":
            self._send(200, "text/html", b"home")
        elif self.path == "/setup/status":
            self._json({"code": 0, "data": {"needs_setup": False}})
        elif self.path == "/api/v1/settings/public":
            self._json({"code": 0, "data": None if self.malformed_settings else {
                "registration_enabled": True,
                "pricing_catalog_public": self.pricing_public,
                "signup_bonus_enabled": True,
                "signup_bonus_balance_usd": self.signup_bonus_balance,
                "payment_enabled": self.payment_enabled,
            }})
        elif self.path in {"/login?next=%2Fconsole", "/register"}:
            self.send_response(self.redirect_status)
            self.send_header("Location", f"http://127.0.0.1:{self.server.server_port}{self.path}")
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
        CandidateHandler.redirect_status = 302
        CandidateHandler.malformed_settings = False
        CandidateHandler.signup_bonus_balance = 1
        CandidateHandler.payment_enabled = False
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), CandidateHandler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)

    def _run(self, phase: str = "candidate") -> subprocess.CompletedProcess[str]:
        origin = f"http://127.0.0.1:{self.server.server_port}"
        return subprocess.run(
            ["python3", str(_SCRIPT), "--base-url", origin, "--product-url", origin, "--phase", phase],
            capture_output=True,
            text=True,
            check=False,
        )

    def test_candidate_contract_passes(self) -> None:
        proc = self._run()

        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        summary = json.loads(proc.stdout.splitlines()[-1])
        self.assertEqual(summary, {
            "failures": [],
            "ok": 6,
            "status": "ok",
            "summary": "global_candidate",
            "total": 6,
        })

    def test_false_pricing_projection_fails(self) -> None:
        CandidateHandler.pricing_public = False

        proc = self._run()

        self.assertEqual(proc.returncode, 4)
        rows = [json.loads(line) for line in proc.stdout.splitlines()]
        self.assertEqual(rows[-1]["failures"], ["public_settings"])
        self.assertIn('"pricing_catalog_public": false', rows[3]["detail"])

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

    def test_live_phase_requires_permanent_redirects(self) -> None:
        CandidateHandler.redirect_status = 301

        proc = self._run(phase="live")

        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)

    def test_non_object_settings_are_reported_as_failure(self) -> None:
        CandidateHandler.malformed_settings = True

        proc = self._run()

        self.assertEqual(proc.returncode, 4, msg=proc.stderr + proc.stdout)
        summary = json.loads(proc.stdout.splitlines()[-1])
        self.assertEqual(summary["failures"], ["public_settings"])


if __name__ == "__main__":
    unittest.main()
