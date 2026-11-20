"""Guard cn-prod pool-env probe targets against compose.override.yml drift."""

from __future__ import annotations

import json
import os
import re
import subprocess
import unittest
from pathlib import Path

from deploy.domestic import node_compose

ROOT = Path(__file__).resolve().parents[2]
OVERRIDE = ROOT / "deploy/domestic/compose.override.yml"
PROBE = ROOT / "ops/observability/probe-cn-prod-pool-env.sh"
TARGET = ROOT / "deploy/domestic/prod-target.json"
BASE = ROOT / "deploy/stage0/docker-compose.yml"

KEYS = {
    "GOMEMLIMIT": "WANT_GOMEM",
    "DATABASE_MAX_OPEN_CONNS": "WANT_DB_OPEN",
    "DATABASE_MAX_IDLE_CONNS": "WANT_DB_IDLE",
    "REDIS_POOL_SIZE": "WANT_REDIS_POOL",
    "REDIS_MIN_IDLE_CONNS": "WANT_REDIS_IDLE",
}


def override_pool_values(text: str) -> dict[str, str]:
    values: dict[str, str] = {}
    for key in KEYS:
        match = re.search(rf'(?m)^\s*{re.escape(key)}:\s*"([^"]+)"', text)
        if not match:
            raise AssertionError(f"missing {key} in compose.override.yml")
        values[key] = match.group(1)
    return values


def probe_want_values(text: str) -> dict[str, str]:
    values: dict[str, str] = {}
    for key, want_name in KEYS.items():
        match = re.search(rf'(?m)^export {re.escape(want_name)}="([^"]+)"', text)
        if not match:
            raise AssertionError(f"missing export {want_name} in probe")
        values[key] = match.group(1)
    return values


def compose_env(**extra: str) -> dict[str, str]:
    env = os.environ.copy()
    env.update({
        "JWT_SECRET": "dummy-jwt",
        "TOTP_ENCRYPTION_KEY": "dummy-totp",
        "POSTGRES_PASSWORD": "dummy-pg",
        "TOKENKEY_IMAGE": "tokenkey:local",
        "POSTGRES_IMAGE": "postgres:18-alpine",
        "REDIS_IMAGE": "redis:8-alpine",
        "CADDY_IMAGE": "caddy:2",
        "DATABASE_HOST": "10.2.4.5",
        "REDIS_HOST": "10.2.4.5",
        **extra,
    })
    return env


