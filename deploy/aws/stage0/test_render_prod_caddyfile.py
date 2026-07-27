#!/usr/bin/env python3
"""Behavior tests for deploy/aws/stage0/render-prod-caddyfile.sh."""

from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import unittest

_HERE = pathlib.Path(__file__).resolve().parent
_TEMPLATE = _HERE / "Caddyfile"
_RENDER = _HERE / "render-prod-caddyfile.sh"


def _render(*, api_domain: str, site_domain: str = "", acme_email: str = "ops@example.com") -> str:
    env = {
        **os.environ,
        "API_DOMAIN": api_domain,
        "ACME_EMAIL": acme_email,
        "SITE_DOMAIN": site_domain,
    }
    with tempfile.NamedTemporaryFile("w+", suffix=".caddy", delete=False) as tmp:
        out_path = pathlib.Path(tmp.name)
    try:
        proc = subprocess.run(
            ["bash", str(_RENDER), str(_TEMPLATE), str(out_path)],
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        if proc.returncode != 0:
            raise AssertionError(proc.stderr or proc.stdout)
        return out_path.read_text()
    finally:
        out_path.unlink(missing_ok=True)


class RenderProdCaddyfileTest(unittest.TestCase):
    def test_api_domain_derives_apex_redirect(self) -> None:
        rendered = _render(api_domain="api.tokenkey.dev")
        self.assertIn("tokenkey.dev {", rendered)
        self.assertIn("redir https://api.tokenkey.dev{uri} permanent", rendered)
        self.assertNotIn("BEGIN_APEX_VHOST", rendered)

    def test_localhost_skips_apex_block(self) -> None:
        rendered = _render(api_domain="localhost")
        self.assertIn("localhost {", rendered)
        self.assertNotIn("redir https://", rendered)
        self.assertNotIn("BEGIN_APEX_VHOST", rendered)

    def test_explicit_site_domain_override(self) -> None:
        rendered = _render(api_domain="api.custom.example", site_domain="custom.example")
        self.assertIn("custom.example {", rendered)
        self.assertIn("redir https://api.custom.example{uri} permanent", rendered)


if __name__ == "__main__":
    unittest.main()
