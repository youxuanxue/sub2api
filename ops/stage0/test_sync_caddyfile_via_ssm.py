#!/usr/bin/env python3
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


def _host_script(kind: str = "edge") -> str:
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


class SyncCaddyfileCidrRecoveryTest(unittest.TestCase):
    """A recovered remote_ip allowlist must be validated, not trusted.

    The live Caddyfile can itself be the product of an earlier bad write; that is
    how two edges were rendered with a blank relay allowlist on 2026-10-11.
    Recovery now fails closed instead of opening the allowlist.
    """

    def _recover(self, live_body: str) -> subprocess.CompletedProcess:
        with tempfile.TemporaryDirectory() as tmp:
            live = pathlib.Path(tmp) / "Caddyfile"
            live.write_text(live_body)
            return _run_fragment(
                _host_script("edge"),
                'if [ -n "$TARGET_MAIN_GATEWAY_ALLOWED_CIDR" ]',
                'echo "render context loaded',
                f'KIND=edge\nLIVE={live}\nTARGET_MAIN_GATEWAY_ALLOWED_CIDR=',
                'echo "RECOVERED=$MAIN_GATEWAY_ALLOWED_CIDR"',
            )

    def test_recovers_a_valid_allowlist(self) -> None:
        proc = self._recover("api.x {\n\t@main {\n\t\tremote_ip 167.233.211.115/32\n\t}\n}\n")
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("RECOVERED=167.233.211.115/32", proc.stdout)

    def test_recovers_a_multi_entry_allowlist(self) -> None:
        proc = self._recover("api.x {\n\t@main {\n\t\tremote_ip 1.2.3.4/32 5.6.7.8/32\n\t}\n}\n")
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("RECOVERED=1.2.3.4/32 5.6.7.8/32", proc.stdout)

    def test_fails_closed_on_a_blank_allowlist(self) -> None:
        # The exact shape of the 2026-10-11 breakage: remote_ip present but empty.
        proc = self._recover("api.x {\n\t@main {\n\t\tremote_ip \n\t}\n}\n")
        self.assertNotEqual(proc.returncode, 0, f"must refuse: {proc.stdout}")
        self.assertNotIn("RECOVERED=", proc.stdout)

    def test_fails_closed_when_remote_ip_is_missing(self) -> None:
        proc = self._recover("api.x {\n\treverse_proxy tokenkey:8080\n}\n")
        self.assertNotEqual(proc.returncode, 0, f"must refuse: {proc.stdout}")

    def test_fails_closed_on_a_prefixless_value(self) -> None:
        proc = self._recover("api.x {\n\t@main {\n\t\tremote_ip 167.233.211.115\n\t}\n}\n")
        self.assertNotEqual(proc.returncode, 0, f"must refuse a bare IP: {proc.stdout}")
        self.assertIn("prefix length", proc.stdout + proc.stderr)

    def test_recovers_an_ipv6_allowlist(self) -> None:
        proc = self._recover("api.x {\n\t@main {\n\t\tremote_ip 2a01:4f8:c17:1::1/128\n\t}\n}\n")
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("RECOVERED=2a01:4f8:c17:1::1/128", proc.stdout)

    def test_refuses_an_all_addresses_v4_allowlist(self) -> None:
        """0.0.0.0/0 is a syntactically valid CIDR and the exact danger case.

        A blank remote_ip merely breaks relay (Caddy stops matching @allowed_relay,
        so /v1/* falls through to the 403 handler). An all-addresses entry is the
        direction that actually exposes the host, so recovery must refuse it.
        """
        proc = self._recover("api.x {\n\t@main {\n\t\tremote_ip 0.0.0.0/0\n\t}\n}\n")
        self.assertNotEqual(proc.returncode, 0, f"must refuse an open allowlist: {proc.stdout}")
        self.assertIn("every address", proc.stdout + proc.stderr)
        self.assertNotIn("RECOVERED=", proc.stdout)

    def test_refuses_an_all_addresses_v6_allowlist(self) -> None:
        proc = self._recover("api.x {\n\t@main {\n\t\tremote_ip ::/0\n\t}\n}\n")
        self.assertNotEqual(proc.returncode, 0, f"must refuse an open allowlist: {proc.stdout}")
        self.assertIn("every address", proc.stdout + proc.stderr)

    def test_refuses_an_open_entry_mixed_into_a_valid_list(self) -> None:
        """Per-entry validation: one bad entry in a list must not ride along."""
        proc = self._recover("api.x {\n\t@main {\n\t\tremote_ip 1.2.3.4/32 0.0.0.0/0\n\t}\n}\n")
        self.assertNotEqual(proc.returncode, 0, f"must refuse: {proc.stdout}")
        self.assertIn("every address", proc.stdout + proc.stderr)

    def test_refuses_a_prefix_without_an_address(self) -> None:
        proc = self._recover("api.x {\n\t@main {\n\t\tremote_ip /32\n\t}\n}\n")
        self.assertNotEqual(proc.returncode, 0, f"must refuse: {proc.stdout}")
        self.assertIn("not an IP address", proc.stdout + proc.stderr)

    def test_refuses_an_address_without_a_prefix_value(self) -> None:
        proc = self._recover("api.x {\n\t@main {\n\t\tremote_ip 1.2.3.4/\n\t}\n}\n")
        self.assertNotEqual(proc.returncode, 0, f"must refuse: {proc.stdout}")
        self.assertIn("non-numeric prefix length", proc.stdout + proc.stderr)

    def test_refuses_an_out_of_range_v4_prefix(self) -> None:
        proc = self._recover("api.x {\n\t@main {\n\t\tremote_ip 1.2.3.4/33\n\t}\n}\n")
        self.assertNotEqual(proc.returncode, 0, f"must refuse: {proc.stdout}")
        self.assertIn("out-of-range", proc.stdout + proc.stderr)

    def test_refuses_a_caddy_placeholder(self) -> None:
        """A remote_ip rendered as a placeholder is not a literal allowlist."""
        proc = self._recover("api.x {\n\t@main {\n\t\tremote_ip {remote_host}\n\t}\n}\n")
        self.assertNotEqual(proc.returncode, 0, f"must refuse: {proc.stdout}")


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
    edge template keeps its two handles on one `(tk_upstream)` snippet, which is
    what makes that hold there.
    """

    _REPO = pathlib.Path(__file__).resolve().parents[2]
    _BLOCK = re.compile(r"^[ \t]*reverse_proxy[ \t]+.*\{[ \t]*$", re.M)

    def test_edge_template_renders_one_upstream_block(self) -> None:
        template = (self._REPO / "deploy/aws/stage0/Caddyfile.edge").read_text()
        rendered = (
            template.replace("${API_DOMAIN}", "api.x")
            .replace("${ACME_EMAIL}", "a@b.c")
            .replace("${MAIN_GATEWAY_ALLOWED_CIDR}", "1.2.3.4/32")
        )
        self.assertEqual(len(self._BLOCK.findall(rendered)), 1)

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
            with self.subTest(phase=phase.get("GLOBAL_SITE_PHASE"), alias=phase.get("API_ALIAS_DOMAIN")):
                with tempfile.TemporaryDirectory() as tmp:
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


class SyncCaddyfileExplicitCidrOverrideTest(unittest.TestCase):
    """The explicit MAIN_GATEWAY_ALLOWED_CIDR override takes the same rule.

    It bypasses host-side recovery entirely, so validating only the recovered
    value would leave the open-relay shape reachable through the override.
    """

    def test_rejects_an_all_addresses_override(self) -> None:
        proc, params = _run_sync("edge", {"MAIN_GATEWAY_ALLOWED_CIDR": "0.0.0.0/0"})
        self.assertNotEqual(proc.returncode, 0, proc.stdout)
        self.assertIn("non-zero prefix length", proc.stderr)
        self.assertIsNone(params, "must refuse before building the SSM payload")

    def test_rejects_an_all_addresses_v6_override(self) -> None:
        proc, _ = _run_sync("edge", {"MAIN_GATEWAY_ALLOWED_CIDR": "::/0"})
        self.assertNotEqual(proc.returncode, 0, proc.stdout)
        self.assertIn("non-zero prefix length", proc.stderr)

    def test_rejects_a_prefixless_override(self) -> None:
        proc, _ = _run_sync("edge", {"MAIN_GATEWAY_ALLOWED_CIDR": "1.2.3.4"})
        self.assertNotEqual(proc.returncode, 0, proc.stdout)
        self.assertIn("non-zero prefix length", proc.stderr)

    def test_accepts_a_normal_override(self) -> None:
        proc, params = _run_sync("edge", {"MAIN_GATEWAY_ALLOWED_CIDR": "167.233.211.115/32"})
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        assert params is not None
        self.assertIn("167.233.211.115/32", _ssm_script_body(params))


class SyncCaddyfileBlueGreenEdgeTest(unittest.TestCase):
    """Edges run blue/green too, so the active-color rewrite is not prod-only.

    Gating it on kind=prod left a blue/green edge rendering the template's literal
    tokenkey:8080 and pointing Caddy at a container that does not exist there.
    """

    def test_rewrite_is_not_gated_on_prod(self) -> None:
        script = _host_script("edge")
        self.assertIn("/var/lib/tokenkey/active-color", script)
        self.assertNotIn('if [ "$KIND" = prod ] && [ -r /var/lib/tokenkey/active-color ]', script)

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

    def test_edge_render_rewrites_upstream_to_active_color(self) -> None:
        proc, rendered = self._rewrite(
            "edge", "blue\n",
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
            "edge", "purple\n", "api.x {\n\treverse_proxy tokenkey:8080 {\n\t}\n}\n")
        self.assertNotEqual(proc.returncode, 0, f"must refuse: {proc.stdout}")
        self.assertIn("invalid active-color", proc.stdout + proc.stderr)
        self.assertIn("reverse_proxy tokenkey:8080", rendered)

    def test_empty_active_color_fails_closed(self) -> None:
        """A truncated active-color must refuse, not fall back to a guess."""
        proc, rendered = self._rewrite(
            "edge", "", "api.x {\n\treverse_proxy tokenkey:8080 {\n\t}\n}\n")
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
            script = _host_script("edge").replace("/var/lib/tokenkey/active-color", str(absent))
            proc = _run_fragment(
                script,
                "if [ -r " + str(absent),
                "\necho === validate rendered config",
                f'sudo() {{ "$@"; }}\nCADDY_DIR={d}\nKIND=edge',
            )
            self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
            self.assertEqual((d / "Caddyfile.new").read_text(), body, "legacy host must be untouched")

    def test_rewrite_requires_exactly_one_reverse_proxy_block(self) -> None:
        """The awk guard is what keeps a template drift from silently half-rewriting."""
        proc, rendered = self._rewrite(
            "edge", "blue\n",
            "api.x {\n\treverse_proxy a:1 {\n\t}\n\treverse_proxy b:2 {\n\t}\n}\n")
        self.assertNotEqual(proc.returncode, 0, f"must refuse two blocks: {proc.stdout}")
        self.assertIn("reverse_proxy a:1", rendered, "original must survive a refused rewrite")


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


class SyncCaddyfileEncodeVerifyTest(unittest.TestCase):
    """Post-reload encode guards: prod whitelist / edge no-encode / no shorthand."""

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

    def test_edge_accepts_no_encode(self) -> None:
        proc = self._run_verify("edge", self._NO_ENCODE)
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)

    def test_edge_rejects_any_encode_directive(self) -> None:
        proc = self._run_verify("edge", self._WHITELIST)
        self.assertNotEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("must not enable encode", proc.stdout + proc.stderr)

    def test_edge_rejects_shorthand_encode(self) -> None:
        proc = self._run_verify("edge", self._SHORTHAND)
        self.assertNotEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("shorthand encode", proc.stdout + proc.stderr)


if __name__ == "__main__":
    unittest.main()
