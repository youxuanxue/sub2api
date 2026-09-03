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


def _render(
    *,
    api_domain: str,
    site_domain: str = "",
    acme_email: str = "ops@example.com",
    global_site_domain: str = "",
    global_site_phase: str = "disabled",
) -> str:
    env = {
        **os.environ,
        "API_DOMAIN": api_domain,
        "ACME_EMAIL": acme_email,
        "SITE_DOMAIN": site_domain,
        "GLOBAL_SITE_DOMAIN": global_site_domain,
        "GLOBAL_SITE_PHASE": global_site_phase,
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
    def test_api_domain_derives_apex_serving_and_api_machine_split(self) -> None:
        rendered = _render(api_domain="api.tokenkey.dev")
        self.assertIn("tokenkey.dev {", rendered)
        self.assertIn("import tokenkey_reverse_proxy", rendered)
        self.assertNotIn("redir https://api.tokenkey.dev{uri} permanent", rendered)
        self.assertIn("@machine {", rendered)
        self.assertIn("path /v1/*", rendered)
        self.assertIn("path /openrouter/*", rendered)
        self.assertIn("path /backend-api/codex/*", rendered)
        self.assertIn("path /antigravity/*", rendered)
        self.assertIn("path /api/v1/payment/webhook/*", rendered)
        self.assertIn("path /api/v1/auth/oauth/*/*/callback", rendered)
        self.assertIn("path /api/event_logging/batch", rendered)
        self.assertIn("redir https://tokenkey.dev{uri} permanent", rendered)
        self.assertNotIn("BEGIN_APEX_VHOST", rendered)
        self.assertNotIn("BEGIN_API_FULL_PROXY", rendered)

    def test_localhost_skips_apex_and_uses_full_api_proxy(self) -> None:
        rendered = _render(api_domain="localhost")
        self.assertIn("localhost {", rendered)
        self.assertIn("import tokenkey_reverse_proxy", rendered)
        self.assertNotIn("tokenkey.dev {", rendered)
        self.assertNotIn("@machine {", rendered)
        self.assertNotIn("redir https://", rendered)
        self.assertNotIn("BEGIN_APEX_VHOST", rendered)

    def test_edge_api_domain_skips_apex_split(self) -> None:
        rendered = _render(api_domain="api-us4.tokenkey.dev")
        self.assertIn("api-us4.tokenkey.dev {", rendered)
        self.assertNotRegex(rendered, r"(?m)^us4\\.tokenkey\\.dev \\{")
        self.assertNotIn("@machine {", rendered)
        self.assertIn("import tokenkey_reverse_proxy", rendered)

    def test_explicit_site_domain_override(self) -> None:
        rendered = _render(api_domain="api.custom.example", site_domain="custom.example")
        self.assertIn("custom.example {", rendered)
        self.assertIn("redir https://custom.example{uri} permanent", rendered)
        self.assertIn("api.custom.example {", rendered)

    def test_global_homepage_is_disabled_by_default(self) -> None:
        rendered = _render(api_domain="api.tokenkey.dev")

        self.assertNotIn("global.tokenkey.dev {", rendered)
        self.assertNotIn("GLOBAL_REDIRECT_STATUS", rendered)
        self.assertNotIn("BEGIN_GLOBAL_VHOST", rendered)

    def test_candidate_global_homepage_is_allowlisted_and_noindex(self) -> None:
        rendered = _render(
            api_domain="api.tokenkey.dev",
            global_site_domain="global.tokenkey.dev",
            global_site_phase="candidate",
        )

        self.assertIn("global.tokenkey.dev {", rendered)
        self.assertIn('header X-Robots-Tag "noindex, nofollow"', rendered)
        self.assertIn("path /seedance-2-5-official-showcase-8b37bc3e.mp4", rendered)
        self.assertIn("path /api/v1/settings/public", rendered)
        self.assertIn("path /api/v1/auth/refresh", rendered)
        self.assertIn("path /api/v1/auth/me", rendered)
        self.assertIn("redir https://tokenkey.dev{uri} 302", rendered)
        self.assertNotIn("BEGIN_GLOBAL_NOINDEX", rendered)

    def test_live_global_homepage_uses_permanent_redirect_without_noindex(self) -> None:
        rendered = _render(
            api_domain="api.tokenkey.dev",
            global_site_domain="global.tokenkey.dev",
            global_site_phase="live",
        )

        self.assertIn("global.tokenkey.dev {", rendered)
        self.assertNotIn("X-Robots-Tag", rendered)
        self.assertIn("redir https://tokenkey.dev{uri} 301", rendered)

    def test_enabled_global_phase_requires_an_explicit_hostname(self) -> None:
        env = {
            **os.environ,
            "API_DOMAIN": "api.tokenkey.dev",
            "ACME_EMAIL": "ops@example.com",
            "GLOBAL_SITE_PHASE": "candidate",
        }
        with tempfile.NamedTemporaryFile("w+", suffix=".caddy") as tmp:
            proc = subprocess.run(
                ["bash", str(_RENDER), str(_TEMPLATE), tmp.name],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )

        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("GLOBAL_SITE_DOMAIN is required", proc.stderr)

    def test_enabled_global_phase_requires_a_resolvable_apex_hostname(self) -> None:
        env = {
            **os.environ,
            "API_DOMAIN": "localhost",
            "ACME_EMAIL": "ops@example.com",
            "GLOBAL_SITE_DOMAIN": "global.tokenkey.dev",
            "GLOBAL_SITE_PHASE": "candidate",
        }
        with tempfile.NamedTemporaryFile("w+", suffix=".caddy") as tmp:
            proc = subprocess.run(
                ["bash", str(_RENDER), str(_TEMPLATE), tmp.name],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )

        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("SITE_DOMAIN must resolve", proc.stderr)


if __name__ == "__main__":
    unittest.main()
