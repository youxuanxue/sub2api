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
    api_alias_domain: str = "",
) -> str:
    env = {
        **os.environ,
        "API_DOMAIN": api_domain,
        "ACME_EMAIL": acme_email,
        "SITE_DOMAIN": site_domain,
        "GLOBAL_SITE_DOMAIN": global_site_domain,
        "GLOBAL_SITE_PHASE": global_site_phase,
        "API_ALIAS_DOMAIN": api_alias_domain,
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
        self.assertNotIn("api.callmodel.io {", rendered)

    def test_apex_serves_public_legal_pages_before_reverse_proxy(self) -> None:
        rendered = _render(api_domain="api.tokenkey.dev")
        apex = rendered[rendered.index("tokenkey.dev {") : rendered.index("api.tokenkey.dev {")]
        self.assertIn("handle /privacy", apex)
        self.assertIn("handle /terms", apex)
        self.assertIn("handle_path /legal-assets/*", apex)
        self.assertIn("root * /data/legal", apex)
        self.assertLess(apex.index("handle /privacy"), apex.index("import tokenkey_reverse_proxy"))
        self.assertEqual(rendered.count("reverse_proxy tokenkey:8080"), 1)

    def test_localhost_skips_apex_and_uses_full_api_proxy(self) -> None:
        rendered = _render(api_domain="localhost")
        self.assertIn("localhost {", rendered)
        self.assertIn("import tokenkey_reverse_proxy", rendered)
        self.assertNotIn("tokenkey.dev {", rendered)
        self.assertNotIn("@machine {", rendered)
        self.assertNotIn("tokenkey_api_machine", rendered)
        self.assertNotIn("redir https://", rendered)
        self.assertNotIn("BEGIN_APEX_VHOST", rendered)
        self.assertNotIn("handle /privacy", rendered)

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
        self.assertIn("handle /privacy", rendered)

    def test_global_homepage_is_disabled_by_default(self) -> None:
        rendered = _render(api_domain="api.tokenkey.dev")

        self.assertNotIn("callmodel.io {", rendered)
        self.assertNotIn("global.tokenkey.dev {", rendered)
        self.assertNotIn("GLOBAL_REDIRECT_STATUS", rendered)
        self.assertNotIn("BEGIN_GLOBAL_VHOST", rendered)

    def test_candidate_callmodel_facade_proxies_spa_and_kicks_admin_only(self) -> None:
        rendered = _render(
            api_domain="api.tokenkey.dev",
            global_site_domain="callmodel.io",
            global_site_phase="candidate",
        )

        self.assertIn("callmodel.io {", rendered)
        global_block = rendered[
            rendered.index("callmodel.io {") : rendered.index("(tokenkey_api_machine)")
        ]
        self.assertNotIn("X-Robots-Tag", global_block)
        self.assertIn("@callmodel_admin", global_block)
        self.assertIn("path /admin*", global_block)
        self.assertIn("redir https://tokenkey.dev{uri} 302", global_block)
        self.assertIn("handle /privacy", global_block)
        self.assertIn("handle /terms", global_block)
        self.assertIn("import tokenkey_reverse_proxy", global_block)
        # Full facade: no homepage-only allowlist / catch-all kick.
        self.assertNotIn("@global_home", global_block)
        self.assertNotIn("@global_setup_status", global_block)
        self.assertNotIn("@global_runtime", global_block)
        # Admin kick must precede the SPA reverse proxy.
        self.assertLess(
            global_block.index("handle @callmodel_admin"),
            global_block.index("import tokenkey_reverse_proxy"),
        )

    def test_candidate_callmodel_facade_does_not_redirect_product_paths(self) -> None:
        rendered = _render(
            api_domain="api.tokenkey.dev",
            global_site_domain="callmodel.io",
            global_site_phase="candidate",
        )
        global_block = rendered[
            rendered.index("callmodel.io {") : rendered.index("(tokenkey_api_machine)")
        ]
        # Only /admin* redirects off-host; product paths stay on the facade SPA.
        self.assertEqual(global_block.count("redir https://tokenkey.dev{uri}"), 1)
        self.assertIn("path /admin*", global_block)

    def test_live_callmodel_facade_uses_permanent_admin_redirect(self) -> None:
        rendered = _render(
            api_domain="api.tokenkey.dev",
            global_site_domain="callmodel.io",
            global_site_phase="live",
        )

        self.assertIn("callmodel.io {", rendered)
        self.assertIn("redir https://tokenkey.dev{uri} 301", rendered)
        self.assertIn("path /admin*", rendered)

    def test_api_alias_reuses_shared_machine_snippet_and_lands_on_global_face(self) -> None:
        rendered = _render(
            api_domain="api.tokenkey.dev",
            global_site_domain="callmodel.io",
            global_site_phase="candidate",
            api_alias_domain="api.callmodel.io",
        )

        self.assertIn("api.callmodel.io {", rendered)
        self.assertEqual(rendered.count("import tokenkey_api_machine"), 2)
        self.assertEqual(rendered.count("\n\t@machine {\n"), 1)
        alias_block = rendered[rendered.index("api.callmodel.io {") :]
        self.assertIn("import tokenkey_api_machine", alias_block)
        self.assertNotIn("@machine {", alias_block)
        self.assertIn("redir https://callmodel.io{uri} permanent", alias_block)
        api_block = rendered[
            rendered.index("api.tokenkey.dev {") : rendered.index("api.callmodel.io {")
        ]
        self.assertIn("redir https://tokenkey.dev{uri} permanent", api_block)

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
            "GLOBAL_SITE_DOMAIN": "callmodel.io",
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

    def test_no_caddy_status_vhost(self) -> None:
        rendered = _render(api_domain="api.tokenkey.dev")
        self.assertNotIn("status.tokenkey.dev {", rendered)
        self.assertNotIn("root * /data/status", rendered)
        self.assertNotIn("STATUS_SITE_DOMAIN", rendered)


if __name__ == "__main__":
    unittest.main()
