#!/usr/bin/env python3
"""Render tests for ops/stage0/sync_caddyfile_via_ssm.sh."""

from __future__ import annotations

import json
import os
import pathlib
import shlex
import stat
import subprocess
import tempfile
import textwrap
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "sync_caddyfile_via_ssm.sh"


def _ssm_script_body(params: dict) -> str:
    """Inner script of the single `bash -c …` SSM command (dash-safe wrapper)."""
    commands = params["commands"]
    if len(commands) != 1 or not commands[0].startswith("bash -c "):
        raise AssertionError(f"expected one bash -c command, got {commands!r}")
    parts = shlex.split(commands[0])
    if len(parts) < 3 or parts[0] != "bash" or parts[1] != "-c":
        raise AssertionError(f"unparseable bash -c wrapper: {commands[0][:120]!r}")
    return parts[2]


def _run_sync(kind: str = "prod", extra_env: dict[str, str] | None = None):
    out_dir = pathlib.Path(tempfile.mkdtemp(prefix="sync-caddy-render-"))
    bin_dir = out_dir / "bin"
    bin_dir.mkdir()
    aws_stub = bin_dir / "aws"
    aws_stub.write_text(
        textwrap.dedent(
            """\
            #!/usr/bin/env bash
            case "$*" in
              *send-command*)                         echo "cmd-stub" ;;
              *get-command-invocation*Status*)        echo "Success" ;;
              *get-command-invocation*StandardOutput*) echo "stdout" ;;
              *get-command-invocation*StandardError*) echo "" ;;
              *)                                      echo "stub" ;;
            esac
            """
        )
    )
    aws_stub.chmod(aws_stub.stat().st_mode | stat.S_IXUSR)
    env = {
        **os.environ,
        "PATH": f"{bin_dir}:{os.environ['PATH']}",
        "AWS_REGION": "us-east-1",
        "STAGE0_SSM_OUTPUT_DIR": str(out_dir),
    }
    env.pop("GLOBAL_SITE_PHASE", None)
    env.pop("GLOBAL_SITE_DOMAIN", None)
    env.pop("MAIN_GATEWAY_ALLOWED_CIDR", None)
    env.pop("ACME_EMAIL", None)
    env.update(extra_env or {})
    proc = subprocess.run(
        ["bash", str(_SCRIPT), kind, "i-0stub", "probe"],
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    params = None
    params_path = out_dir / "ssm-params.json"
    if params_path.exists():
        params = json.loads(params_path.read_text())
    return proc, params


class SyncCaddyfileRenderTest(unittest.TestCase):
    def test_ssm_commands_are_single_bash_c_for_dash_hybrid(self) -> None:
        """Hybrid SSM uses /bin/sh; pipefail must run under bash -c."""
        proc, params = _run_sync("edge")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert params is not None
        self.assertEqual(len(params["commands"]), 1)
        self.assertTrue(params["commands"][0].startswith("bash -c "))
        body = _ssm_script_body(params)
        self.assertIn("set -euo pipefail", body)

    def test_prod_preserves_bluegreen_active_upstream(self) -> None:
        proc, params = _run_sync("prod")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert params is not None
        joined = _ssm_script_body(params)

        parsed = subprocess.run(
            ["bash", "-n"],
            input=joined,
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(parsed.returncode, 0, msg=parsed.stderr)
        self.assertIn("/var/lib/tokenkey/active-color", joined)
        self.assertIn("tokenkey-$ACTIVE_COLOR:8080", joined)
        self.assertIn("Caddyfile.rewritten", joined)
        self.assertIn("render-prod-caddyfile.sh", joined)
        self.assertIn("data/legal", joined)
        self.assertIn("tokenkey-legal.tgz.b64", joined)
        self.assertIn("write legal pages", joined)
        self.assertNotIn("data/status/index.html", joined)
        self.assertIn("envsubst '$API_DOMAIN $ACME_EMAIL $MAIN_GATEWAY_ALLOWED_CIDR'", joined)

    def test_prod_candidate_persists_domain_and_phase_before_render(self) -> None:
        proc, params = _run_sync(
            "prod",
            {
                "GLOBAL_SITE_PHASE": "candidate",
                "GLOBAL_SITE_DOMAIN": "callmodel.io",
                "API_ALIAS_DOMAIN": "api.callmodel.io",
            },
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert params is not None
        joined = _ssm_script_body(params)

        parsed = subprocess.run(
            ["bash", "-n"], input=joined, text=True, capture_output=True, check=False
        )
        self.assertEqual(parsed.returncode, 0, msg=parsed.stderr)
        self.assertIn("APPLY_GLOBAL_PROFILE='true'", joined)
        self.assertIn("TARGET_GLOBAL_SITE_PHASE='candidate'", joined)
        self.assertIn("TARGET_GLOBAL_SITE_DOMAIN='callmodel.io'", joined)
        self.assertIn("TARGET_API_ALIAS_DOMAIN='api.callmodel.io'", joined)
        self.assertLess(joined.index("global homepage phase persisted"), joined.index("render context loaded"))
        self.assertIn("restoring previous Caddyfile and environment", joined)

    def test_prod_disabled_clears_persisted_domain(self) -> None:
        proc, params = _run_sync("prod", {"GLOBAL_SITE_PHASE": "disabled"})
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert params is not None
        joined = _ssm_script_body(params)
        self.assertIn("TARGET_GLOBAL_SITE_PHASE='disabled'", joined)
        self.assertIn("TARGET_GLOBAL_SITE_DOMAIN=''", joined)

    def test_prod_preserves_existing_phase_when_inputs_are_omitted(self) -> None:
        proc, params = _run_sync("prod")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert params is not None
        self.assertIn("APPLY_GLOBAL_PROFILE='false'", _ssm_script_body(params))

    def test_enabled_phase_requires_valid_hostname(self) -> None:
        for domain in ("", "https://callmodel.io", "CALLMODEL.IO"):
            with self.subTest(domain=domain):
                proc, params = _run_sync(
                    "prod",
                    {"GLOBAL_SITE_PHASE": "candidate", "GLOBAL_SITE_DOMAIN": domain},
                )
                self.assertNotEqual(proc.returncode, 0)
                self.assertIsNone(params)

    def test_global_profile_inputs_are_rejected_for_edge(self) -> None:
        proc, params = _run_sync("edge", {"GLOBAL_SITE_PHASE": "disabled"})
        self.assertNotEqual(proc.returncode, 0)
        self.assertIsNone(params)

    def test_edge_allowlist_override_skips_live_remote_ip_scrape(self) -> None:
        proc, params = _run_sync("edge", {"MAIN_GATEWAY_ALLOWED_CIDR": "34.194.234.88/32"})
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert params is not None
        joined = _ssm_script_body(params)
        self.assertIn("TARGET_MAIN_GATEWAY_ALLOWED_CIDR='34.194.234.88/32'", joined)
        self.assertIn('if [ -n "$TARGET_MAIN_GATEWAY_ALLOWED_CIDR" ]', joined)
        self.assertIn("ACME_EMAIL empty in /var/lib/tokenkey/.env and live Caddyfile", joined)
        self.assertLess(
            joined.index("TARGET_MAIN_GATEWAY_ALLOWED_CIDR='34.194.234.88/32'"),
            joined.index("MAIN_GATEWAY_ALLOWED_CIDR="),
        )

    def test_acme_email_override_wins_over_empty_host_env(self) -> None:
        proc, params = _run_sync("edge", {"ACME_EMAIL": "ops@example.com"})
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert params is not None
        joined = _ssm_script_body(params)
        self.assertIn("TARGET_ACME_EMAIL='ops@example.com'", joined)
        self.assertIn('if [ -n "$TARGET_ACME_EMAIL" ]; then ACME_EMAIL="$TARGET_ACME_EMAIL"; fi', joined)

    def test_prod_rejects_main_gateway_cidr_override(self) -> None:
        proc, params = _run_sync("prod", {"MAIN_GATEWAY_ALLOWED_CIDR": "34.194.234.88/32"})
        self.assertNotEqual(proc.returncode, 0)
        self.assertIsNone(params)


if __name__ == "__main__":
    unittest.main()
