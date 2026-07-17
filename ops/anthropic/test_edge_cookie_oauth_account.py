#!/usr/bin/env python3
"""Tests for edge-cookie-oauth-account.py pure helpers.

Run:
  python3 -m unittest ops/anthropic/test_edge_cookie_oauth_account.py
"""

from __future__ import annotations

import importlib.util
import pathlib
import unittest

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "edge-cookie-oauth-account.py"
_spec = importlib.util.spec_from_file_location("edge_cookie_oauth_account", _MOD_PATH)
mod = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(mod)


class EdgeCookieOAuthAccountTest(unittest.TestCase):
    def test_normalize_edge_id_accepts_common_forms(self) -> None:
        self.assertEqual(mod.normalize_edge_id("us6"), "us6")
        self.assertEqual(mod.normalize_edge_id("edge-us6"), "us6")
        self.assertEqual(mod.normalize_edge_id("edge:us6"), "us6")

    def test_parse_admin_credentials_key_value_file(self) -> None:
        creds = mod.parse_admin_credentials_text("email=admin@example.test\npassword=secret\n")
        self.assertEqual(creds, {"email": "admin@example.test", "password": "secret"})

    def test_parse_admin_credentials_plain_password_file(self) -> None:
        creds = mod.parse_admin_credentials_text("secret-only\n")
        self.assertEqual(creds, {"password": "secret-only"})

    def test_cookie_header_safe_rejects_header_breakers(self) -> None:
        self.assertTrue(mod.cookie_header_safe("sessionKey", "abc.123"))
        self.assertFalse(mod.cookie_header_safe("bad\nname", "abc"))
        self.assertFalse(mod.cookie_header_safe("name", "bad;value"))
        self.assertFalse(mod.cookie_header_safe("name", "bad\nvalue"))

    def test_remote_script_has_sanitized_failure_contract(self) -> None:
        self.assertIn('"ok": False', mod.REMOTE_SCRIPT)
        self.assertIn("claude_orgs_http_error", mod.REMOTE_SCRIPT)
        self.assertNotIn("body_preview", mod.REMOTE_SCRIPT)


if __name__ == "__main__":
    unittest.main()