class PoolEnvProbeSSOTTest(unittest.TestCase):
    def test_probe_want_exports_match_compose_override(self):
        override = override_pool_values(OVERRIDE.read_text())
        probe = probe_want_values(PROBE.read_text())
        self.assertEqual(probe, override)

    def test_probe_documents_compose_override_as_ssot(self):
        text = PROBE.read_text()
        self.assertIn("deploy/domestic/compose.override.yml", text)
        self.assertIn("SSOT", text)

    def test_override_app_env_reaches_compose_config(self):
        """Rolling reads compose as SSOT; every override env key must render.

        Pre-M6 blue/green inherited live-container env and overlayed only named
        tuples. After M6 the single container reads compose directly, so a key
        present in override but missing from `docker compose config` would
        silently never reach the gateway (same class of bug as
        SECURITY_PROXY_PROBE_PRESET on 2026-10-23).
        """
        text = OVERRIDE.read_text()
        service = re.search(r'(?ms)^  tokenkey:\n(.*?)(?=^  \S|\Z)', text)
        self.assertIsNotNone(service, "compose.override.yml must define the tokenkey service")
        environment = re.search(r'(?ms)^    environment:\n(.*?)(?=^    \S|\Z)', service.group(1))
        self.assertIsNotNone(environment, "tokenkey service must declare environment")
        declared = set(re.findall(r'(?m)^\s*([A-Z][A-Z0-9_]*):\s', environment.group(1)))
        self.assertIn("SECURITY_PROXY_PROBE_PRESET", declared)

        proc = subprocess.run(
            ["docker", "compose", "-f", str(BASE), "-f", str(OVERRIDE), "config", "--format", "json"],
            env=compose_env(),
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        rendered = json.loads(proc.stdout)["services"]["tokenkey"]["environment"]
        if isinstance(rendered, list):
            rendered = dict(item.split("=", 1) for item in rendered if "=" in item)
        missing = sorted(k for k in declared if k not in rendered)
        self.assertEqual(missing, [], f"override env missing from compose config: {missing}")

    def test_compose_override_sets_domestic_proxy_probe_preset(self):
        text = OVERRIDE.read_text()
        self.assertRegex(
            text,
            r'(?m)^\s*SECURITY_PROXY_PROBE_PRESET:\s*"domestic"\s*$',
            "cn-prod must default proxy exit probes to the domestic builtin chain",
        )

    def test_relay_gate_cap_is_declared_and_in_node_compose(self):
        text = OVERRIDE.read_text()
        self.assertRegex(
            text,
            r'(?m)^\s*GATEWAY_MAX_IN_FLIGHT_REQUESTS:\s*"[1-9][0-9]*"\s*$',
            "cn-prod must ship a non-zero relay concurrency cap, not the disabled default",
        )
        want = node_compose.default_max_in_flight(OVERRIDE)
        apps = json.loads(TARGET.read_text())["apps"]
        rendered = node_compose.render(apps[0], max_in_flight=want)
        self.assertIn(f'GATEWAY_MAX_IN_FLIGHT_REQUESTS: "{want}"', rendered)


class LocalDataProfileTest(unittest.TestCase):
    def _config(self, *extra: str) -> dict:
        proc = subprocess.run(
            ["docker", "compose", "-f", str(BASE), "-f", str(OVERRIDE), *extra, "config", "--format", "json"],
            env=compose_env(),
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        return json.loads(proc.stdout)

    def test_default_project_resolves_the_gateway_without_local_servers(self):
        doc = self._config()
        # App-edge caddy is on legacy-edge profile; default project is gateway only.
        self.assertEqual(set(doc["services"]), {"tokenkey"})
        tokenkey = doc["services"]["tokenkey"]
        self.assertEqual(tokenkey.get("depends_on") or {}, {})
        self.assertEqual(tokenkey["environment"]["DATABASE_HOST"], "10.2.4.5")
        self.assertEqual(tokenkey["environment"]["REDIS_HOST"], "10.2.4.5")

    def test_local_data_profile_is_what_starts_the_old_servers(self):
        doc = self._config("--profile", "local-data")
        self.assertEqual(set(doc["services"]), {"tokenkey", "postgres", "redis"})
        self.assertEqual(doc["services"]["postgres"].get("profiles"), ["local-data"])
        self.assertEqual(doc["services"]["redis"].get("profiles"), ["local-data"])
        self.assertEqual(doc["services"]["tokenkey"].get("depends_on") or {}, {})

    def test_legacy_edge_profile_is_what_exposes_retired_app_caddy(self):
        doc = self._config("--profile", "legacy-edge")
        self.assertEqual(set(doc["services"]), {"tokenkey", "caddy"})
        self.assertEqual(doc["services"]["caddy"].get("profiles"), ["legacy-edge"])

    def test_memory_cap_applies_to_the_gateway_only(self):
        """The cap must not leak onto Caddy, the retired local servers, or any
        other service: those have their own memory needs and, for PG/Redis, live
        on the data machine."""
        doc = self._config("--profile", "local-data", "--profile", "legacy-edge")
        tokenkey = doc["services"]["tokenkey"]
        self.assertEqual(tokenkey["mem_limit"], str(3 * 1024 * 1024 * 1024))
        self.assertEqual(tokenkey["memswap_limit"], str(3 * 1024 * 1024 * 1024))
        # memswap == mem: no extra swap for this cgroup.
        self.assertEqual(tokenkey["mem_limit"], tokenkey["memswap_limit"])
        for other in ("caddy", "postgres", "redis"):
            self.assertNotIn("mem_limit", doc["services"][other], other)
            self.assertNotIn("memswap_limit", doc["services"][other], other)


if __name__ == "__main__":
    unittest.main()
