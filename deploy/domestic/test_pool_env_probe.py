"""Guard cn-prod pool-env probe targets against compose.override.yml drift."""

from __future__ import annotations

import re
import unittest
from pathlib import Path

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

    def test_probe_documents_compose_override_as_ssot(self):
        text = PROBE.read_text()
        self.assertIn("deploy/domestic/compose.override.yml", text)
        self.assertIn("SSOT", text)

    def test_compose_override_sets_domestic_proxy_probe_preset(self):
        text = OVERRIDE.read_text()
        self.assertRegex(
            text,
            r'(?m)^\s*SECURITY_PROXY_PROBE_PRESET:\s*"domestic"\s*$',
            "cn-prod must default proxy exit probes to the domestic builtin chain",
        )


if __name__ == "__main__":
    unittest.main()
