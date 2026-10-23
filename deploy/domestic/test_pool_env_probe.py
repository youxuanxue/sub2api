"""Guard cn-prod pool-env probe targets against compose.override.yml drift."""

from __future__ import annotations

import re
import unittest
from pathlib import Path

from deploy.domestic import bluegreen as bg

ROOT = Path(__file__).resolve().parents[2]
OVERRIDE = ROOT / "deploy/domestic/compose.override.yml"
PROBE = ROOT / "ops/observability/probe-cn-prod-pool-env.sh"

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


class PoolEnvProbeSSOTTest(unittest.TestCase):
    def test_probe_want_exports_match_compose_override(self):
        override = override_pool_values(OVERRIDE.read_text())
        probe = probe_want_values(PROBE.read_text())
        self.assertEqual(probe, override)

    def test_bluegreen_pool_env_keys_match_probe_ssot(self):
        self.assertEqual(set(bg.POOL_ENV_KEYS), set(KEYS))

    def test_probe_documents_compose_override_as_ssot(self):
        text = PROBE.read_text()
        self.assertIn("deploy/domestic/compose.override.yml", text)
        self.assertIn("SSOT", text)

    def test_every_override_app_env_key_propagates_through_bluegreen(self):
        """An override key that no *_ENV_KEYS tuple claims never reaches a new color.

        candidate_compose() inherits the candidate environment from the live
        container so secrets stay on the host, then overlays only the keys named
        in POOL_ENV_KEYS / QA_ENV_KEYS / CONFIG_ENV_KEYS. When
        SECURITY_PROXY_PROBE_PRESET was added to the override alone, every
        promote produced a color without it and nothing failed
        (found 2026-10-23, cn-prod 1.8.318). Keep that silent path closed.
        """
        text = OVERRIDE.read_text()
        service = re.search(r'(?ms)^  tokenkey:\n(.*?)(?=^  \S|\Z)', text)
        self.assertIsNotNone(service, "compose.override.yml must define the tokenkey service")
        environment = re.search(r'(?ms)^    environment:\n(.*?)(?=^    \S|\Z)', service.group(1))
        self.assertIsNotNone(environment, "tokenkey service must declare environment")
        declared = set(re.findall(r'(?m)^\s*([A-Z][A-Z0-9_]*):\s', environment.group(1)))
        self.assertIn("SECURITY_PROXY_PROBE_PRESET", declared)
        propagated = set(bg.POOL_ENV_KEYS) | set(bg.QA_ENV_KEYS) | set(bg.CONFIG_ENV_KEYS)
        missing = declared - propagated
        self.assertEqual(
            missing, set(),
            "compose.override.yml declares app env not propagated by blue/green: "
            + ", ".join(sorted(missing))
            + ". Add each to POOL_ENV_KEYS, QA_ENV_KEYS or CONFIG_ENV_KEYS in "
              "deploy/domestic/bluegreen.py, or the next promote serves without it.",
        )

    def test_compose_override_sets_domestic_proxy_probe_preset(self):
        text = OVERRIDE.read_text()
        self.assertRegex(
            text,
            r'(?m)^\s*SECURITY_PROXY_PROBE_PRESET:\s*"domestic"\s*$',
            "cn-prod must default proxy exit probes to the domestic builtin chain",
        )


if __name__ == "__main__":
    unittest.main()
