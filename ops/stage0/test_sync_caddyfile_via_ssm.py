"""Render tests for ops/stage0/sync_caddyfile_via_ssm.sh."""

from __future__ import annotations

import copy
import importlib.util
import json
import os
import pathlib
import re
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


def _host_script(kind: str = "prod") -> str:
    """The host-side script this sync would run, for executing fragments of it."""
    proc, params = _run_sync(kind)
    assert params is not None, proc.stderr
    return _ssm_script_body(params)


def _run_fragment(script: str, start: str, end: str, prelude: str, epilogue: str = ""):
    """Execute one balanced fragment of the host script under bash.

    The markers are asserted to be unique and to bound a non-trivial slice, so a
    marker that drifts out of the script fails loudly here instead of yielding an
    empty fragment that every assertion would then pass vacuously.
    """
    for marker in (start, end):
        found = script.count(marker)
        if found != 1:
            raise AssertionError(f"fragment marker {marker!r} occurs {found} times, expected exactly 1")
    a = script.index(start)
    b = script.index(end, a)
    fragment = script[a:b]
    if len(fragment) <= len(start):
        raise AssertionError(f"fragment between {start!r} and {end!r} is degenerate: {fragment!r}")
    # The host script's own first line. Without it the fragment runs with
    # pipefail off, so an `awk … | tee` guard that exits non-zero inside a
    # pipeline would look like success here and the test would pass vacuously.
    harness = "set -euo pipefail\n" + prelude + "\n" + fragment + "\n" + epilogue + "\n"
    return subprocess.run(["bash", "-c", harness], capture_output=True, text=True, check=False)


class CaddyHealthPolicyTest(unittest.TestCase):
    """Reject unsafe adapted semantics, including nested/imported proxy routes."""

    def setUp(self) -> None:
        checker = pathlib.Path(__file__).resolve().parents[2] / "scripts/checks/caddy-health-policy.py"
        spec = importlib.util.spec_from_file_location("caddy_health_policy", checker)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.check_policy = module.check_policy
        self.proxy = {
            "handler": "reverse_proxy",
            "upstreams": [{"dial": "tokenkey-blue:8080"}],
            "health_checks": {"active": {
                "uri": "/health", "interval": 5_000_000_000, "timeout": 3_000_000_000,
            }},
            "load_balancing": {"try_duration": 30_000_000_000, "try_interval": 1_000_000_000},
        }

    def test_accepts_active_health_with_bounded_retries(self) -> None:
        self.check_policy({"routes": [{"handle": [self.proxy]}]})

    def test_rejects_passive_transport_cooldown_even_without_status_list(self) -> None:
        for passive in ({"fail_duration": 5_000_000_000}, {
            "fail_duration": 5_000_000_000, "max_fails": 1, "unhealthy_status": [502, 504],
        }):
            with self.subTest(passive=passive):
                broken = copy.deepcopy(self.proxy)
                broken["health_checks"]["passive"] = passive
                with self.assertRaisesRegex(ValueError, "passive health"):
                    self.check_policy({"routes": [self.proxy, {"handle": [broken]}]})

    def test_rejects_loss_of_drain_or_downtime_protection(self) -> None:
        for field, value in (("health_checks", {}), ("load_balancing", {}), ("upstreams", [])):
            with self.subTest(field=field):
                broken = copy.deepcopy(self.proxy)
                broken[field] = value
                with self.assertRaises(ValueError):
                    self.check_policy({"handle": [broken]})
        with self.assertRaisesRegex(ValueError, "no reverse proxy"):
            self.check_policy({"routes": []})


