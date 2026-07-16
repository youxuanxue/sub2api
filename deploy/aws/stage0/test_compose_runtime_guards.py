#!/usr/bin/env python3
from __future__ import annotations

import pathlib
import re
import unittest

COMPOSE = pathlib.Path(__file__).with_name("docker-compose.yml")


def logging_violations(text: str) -> list[str]:
    violations: list[str] = []
    if "x-tokenkey-logging: &tokenkey-logging" not in text:
        violations.append("logging-anchor")
    if 'max-size: "100m"' not in text:
        violations.append("max-size")
    if 'max-file: "5"' not in text:
        violations.append("max-file")
    for service in ("caddy", "tokenkey", "postgres", "redis"):
        match = re.search(
            rf"^  {service}:\n(?P<body>.*?)(?=^  [a-z][a-z0-9_-]*:\n|^networks:)",
            text,
            re.M | re.S,
        )
        if not match or "logging: *tokenkey-logging" not in match.group("body"):
            violations.append(service)
    return violations


class ComposeRuntimeGuardsTest(unittest.TestCase):
    def test_all_stage0_services_use_bounded_json_logging(self) -> None:
        self.assertEqual(logging_violations(COMPOSE.read_text(encoding="utf-8")), [])

    def test_missing_service_policy_is_rejected(self) -> None:
        text = COMPOSE.read_text(encoding="utf-8").replace(
            "    logging: *tokenkey-logging\n", "", 1
        )
        self.assertIn("caddy", logging_violations(text))


if __name__ == "__main__":
    unittest.main()