class CaddyTemplateSingleUpstreamTest(unittest.TestCase):
    """The active-color rewrite's awk exits 7 unless EXACTLY one block matches.

    Both shipped templates must therefore render exactly one
    `reverse_proxy … {` block, or the hot-sync fails closed on a live host. The
    """

    _REPO = pathlib.Path(__file__).resolve().parents[2]
    _BLOCK = re.compile(r"^[ \t]*reverse_proxy[ \t]+.*\{[ \t]*$", re.MULTILINE)

    def test_prod_template_renders_one_upstream_block_in_every_phase(self) -> None:
        render = self._REPO / "deploy/aws/stage0/render-prod-caddyfile.sh"
        template = self._REPO / "deploy/aws/stage0/Caddyfile"
        phases = [
            {"API_DOMAIN": "api.x.dev", "SITE_DOMAIN": "", "GLOBAL_SITE_PHASE": "disabled"},
            {"API_DOMAIN": "api.tokenkey.dev", "GLOBAL_SITE_PHASE": "disabled"},
            {
                "API_DOMAIN": "api.tokenkey.dev",
                "GLOBAL_SITE_PHASE": "candidate",
                "GLOBAL_SITE_DOMAIN": "callmodel.io",
            },
            {
                "API_DOMAIN": "api.tokenkey.dev",
                "GLOBAL_SITE_PHASE": "live",
                "GLOBAL_SITE_DOMAIN": "callmodel.io",
                "API_ALIAS_DOMAIN": "api.callmodel.io",
            },
        ]
        for phase in phases:
            with self.subTest(phase=phase.get("GLOBAL_SITE_PHASE"), alias=phase.get("API_ALIAS_DOMAIN")), tempfile.TemporaryDirectory() as tmp:
                out = pathlib.Path(tmp) / "Caddyfile"
                proc = subprocess.run(
                    ["bash", str(render), str(template), str(out)],
                    env={**os.environ, "ACME_EMAIL": "a@b.c", **phase},
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(proc.returncode, 0, proc.stderr)
                self.assertEqual(len(self._BLOCK.findall(out.read_text())), 1)


class SyncCaddyfileBlueGreenTest(unittest.TestCase):
    """Production blue/green sync preserves the active upstream."""

    def _rewrite(self, kind: str, color: str, body: str):
        with tempfile.TemporaryDirectory() as tmp:
            d = pathlib.Path(tmp)
            (d / "active-color").write_text(color)
            (d / "Caddyfile.new").write_text(body)
            script = _host_script(kind).replace("/var/lib/tokenkey/active-color", str(d / "active-color"))
            proc = _run_fragment(
                script,
                'if [ -r ' + str(d / "active-color"),
                "\necho === validate rendered config",
                f'sudo() {{ "$@"; }}\nCADDY_DIR={d}\nKIND={kind}',
            )
            return proc, (d / "Caddyfile.new").read_text()

    def test_blue_render_rewrites_upstream_to_active_color(self) -> None:
        proc, rendered = self._rewrite(
            "prod", "blue\n",
            "api.x {\n\treverse_proxy tokenkey:8080 {\n\t\theader_up Host {host}\n\t}\n}\n")
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("reverse_proxy tokenkey-blue:8080 {", rendered)
        self.assertNotIn("reverse_proxy tokenkey:8080", rendered)
        # The rest of the block must survive untouched.
        self.assertIn("header_up Host {host}", rendered)

    def test_prod_render_still_rewrites(self) -> None:
        proc, rendered = self._rewrite(
            "prod", "green\n",
            "api.x {\n\treverse_proxy tokenkey:8080 {\n\t\tflush_interval -1\n\t}\n}\n")
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("reverse_proxy tokenkey-green:8080 {", rendered)

    def test_invalid_active_color_fails_closed(self) -> None:
        proc, rendered = self._rewrite(
            "prod", "purple\n", "api.x {\n\treverse_proxy tokenkey:8080 {\n\t}\n}\n")
        self.assertNotEqual(proc.returncode, 0, f"must refuse: {proc.stdout}")
        self.assertIn("invalid active-color", proc.stdout + proc.stderr)
        self.assertIn("reverse_proxy tokenkey:8080", rendered)

    def test_empty_active_color_fails_closed(self) -> None:
        """A truncated active-color must refuse, not fall back to a guess."""
        proc, rendered = self._rewrite(
            "prod", "", "api.x {\n\treverse_proxy tokenkey:8080 {\n\t}\n}\n")
        self.assertNotEqual(proc.returncode, 0, f"must refuse: {proc.stdout}")
        self.assertIn("invalid active-color", proc.stdout + proc.stderr)
        self.assertIn("reverse_proxy tokenkey:8080", rendered)

    def test_host_without_active_color_is_left_alone(self) -> None:
        """Widening the gate must not touch a host that never went blue/green.

        Such hosts run a single container named `tokenkey`; rewriting them to
        tokenkey-<color>:8080 would be the mirror image of the bug being fixed.
        """
        body = "api.x {\n\treverse_proxy tokenkey:8080 {\n\t\tflush_interval -1\n\t}\n}\n"
        with tempfile.TemporaryDirectory() as tmp:
            d = pathlib.Path(tmp)
            (d / "Caddyfile.new").write_text(body)
            absent = d / "active-color"  # deliberately never created
            script = _host_script("prod").replace("/var/lib/tokenkey/active-color", str(absent))
            proc = _run_fragment(
                script,
                "if [ -r " + str(absent),
                "\necho === validate rendered config",
                f'sudo() {{ "$@"; }}\nCADDY_DIR={d}\nKIND=prod',
            )
            self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
            self.assertEqual((d / "Caddyfile.new").read_text(), body, "legacy host must be untouched")

    def test_rewrite_requires_exactly_one_reverse_proxy_block(self) -> None:
        """The awk guard is what keeps a template drift from silently half-rewriting."""
        proc, rendered = self._rewrite(
            "prod", "blue\n",
            "api.x {\n\treverse_proxy a:1 {\n\t}\n\treverse_proxy b:2 {\n\t}\n}\n")
        self.assertNotEqual(proc.returncode, 0, f"must refuse two blocks: {proc.stdout}")
        self.assertIn("reverse_proxy a:1", rendered, "original must survive a refused rewrite")


class SyncCaddyfileRenderTest(unittest.TestCase):
    def test_ssm_commands_are_single_bash_c_for_dash_hybrid(self) -> None:
        """Hybrid SSM uses /bin/sh; pipefail must run under bash -c."""
        proc, params = _run_sync("prod")
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
        self.assertIn("LEGAL_DIR=", joined)
        self.assertIn("live Caddyfile from container mount", joined)
        self.assertIn("legal dir from container mount", joined)
        self.assertIn("/etc/caddy/Caddyfile", joined)
        self.assertIn("tokenkey-legal.tgz.b64", joined)
        self.assertIn("write legal pages", joined)
        self.assertNotIn("data/status/index.html", joined)

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

    def test_acme_email_override_wins_over_empty_host_env(self) -> None:
        proc, params = _run_sync("prod", {"ACME_EMAIL": "ops@example.com"})
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert params is not None
        joined = _ssm_script_body(params)
        self.assertIn("TARGET_ACME_EMAIL='ops@example.com'", joined)
        self.assertIn('if [ -n "$TARGET_ACME_EMAIL" ]; then ACME_EMAIL="$TARGET_ACME_EMAIL"; fi', joined)

class SyncCaddyfileEncodeVerifyTest(unittest.TestCase):
    """Post-reload encode guards: prod whitelist and no shorthand."""

    _WHITELIST = textwrap.dedent(
        """\
        (tokenkey_encode_static) {
        	encode {
        		zstd
        		gzip 6
        		match {
        			header Content-Type application/json*
        			header Content-Type text/html*
        		}
        	}
        }
        """
    )
    _SHORTHAND = "api.x {\n\tencode zstd gzip\n}\n"
    _NO_ENCODE = "api.x {\n\t# No encode: SSE relay\n\treverse_proxy tokenkey:8080 {\n\t}\n}\n"

    def _run_verify(self, kind: str, live_body: str):
        script = _host_script(kind)
        self.assertIn("live Caddyfile still has shorthand encode", script)
        with tempfile.TemporaryDirectory() as tmp:
            live = pathlib.Path(tmp) / "Caddyfile"
            live.write_text(live_body)
            # Slice only the encode-related verify predicates (docker inspect is
            # incidental and unavailable in unit tests).
            start = 'if grep -nE \'^[[:space:]]*encode[[:space:]]+(zstd[[:space:]]+gzip|gzip[[:space:]]+zstd)[[:space:]]*$\' "$LIVE"'
            end = "trap - ERR"
            return _run_fragment(
                script,
                start,
                end,
                f'LIVE={shlex.quote(str(live))}\nKIND={shlex.quote(kind)}\n',
            )

    def test_prod_accepts_whitelist_encode(self) -> None:
        proc = self._run_verify("prod", self._WHITELIST)
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)

    def test_prod_rejects_missing_whitelist(self) -> None:
        proc = self._run_verify("prod", self._NO_ENCODE)
        self.assertNotEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("prod encode Content-Type whitelist missing", proc.stdout + proc.stderr)

    def test_prod_rejects_shorthand_encode(self) -> None:
        proc = self._run_verify("prod", self._SHORTHAND)
        self.assertNotEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("shorthand encode", proc.stdout + proc.stderr)

if __name__ == "__main__":
    unittest.main()
